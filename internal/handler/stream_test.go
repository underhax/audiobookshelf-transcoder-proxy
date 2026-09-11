package handler

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/underhax/audiobookshelf-transcoder-proxy/internal/absclient"
	"github.com/underhax/audiobookshelf-transcoder-proxy/internal/session"
	"github.com/underhax/audiobookshelf-transcoder-proxy/internal/trackproxy"
)

func TestStream_Errors(t *testing.T) {
	t.Parallel()

	h, store := newTestEnv(t, nil)
	routes := h.Routes()

	reqNoToken := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/stream/non-existent.aac", http.NoBody)
	recNoToken := httptest.NewRecorder()
	routes.ServeHTTP(recNoToken, reqNoToken)
	if recNoToken.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for stream without token, got %d", recNoToken.Code)
	}

	reqInvalidToken := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/stream/non-existent.aac?token=bad-token", http.NoBody)
	recInvalidToken := httptest.NewRecorder()
	routes.ServeHTTP(recInvalidToken, reqInvalidToken)
	if recInvalidToken.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for stream with invalid token, got %d", recInvalidToken.Code)
	}

	sessEmptyTracks := &session.Session{
		ID:          "sess-empty-tracks",
		AudioTracks: []absclient.AudioTrack{},
	}
	tok, err := store.Create(sessEmptyTracks)
	if err != nil {
		t.Fatalf("store create sess: %v", err)
	}
	reqEmptyTracks := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/stream/sess-empty-tracks.aac?token="+tok, http.NoBody)
	recEmptyTracks := httptest.NewRecorder()
	routes.ServeHTTP(recEmptyTracks, reqEmptyTracks)
	if recEmptyTracks.Code != http.StatusInternalServerError {
		t.Errorf("expected 500 when audio tracks are empty, got %d", recEmptyTracks.Code)
	}

	sessBadFFmpeg := &session.Session{
		ID: "sess-bad-ffmpeg",
		AudioTracks: []absclient.AudioTrack{
			{Index: 0, Duration: 10.0, ContentURL: "/bad-ffmpeg-track.mp3"},
		},
	}
	tokBadFF, err := store.Create(sessBadFFmpeg)
	if err != nil {
		t.Fatalf("store create sessBadFFmpeg: %v", err)
	}
	hBadFF, _ := newTestEnv(t, nil)
	hBadFF.cfg.FFmpegPath = "non_existent_ffmpeg_bin_123"
	hBadFF.store = store
	reqBadFF := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/stream/sess-bad-ffmpeg.aac?token="+tokBadFF, http.NoBody)
	recBadFF := httptest.NewRecorder()
	hBadFF.Routes().ServeHTTP(recBadFF, reqBadFF)
	if recBadFF.Code != http.StatusInternalServerError {
		t.Errorf("expected 500 when ffmpeg binary missing, got %d", recBadFF.Code)
	}
}

func TestStream_MaxStreamsExceeded(t *testing.T) {
	t.Parallel()

	h, store := newTestEnv(t, nil)
	routes := h.Routes()

	sess := &session.Session{
		ID: "sess-max-streams",
		AudioTracks: []absclient.AudioTrack{
			{Index: 0, Duration: 10.0, ContentURL: "/track.mp3"},
		},
	}
	tok, err := store.Create(sess)
	if err != nil {
		t.Fatalf("store create: %v", err)
	}

	for range cap(h.streamsSem) {
		h.streamsSem <- struct{}{}
	}

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/stream/sess-max-streams.aac?token="+tok, http.NoBody)
	rec := httptest.NewRecorder()
	routes.ServeHTTP(rec, req)

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429 Too Many Requests, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "maximum active streams reached") {
		t.Errorf("expected max streams error message, got %s", rec.Body.String())
	}

	for range cap(h.streamsSem) {
		<-h.streamsSem
	}
}

