package handler

import (
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