func TestStream_Success(t *testing.T) {
	t.Parallel()

	h, store := newTestEnv(t, nil)
	routes := h.Routes()

	sess := &session.Session{
		ID: "sess-stream-ok",
		AudioTracks: []absclient.AudioTrack{
			{Index: 0, Duration: 10.0, ContentURL: "/stream-ok-track-1.mp3"},
			{Index: 1, Duration: 10.0, ContentURL: "/stream-ok-track-2.mp3"},
		},
		StartingTrackIndex: 0,
		SeekOffset:         0,
		CurrentTime:        0,
		Duration:           20.0,
		Speed:              1.0,
	}

	token, err := store.Create(sess)
	if err != nil {
		t.Fatalf("create session in store: %v", err)
	}

	reqHead := httptest.NewRequestWithContext(context.Background(), http.MethodHead, "/stream/sess-stream-ok.aac?token="+token, http.NoBody)
	recHead := httptest.NewRecorder()
	routes.ServeHTTP(recHead, reqHead)
	if recHead.Code != http.StatusOK {
		t.Errorf("HEAD stream request expected 200, got %d", recHead.Code)
	}

	reqGet := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/stream/sess-stream-ok.aac?token="+token, http.NoBody)
	recGet := httptest.NewRecorder()
	routes.ServeHTTP(recGet, reqGet)
	if recGet.Code != http.StatusOK {
		t.Errorf("GET stream request expected 200, got %d", recGet.Code)
	}
	if recGet.Header().Get("Content-Type") != "audio/aac" {
		t.Errorf("expected audio/aac Content-Type, got %s", recGet.Header().Get("Content-Type"))
	}

	reqUsed := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/stream/sess-stream-ok.aac?token="+token, http.NoBody)
	recUsed := httptest.NewRecorder()
	routes.ServeHTTP(recUsed, reqUsed)
	if recUsed.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for already used one-time stream token, got %d", recUsed.Code)
	}
}

func TestStream_SingleTrackAndSyncLoop(t *testing.T) {
	t.Parallel()

	syncHit := make(chan struct{}, 1)
	roundTrip := func(req *http.Request) (*http.Response, error) {
		if strings.Contains(req.URL.Path, "/sync") {
			select {
			case syncHit <- struct{}{}:
			default:
			}
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{}`)),
		}, nil
	}

	h, store := newTestEnv(t, roundTrip)
	h.syncInterval = 10 * time.Millisecond

	sess := &session.Session{
		ID: "sess-single-track",
		AudioTracks: []absclient.AudioTrack{
			{Index: 0, Duration: 10.0, ContentURL: "/single.mp3"},
		},
		StartingTrackIndex: 0,
		SeekOffset:         0,
		CurrentTime:        5.0,
		Duration:           10.0,
		Speed:              1.0,
	}

	token, err := store.Create(sess)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/stream/sess-single-track.aac?token="+token, http.NoBody)
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
}

func TestStream_WriteError(t *testing.T) {
	t.Parallel()

	h, store := newTestEnv(t, nil)

	sess := &session.Session{
		ID: "sess-stream-write-err",
		AudioTracks: []absclient.AudioTrack{
			{Index: 0, Duration: 10, ContentURL: "/write-err-track.mp3"},
		},
		Speed: 1.0,
	}

	token, err := store.Create(sess)
	if err != nil {
		t.Fatalf("store create: %v", err)
	}

	ew := &errResponseWriter{}
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/stream/sess-stream-write-err?token="+token, http.NoBody)
	req.SetPathValue("session_id", "sess-stream-write-err")

	h.HandleStream(ew, req)
}

func TestStream_DebugAndProbes(t *testing.T) {
	t.Parallel()

	h, store := newTestEnv(t, nil)
	h.cfg.Debug = true
	h.keepaliveInterval = 5 * time.Millisecond
	h.progressInterval = 5 * time.Millisecond

	reqInvalid := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/stream/sess-nonexistent.aac?token=bad-token", http.NoBody)
	reqInvalid.SetPathValue("session_id", "sess-nonexistent.aac")
	recInvalid := httptest.NewRecorder()
	h.HandleStream(recInvalid, reqInvalid)
	if recInvalid.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", recInvalid.Code)
	}

	sess := &session.Session{
		ID: "sess-probe-debug",
		AudioTracks: []absclient.AudioTrack{
			{Index: 0, Duration: 10, ContentURL: "/probe-debug-track.mp3"},
		},
		Speed: 1.0,
	}
	tok, err := store.Create(sess)
	if err != nil {
		t.Fatalf("create sess: %v", err)
	}

	reqHead := httptest.NewRequestWithContext(context.Background(), http.MethodHead, "/stream/sess-probe-debug.aac?token="+tok, http.NoBody)
	reqHead.SetPathValue("session_id", "sess-probe-debug.aac")
	recHead := httptest.NewRecorder()
	h.HandleStream(recHead, reqHead)
	if recHead.Code != http.StatusOK {
		t.Errorf("expected 200 for HEAD probe, got %d", recHead.Code)
	}

	sess2 := &session.Session{
		ID: "sess-full-debug",
		AudioTracks: []absclient.AudioTrack{
			{Index: 0, Duration: 10, ContentURL: "/full-debug-track.mp3"},
		},
		Speed: 1.0,
	}
	tok2, err := store.Create(sess2)
	if err != nil {
		t.Fatalf("create sess 2: %v", err)
	}

	reqGet := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/stream/sess-full-debug.aac?token="+tok2, http.NoBody)
	reqGet.SetPathValue("session_id", "sess-full-debug.aac")
	recGet := httptest.NewRecorder()
	h.HandleStream(recGet, reqGet)
	if recGet.Code != http.StatusOK {
		t.Errorf("expected 200 for GET stream, got %d", recGet.Code)
	}
}

func TestStream_ProbeZeroBytesSent(t *testing.T) {
	t.Parallel()

	h, store := newTestEnv(t, nil)
	h.cfg.Debug = true
	h.cfg.FFmpegPath = "true"

	sess := &session.Session{
		ID: "sess-probe-zero",
		AudioTracks: []absclient.AudioTrack{
			{Index: 0, Duration: 10, ContentURL: "/probe-zero-track.mp3"},
		},
		Speed: 1.0,
	}
	tok, err := store.Create(sess)
	if err != nil {
		t.Fatalf("create sess: %v", err)
	}

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/stream/sess-probe-zero.aac?token="+tok, http.NoBody)
	req.SetPathValue("session_id", "sess-probe-zero.aac")
	rec := httptest.NewRecorder()
	h.HandleStream(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 for probe zero, got %d", rec.Code)
	}
}

func TestDisconnectSync_Errors(t *testing.T) {
	t.Parallel()

	mockRoundTrip := func(_ *http.Request) (*http.Response, error) {
		return nil, errors.New("network failure on sync/close")
	}

	h, store := newTestEnv(t, mockRoundTrip)
	sess := &session.Session{
		ID:          "sess-disconnect-err",
		AudioTracks: []absclient.AudioTrack{{Index: 0, Duration: 10, ContentURL: "/disconnect-err-track.mp3"}},
		Speed:       1.0,
	}
	sess.BytesSent.Store(80000)
	tok, err := store.Create(sess)
	if err != nil {
		t.Fatalf("create sess: %v", err)
	}

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/stream/sess-disconnect-err.aac?token="+tok, http.NoBody)
	req.SetPathValue("session_id", "sess-disconnect-err.aac")
	rec := httptest.NewRecorder()
	h.HandleStream(rec, req)
}

func TestStream_ICYHeaders(t *testing.T) {
	t.Parallel()

	tests := []struct {
		sess      *session.Session
		name      string
		method    string
		wantName  string
		wantDesc  string
		wantLogo  string
		isPodcast bool
	}{
		{
			name:   "book with author narrator and title",
			method: http.MethodHead,
			sess: &session.Session{
				ID:       "sess-icy-full",
				Title:    "Book Title",
				Author:   "Author Name",
				Narrator: "Narrator One",
				CoverURL: "http://proxy.example.org/api/proxy/covers/book-1",
			},
			wantName: "Book Title • Author Name",
			wantDesc: "Narrator One",
			wantLogo: "http://proxy.example.org/api/proxy/covers/book-1",
		},
		{
			name:   "book with title and author without narrator",
			method: http.MethodHead,
			sess: &session.Session{
				ID:       "sess-icy-no-narrator",
				Title:    "Book Solo",
				Author:   "Author Solo",
				CoverURL: "http://proxy.example.org/api/proxy/covers/book-2",
			},
			wantName: "Book Solo",
			wantDesc: "Author Solo",
			wantLogo: "http://proxy.example.org/api/proxy/covers/book-2",
		},
		{
			name:   "book with title and narrator without author",
			method: http.MethodHead,
			sess: &session.Session{
				ID:       "sess-icy-no-author",
				Title:    "Book Solo 2",
				Narrator: "Narrator Solo",
				CoverURL: "http://proxy.example.org/api/proxy/covers/book-3",
			},
			wantName: "Book Solo 2",
			wantDesc: "Narrator Solo",
			wantLogo: "http://proxy.example.org/api/proxy/covers/book-3",
		},
		{
			name:   "book with title only",
			method: http.MethodHead,
			sess: &session.Session{
				ID:    "sess-icy-title-only",
				Title: "Only Title",
			},
			wantName: "Only Title",
		},
		{
			name:   "book with author only",
			method: http.MethodHead,
			sess: &session.Session{
				ID:     "sess-icy-author-only",
				Author: "Only Author",
			},
			wantName: "Only Author",
		},
		{
			name: "book with empty metadata defaults to Audiobook",
			sess: &session.Session{
				ID: "sess-icy-empty",
			},
			method:   http.MethodHead,
			wantName: "Audiobook",
		},
		{
			name:      "podcast with episode title and podcast title",
			method:    http.MethodHead,
			isPodcast: true,
			sess: &session.Session{
				ID:           "sess-icy-podcast-full",
				EpisodeTitle: "Episode 1",
				Title:        "Podcast Show",
				Author:       "Host Name",
				CoverURL:     "http://proxy.example.org/api/proxy/covers/pod-1",
			},
			wantName: "Episode 1",
			wantDesc: "Podcast Show",
			wantLogo: "http://proxy.example.org/api/proxy/covers/pod-1",
		},
		{
			name:      "podcast with episode title only falling back to author",
			method:    http.MethodHead,
			isPodcast: true,
			sess: &session.Session{
				ID:           "sess-icy-podcast-ep-only",
				EpisodeTitle: "Episode Solo",
				Author:       "Solo Host",
			},
			wantName: "Episode Solo",
			wantDesc: "Solo Host",
		},
		{
			name:      "podcast by episode id without titles",
			method:    http.MethodHead,
			isPodcast: true,
			sess: &session.Session{
				ID:        "sess-icy-pod-fallback",
				EpisodeID: "ep-999",
			},
			wantName: "Podcast Episode",
		},
		{
			name:   "crlf sanitization in title and author",
			method: http.MethodHead,
			sess: &session.Session{
				ID:       "sess-icy-crlf",
				Title:    "Book\r\nTitle\tLine",
				Author:   "Author\nName",
				Narrator: "Narrator\rName",
			},
			wantName: "Book Title Line • Author Name",
			wantDesc: "Narrator Name",
		},
		{
			name:   "relative cover url resolved via external base",
			method: http.MethodHead,
			sess: &session.Session{
				ID:     "sess-icy-cover-resolve",
				ItemID: "item-cov-1",
				Title:  "Cover Book",
			},
			wantName: "Cover Book",
			wantLogo: "http://proxy.example.org:8099/api/proxy/covers/item-cov-1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h, store := newTestEnv(t, nil)
			if tt.isPodcast {
				tt.sess.MediaType = "podcast"
			} else {
				tt.sess.MediaType = "book"
			}

			tok, err := store.Create(tt.sess)
			if err != nil {
				t.Fatalf("create sess: %v", err)
			}

			path := "/stream/" + tt.sess.ID + ".aac?token=" + tok
			req := httptest.NewRequestWithContext(context.Background(), tt.method, path, http.NoBody)
			req.Host = "example.org"
			req.SetPathValue("session_id", tt.sess.ID+".aac")
			rec := httptest.NewRecorder()

			h.HandleStream(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d", rec.Code)
			}

			wantGenre := "Audiobook"
			if tt.isPodcast {
				wantGenre = "Podcast"
			}
			if got := rec.Header().Get("icy-genre"); got != wantGenre {
				t.Errorf("icy-genre = %q, want %q", got, wantGenre)
			}
			if got := rec.Header().Get("icy-name"); got != tt.wantName {
				t.Errorf("icy-name = %q, want %q", got, tt.wantName)
			}
			if got := rec.Header().Get("icy-description"); got != tt.wantDesc {
				t.Errorf("icy-description = %q, want %q", got, tt.wantDesc)
			}
			if tt.wantLogo != "" {
				if got := rec.Header().Get("icy-logo"); got != tt.wantLogo {
					t.Errorf("icy-logo = %q, want %q", got, tt.wantLogo)
				}
				if got := rec.Header().Get("icy-url"); got != tt.wantLogo {
					t.Errorf("icy-url = %q, want %q", got, tt.wantLogo)
				}
			}
		})
	}
}

func TestExtractTitle(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		reqTitle string
		resp     *absclient.PlayResponse
		want     string
	}{
		{
			name:     "fallback to MediaMetadata title",
			reqTitle: "",
			resp: &absclient.PlayResponse{MediaMetadata: &struct {
				Title        string   `json:"title"`
				AuthorName   string   `json:"authorName"`
				Author       string   `json:"author"`
				NarratorName string   `json:"narratorName"`
				Narrators    []string `json:"narrators"`
			}{Title: "Meta Only"}},
			want: "Meta Only",
		},
		{
			name:     "empty when no metadata at all",
			reqTitle: "",
			resp:     &absclient.PlayResponse{},
			want:     "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := extractTitle(tt.reqTitle, tt.resp); got != tt.want {
				t.Errorf("extractTitle() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestExtractAuthor(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		reqAuthor string
		resp      *absclient.PlayResponse
		want      string
	}{
		{
			name:      "fallback to MediaMetadata Author field",
			reqAuthor: "",
			resp: &absclient.PlayResponse{MediaMetadata: &struct {
				Title        string   `json:"title"`
				AuthorName   string   `json:"authorName"`
				Author       string   `json:"author"`
				NarratorName string   `json:"narratorName"`
				Narrators    []string `json:"narrators"`
			}{Author: "Fallback Author"}},
			want: "Fallback Author",
		},
		{
			name:      "fallback to MediaMetadata AuthorName field",
			reqAuthor: "",
			resp: &absclient.PlayResponse{MediaMetadata: &struct {
				Title        string   `json:"title"`
				AuthorName   string   `json:"authorName"`
				Author       string   `json:"author"`
				NarratorName string   `json:"narratorName"`
				Narrators    []string `json:"narrators"`
			}{AuthorName: "Meta AuthorName"}},
			want: "Meta AuthorName",
		},
		{
			name:      "empty when no metadata at all",
			reqAuthor: "",
			resp:      &absclient.PlayResponse{},
			want:      "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := extractAuthor(tt.reqAuthor, tt.resp); got != tt.want {
				t.Errorf("extractAuthor() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestExtractNarrator(t *testing.T) {
	t.Parallel()

	tests := []struct {
		resp        *absclient.PlayResponse
		name        string
		reqNarrator string
		want        string
	}{
		{
			name:        "explicit request narrator takes precedence",
			reqNarrator: "Explicit Narrator",
			resp:        &absclient.PlayResponse{},
			want:        "Explicit Narrator",
		},
		{
			name:        "extract from MediaMetadata Narrators slice",
			reqNarrator: "",
			resp: &absclient.PlayResponse{
				MediaMetadata: &struct {
					Title        string   `json:"title"`
					AuthorName   string   `json:"authorName"`
					Author       string   `json:"author"`
					NarratorName string   `json:"narratorName"`
					Narrators    []string `json:"narrators"`
				}{
					Narrators: []string{"First Narrator", "Second Narrator"},
				},
			},
			want: "First Narrator, Second Narrator",
		},
		{
			name:        "fallback to MediaMetadata NarratorName string",
			reqNarrator: "",
			resp: &absclient.PlayResponse{
				MediaMetadata: &struct {
					Title        string   `json:"title"`
					AuthorName   string   `json:"authorName"`
					Author       string   `json:"author"`
					NarratorName string   `json:"narratorName"`
					Narrators    []string `json:"narrators"`
				}{
					NarratorName: "Fallback Single Narrator",
				},
			},
			want: "Fallback Single Narrator",
		},
		{
			name:        "fallback to LibraryItem Media Metadata NarratorName",
			reqNarrator: "",
			resp: &absclient.PlayResponse{
				LibraryItem: &struct {
					Media struct {
						Metadata struct {
							NarratorName string `json:"narratorName"`
						} `json:"metadata"`
					} `json:"media"`
				}{
					Media: struct {
						Metadata struct {
							NarratorName string `json:"narratorName"`
						} `json:"metadata"`
					}{
						Metadata: struct {
							NarratorName string `json:"narratorName"`
						}{
							NarratorName: "LibItem Narrator",
						},
					},
				},
			},
			want: "LibItem Narrator",
		},
		{
			name:        "empty when no narrator anywhere",
			reqNarrator: "",
			resp:        &absclient.PlayResponse{},
			want:        "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := extractNarrator(tt.reqNarrator, tt.resp); got != tt.want {
				t.Errorf("extractNarrator() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestLogStreamEnd(t *testing.T) {
	t.Parallel()

	h, _ := newTestEnv(t, nil)
	sess := &session.Session{
		ID: "sess-log-end",
	}
	sess.BytesSent.Store(12345)

	stderrBuf := bytes.NewBufferString("sample error line 1\r\nsample error line 2\n")
	h.logStreamEnd("sess-log-end", sess, 15*time.Second, "ffmpeg_read_error", stderrBuf)

	h.logStreamEnd("sess-log-end", sess, 15*time.Second, "eof", nil)

	emptyBuf := bytes.NewBuffer(nil)
	h.logStreamEnd("sess-log-end", sess, 15*time.Second, "eof", emptyBuf)
}

func TestLogStreamProgress_IntervalFallbackAndStop(t *testing.T) {
	t.Parallel()

	h, _ := newTestEnv(t, nil)
	h.progressInterval = 0

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	sess := &session.Session{
		ID:          "sess-prog-cancel",
		CurrentTime: 0,
		Duration:    100,
		Speed:       1.0,
	}

	stop := make(chan struct{})
	h.logStreamProgress(ctx, sess, "sess-prog-cancel", time.Now(), stop)

	ctx2 := t.Context()
	stop2 := make(chan struct{})
	close(stop2)
	h.logStreamProgress(ctx2, sess, "sess-prog-stop", time.Now(), stop2)
}

func TestLogStreamProgress_Ticker(t *testing.T) {
	t.Parallel()

	h, _ := newTestEnv(t, nil)
	h.progressInterval = 5 * time.Millisecond

	ctx := t.Context()
	sess := &session.Session{
		ID:          "sess-prog-tick",
		CurrentTime: 10,
		Duration:    100,
		Speed:       1.0,
	}

	stop := make(chan struct{})
	go func() {
		time.Sleep(20 * time.Millisecond)
		close(stop)
	}()

	h.logStreamProgress(ctx, sess, "sess-prog-tick", time.Now(), stop)
}

func TestRegisterTrackProxySession_NilProxy(t *testing.T) {
	t.Parallel()

	h := &Handler{}
	port, token, cleanup, err := h.registerTrackProxySession("sess-nil", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if port != 0 || token != "" {
		t.Errorf("expected 0 port and empty token, got %d, %q", port, token)
	}
	cleanup()
}

func TestDefaultTrackProxyRegisterSession_Error(t *testing.T) {
	cleanup := trackproxy.SetGenerateToken(func() (string, error) {
		return "", errors.New("token generator error")
	})
	defer cleanup()

	tp := trackproxy.New("http://test.example.net", "token", "1.0", false)
	if _, err := defaultTrackProxyRegisterSession(tp, "sess-err", []string{"/unique-err-track.mp3"}); err == nil {
		t.Error("expected error when token generation fails")
	}
}

func TestHandleStream_RegisterTrackProxyError(t *testing.T) {
	h, store := newTestEnv(t, nil)
	token, err := store.Create(&session.Session{
		ID: "sess-reg-fail",
	})
	if err != nil {
		t.Fatalf("create session error: %v", err)
	}

	trackProxyRegisterSessionMu.Lock()
	origRegister := trackProxyRegisterSession
	trackProxyRegisterSession = func(_ *trackproxy.Server, _ string, _ []string) (string, error) {
		return "", errors.New("registration failed")
	}
	trackProxyRegisterSessionMu.Unlock()
	defer func() {
		trackProxyRegisterSessionMu.Lock()
		trackProxyRegisterSession = origRegister
		trackProxyRegisterSessionMu.Unlock()
	}()

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/stream/sess-reg-fail.mp3?token="+token, http.NoBody)
	req.SetPathValue("session_id", "sess-reg-fail.mp3")
	rec := httptest.NewRecorder()

	h.HandleStream(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500 Internal Server Error, got %d", rec.Code)
	}
}
