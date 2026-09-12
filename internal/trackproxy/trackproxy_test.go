package trackproxy

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/underhax/audiobookshelf-transcoder-proxy/internal/session"
)

type roundTripFunc func(req *http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type errAfterReader struct {
	err    error
	data   []byte
	offset int
}

type emptyNoErrReader struct{}

func (r *emptyNoErrReader) Read(_ []byte) (int, error) {
	return 0, nil
}

func (r *errAfterReader) Read(p []byte) (int, error) {
	if r.offset < len(r.data) {
		n := copy(p, r.data[r.offset:])
		r.offset += n
		return n, nil
	}
	return 0, r.err
}

func (r *errAfterReader) Close() error {
	return nil
}

type blockingReader struct {
	block chan struct{}
}

func (b *blockingReader) Read(_ []byte) (int, error) {
	<-b.block
	return 0, io.EOF
}

func TestServer_Lifecycle(t *testing.T) {
	s := New("http://example.com", "secret-token", "1.0.0", false)
	port, err := s.Start()
	if err != nil {
		t.Fatalf("Start() returned error: %v", err)
	}
	if port <= 0 {
		t.Fatalf("expected valid port > 0, got %d", port)
	}
	if s.Port() != port {
		t.Fatalf("expected Port() %d, got %d", port, s.Port())
	}

	sessionID := "sess-lifecycle"
	tracks := []string{"/audio/track1.mp3", "/audio/track2.mp3"}
	token, err := s.RegisterSession(sessionID, tracks)
	if err != nil {
		t.Fatalf("RegisterSession() error: %v", err)
	}
	if len(token) != 64 {
		t.Fatalf("expected 64 hex characters for token, got %d", len(token))
	}

	val, ok := s.sessions.Load(sessionID)
	if !ok {
		t.Fatal("expected session to be registered")
	}
	loadedState, ok := val.(*sessionState)
	if !ok || len(loadedState.trackURLs) != 2 || loadedState.token != token {
		t.Fatalf("unexpected loaded state: %+v", val)
	}

	s.UnregisterSession(sessionID)
	if _, ok := s.sessions.Load(sessionID); ok {
		t.Fatal("expected session to be unregistered")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown() returned error: %v", err)
	}

	var nilServer *Server
	if err := nilServer.Shutdown(ctx); err != nil {
		t.Fatalf("nil Server Shutdown() returned error: %v", err)
	}
}

func TestServer_UserAgent_Whitelist(t *testing.T) {
	tests := []struct {
		name           string
		userAgent      string
		expectedStatus int
	}{
		{
			name:           "curl user agent forbidden",
			userAgent:      "curl/7.88.1",
			expectedStatus: http.StatusForbidden,
		},
		{
			name:           "empty user agent forbidden",
			userAgent:      "",
			expectedStatus: http.StatusForbidden,
		},
		{
			name:           "python requests user agent forbidden",
			userAgent:      "python-requests/2.31.0",
			expectedStatus: http.StatusForbidden,
		},
		{
			name:           "valid abstp user agent allowed past whitelist",
			userAgent:      "abstp/1.0.0",
			expectedStatus: http.StatusNotFound,
		},
		{
			name:           "valid lavf user agent allowed past whitelist",
			userAgent:      "Lavf/60.16.100",
			expectedStatus: http.StatusNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := New("http://example.com", "token", "1.0.0", false)
			req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/track/sess/0", http.NoBody)
			req.Header.Set("User-Agent", tt.userAgent)
			req.SetPathValue("session_id", "sess")
			req.SetPathValue("track_index", "0")
			rec := httptest.NewRecorder()
			s.handleTrack(rec, req)

			if rec.Code != tt.expectedStatus {
				t.Fatalf("expected status %d, got %d", tt.expectedStatus, rec.Code)
			}
		})
	}
}

func TestServer_Token_Validation(t *testing.T) {
	s := New("http://example.com", "upstream-token", "1.0.0", false)
	sessionID := "sess-tok-val"
	validToken, err := s.RegisterSession(sessionID, []string{"/audio-token-val.mp3"})
	if err != nil {
		t.Fatalf("register session: %v", err)
	}

	s.httpClient = &http.Client{
		Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode:    http.StatusOK,
				Header:        make(http.Header),
				Body:          io.NopCloser(strings.NewReader("valid-token-data")),
				ContentLength: -1,
			}, nil
		}),
	}

	tests := []struct {
		name           string
		tokenParam     string
		expectedStatus int
	}{
		{
			name:           "missing token query param",
			tokenParam:     "",
			expectedStatus: http.StatusUnauthorized,
		},
		{
			name:           "invalid token query param",
			tokenParam:     "wrong-secret-token",
			expectedStatus: http.StatusUnauthorized,
		},
		{
			name:           "valid token query param",
			tokenParam:     validToken,
			expectedStatus: http.StatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			url := "/track/" + sessionID + "/0"
			if tt.tokenParam != "" {
				url += "?token=" + tt.tokenParam
			}
			req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, url, http.NoBody)
			req.Header.Set("User-Agent", "abstp/1.0.0")
			req.SetPathValue("session_id", sessionID)
			req.SetPathValue("track_index", "0")
			rec := httptest.NewRecorder()
			s.handleTrack(rec, req)

			if rec.Code != tt.expectedStatus {
				t.Fatalf("expected status %d, got %d", tt.expectedStatus, rec.Code)
			}
		})
	}
}

func TestServer_HandleTrack_Validation(t *testing.T) {
	tests := []struct {
		setup          func(s *Server) (sessionID, token string)
		name           string
		trackIdx       string
		expectedStatus int
	}{
		{
			name: "session not found",
			setup: func(_ *Server) (string, string) {
				return "missing-session", "some-token"
			},
			trackIdx:       "0",
			expectedStatus: http.StatusNotFound,
		},
		{
			name: "invalid session state type",
			setup: func(s *Server) (string, string) {
				s.sessions.Store("bad-state", 12345)
				return "bad-state", "some-token"
			},
			trackIdx:       "0",
			expectedStatus: http.StatusInternalServerError,
		},
		{
			name: "non-integer track index",
			setup: func(s *Server) (string, string) {
				tok, err := s.RegisterSession("sess-str-idx", []string{"/track-str.mp3"})
				if err != nil {
					panic(err)
				}
				return "sess-str-idx", tok
			},
			trackIdx:       "abc",
			expectedStatus: http.StatusBadRequest,
		},
		{
			name: "negative track index",
			setup: func(s *Server) (string, string) {
				tok, err := s.RegisterSession("sess-neg-idx", []string{"/track-neg.mp3"})
				if err != nil {
					panic(err)
				}
				return "sess-neg-idx", tok
			},
			trackIdx:       "-1",
			expectedStatus: http.StatusBadRequest,
		},
		{
			name: "out of bounds track index",
			setup: func(s *Server) (string, string) {
				tok, err := s.RegisterSession("sess-oob-idx", []string{"/track-oob.mp3"})
				if err != nil {
					panic(err)
				}
				return "sess-oob-idx", tok
			},
			trackIdx:       "5",
			expectedStatus: http.StatusBadRequest,
		},
		{
			name: "invalid upstream URL",
			setup: func(s *Server) (string, string) {
				s.absURL = "ftp://invalid-scheme.example.com"
				tok, err := s.RegisterSession("sess-bad-url", []string{"/track-bad.mp3"})
				if err != nil {
					panic(err)
				}
				return "sess-bad-url", tok
			},
			trackIdx:       "0",
			expectedStatus: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := New("http://example.com", "token", "1.0.0", false)
			sessionID, token := tt.setup(s)

			req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/track/"+sessionID+"/"+tt.trackIdx+"?token="+token, http.NoBody)
			req.Header.Set("User-Agent", "abstp/1.0.0")
			req.SetPathValue("session_id", sessionID)
			req.SetPathValue("track_index", tt.trackIdx)

			rec := httptest.NewRecorder()
			s.handleTrack(rec, req)

			if rec.Code != tt.expectedStatus {
				t.Fatalf("expected status %d, got %d", tt.expectedStatus, rec.Code)
			}
		})
	}
}

func TestServer_Idle_Timeout(t *testing.T) {
	s := New("http://example.com", "token", "1.0.0", false)
	sessionID := "sess-idle"
	token, err := s.RegisterSession(sessionID, []string{"/audio-idle-timeout.mp3"})
	if err != nil {
		t.Fatalf("register: %v", err)
	}

	val, ok := s.sessions.Load(sessionID)
	if !ok {
		t.Fatal("expected session")
	}
	state, ok := val.(*sessionState)
	if !ok {
		t.Fatal("expected *sessionState")
	}
	state.lastActivity.Store(time.Now().Add(-50 * time.Second).UnixNano())

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/track/"+sessionID+"/0?token="+token, http.NoBody)
	req.Header.Set("User-Agent", "abstp/1.0.0")
	req.SetPathValue("session_id", sessionID)
	req.SetPathValue("track_index", "0")
	rec := httptest.NewRecorder()
	s.handleTrack(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected status 401 for expired session, got %d", rec.Code)
	}

	if _, ok := s.sessions.Load(sessionID); ok {
		t.Fatal("expected expired session to be deleted")
	}
}

func TestServer_Concurrency_Parallel(t *testing.T) {
	s := New("http://example.com", "token", "1.0.0", false)
	holdFirst := make(chan struct{})
	firstStarted := make(chan struct{})
	var calls atomic.Int32
	s.httpClient = &http.Client{
		Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
			if calls.Add(1) == 1 {
				close(firstStarted)
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     make(http.Header),
					Body:       io.NopCloser(&blockingReader{block: holdFirst}),
				}, nil
			}
			return &http.Response{
				StatusCode:    http.StatusOK,
				Header:        make(http.Header),
				Body:          io.NopCloser(strings.NewReader("second-response-body")),
				ContentLength: int64(len("second-response-body")),
			}, nil
		}),
	}

	sessionID := "sess-parallel"
	token, err := s.RegisterSession(sessionID, []string{"/parallel-stream.mp3"})
	if err != nil {
		t.Fatalf("register: %v", err)
	}

	firstDone := make(chan struct{})
	go func() {
		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/track/"+sessionID+"/0?token="+token, http.NoBody)
		req.Header.Set("User-Agent", "abstp/1.0.0")
		req.SetPathValue("session_id", sessionID)
		req.SetPathValue("track_index", "0")
		rec := httptest.NewRecorder()
		s.handleTrack(rec, req)
		if rec.Code != http.StatusOK {
			t.Errorf("expected first concurrent stream to stay alive, got %d", rec.Code)
		}
		close(firstDone)
	}()

	<-firstStarted

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/track/"+sessionID+"/0?token="+token, http.NoBody)
	req.Header.Set("User-Agent", "abstp/1.0.0")
	req.SetPathValue("session_id", sessionID)
	req.SetPathValue("track_index", "0")
	rec := httptest.NewRecorder()
	s.handleTrack(rec, req)

	select {
	case <-firstDone:
		t.Fatal("first stream was canceled by a concurrent request")
	default:
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200 for concurrent request, got %d", rec.Code)
	}
	if rec.Body.String() != "second-response-body" {
		t.Fatalf("expected second response body, got %q", rec.Body.String())
	}

	close(holdFirst)
	select {
	case <-firstDone:
	case <-time.After(2 * time.Second):
		t.Fatal("first stream did not complete after unblock")
	}
}

func TestServer_Sequential_Requests(t *testing.T) {
	s := New("http://example.com", "token", "1.0.0", false)
	s.httpClient = &http.Client{
		Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode:    http.StatusOK,
				Header:        make(http.Header),
				Body:          io.NopCloser(strings.NewReader("track-chunk-data")),
				ContentLength: -1,
			}, nil
		}),
	}

	sessionID := "sess-seq"
	token, err := s.RegisterSession(sessionID, []string{"/t1.mp3", "/t2.mp3"})
	if err != nil {
		t.Fatalf("register: %v", err)
	}

	req1 := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/track/"+sessionID+"/0?token="+token, http.NoBody)
	req1.Header.Set("User-Agent", "abstp/1.0.0")
	req1.SetPathValue("session_id", sessionID)
	req1.SetPathValue("track_index", "0")
	rec1 := httptest.NewRecorder()
	s.handleTrack(rec1, req1)
	if rec1.Code != http.StatusOK {
		t.Fatalf("expected 200 for track 0, got %d", rec1.Code)
	}

	req2 := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/track/"+sessionID+"/1?token="+token, http.NoBody)
	req2.Header.Set("User-Agent", "abstp/1.0.0")
	req2.SetPathValue("session_id", sessionID)
	req2.SetPathValue("track_index", "1")
	rec2 := httptest.NewRecorder()
	s.handleTrack(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("expected 200 for track 1, got %d", rec2.Code)
	}
}

func TestServer_Proxy_NormalStream(t *testing.T) {
	expectedPayload := "audio-sample-data-payload"
	token := "valid-bearer-token"
	version := "2.1.0"

	s := New("http://example.com", token, version, true)
	s.httpClient = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if auth := req.Header.Get("Authorization"); auth != "Bearer "+token {
				t.Errorf("expected Authorization Bearer %s, got %s", token, auth)
			}
			if ua := req.Header.Get("User-Agent"); ua != "abstp/"+version {
				t.Errorf("expected User-Agent abstp/%s, got %s", version, ua)
			}
			resp := &http.Response{
				StatusCode:    http.StatusOK,
				Header:        make(http.Header),
				Body:          io.NopCloser(strings.NewReader(expectedPayload)),
				ContentLength: -1,
			}
			resp.Header.Set("Content-Type", "audio/mpeg")
			resp.Header.Set("Accept-Ranges", "bytes")
			return resp, nil
		}),
	}

	sessionID := "sess-stream"
	proxyToken, err := s.RegisterSession(sessionID, []string{"/audio-normal-stream.mp3"})
	if err != nil {
		t.Fatalf("register session: %v", err)
	}

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/track/"+sessionID+"/0?token="+proxyToken, http.NoBody)
	req.Header.Set("User-Agent", "abstp/1.0.0")
	req.SetPathValue("session_id", sessionID)
	req.SetPathValue("track_index", "0")

	rec := httptest.NewRecorder()
	s.handleTrack(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "audio/mpeg" {
		t.Fatalf("expected Content-Type audio/mpeg, got %s", ct)
	}
	if rec.Body.String() != expectedPayload {
		t.Fatalf("expected payload %q, got %q", expectedPayload, rec.Body.String())
	}
}

func TestServer_Proxy_RangeForwarding(t *testing.T) {
	s := New("http://example.com", "token", "", false)
	s.httpClient = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			rangeHdr := req.Header.Get("Range")
			if rangeHdr != "bytes=10-20" {
				t.Errorf("expected Range bytes=10-20, got %s", rangeHdr)
			}
			resp := &http.Response{
				StatusCode:    http.StatusPartialContent,
				Header:        make(http.Header),
				Body:          io.NopCloser(strings.NewReader("partialdata")),
				ContentLength: -1,
			}
			resp.Header.Set("Content-Range", "bytes 10-20/100")
			return resp, nil
		}),
	}

	sessionID := "sess-range"
	token, err := s.RegisterSession(sessionID, []string{"/audio-range-forward.mp3"})
	if err != nil {
		t.Fatalf("register: %v", err)
	}

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/track/"+sessionID+"/0?token="+token, http.NoBody)
	req.Header.Set("User-Agent", "abstp/1.0.0")
	req.SetPathValue("session_id", sessionID)
	req.SetPathValue("track_index", "0")
	req.Header.Set("Range", "bytes=10-20")

	rec := httptest.NewRecorder()
	s.handleTrack(rec, req)

	if rec.Code != http.StatusPartialContent {
		t.Fatalf("expected status 206, got %d", rec.Code)
	}
	if rec.Body.String() != "partialdata" {
		t.Fatalf("unexpected body: %q", rec.Body.String())
	}
}

func TestServer_Proxy_ResumeAfterUpstreamDrop(t *testing.T) {
	tests := []struct {
		initialRange    string
		name            string
		firstPart       string
		secondPart      string
		wantResumeRange string
	}{
		{
			name:            "bounded range resume",
			initialRange:    "bytes=10-20",
			firstPart:       "abcde",
			secondPart:      "fghij",
			wantResumeRange: "bytes=15-20",
		},
		{
			name:            "open ended stream resume",
			firstPart:       "first-chunk-of-data-",
			secondPart:      "second-chunk-resumed",
			wantResumeRange: "bytes=20-",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var requestCount atomic.Int32

			s := New("http://example.com", "token", "test", true)
			s.spoolBytes = 2
			s.retryDelays = []time.Duration{time.Millisecond, 2 * time.Millisecond}
			s.httpClient = &http.Client{
				Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
					reqNum := requestCount.Add(1)
					if reqNum == 1 {
						if tt.initialRange != "" && req.Header.Get("Range") != tt.initialRange {
							t.Errorf("expected Range %q on attempt 1, got %q", tt.initialRange, req.Header.Get("Range"))
						}
						resp := &http.Response{
							StatusCode: http.StatusOK,
							Header:     make(http.Header),
							Body: &errAfterReader{
								data: []byte(tt.firstPart),
								err:  errors.New("tcp drop"),
							},
						}
						resp.Header.Set("Content-Type", "audio/mpeg")
						return resp, nil
					}

					if req.Header.Get("Range") != tt.wantResumeRange {
						t.Errorf("expected Range %q on resume, got %q", tt.wantResumeRange, req.Header.Get("Range"))
					}

					resp := &http.Response{
						StatusCode:    http.StatusPartialContent,
						Header:        make(http.Header),
						Body:          io.NopCloser(strings.NewReader(tt.secondPart)),
						ContentLength: -1,
					}
					resp.Header.Set("Content-Type", "audio/mpeg")
					return resp, nil
				}),
			}

			sessionID := "sess-resume-drop"
			token, err := s.RegisterSession(sessionID, []string{"/track.mp3"})
			if err != nil {
				t.Fatalf("register: %v", err)
			}

			req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/track/"+sessionID+"/0?token="+token, http.NoBody)
			req.Header.Set("User-Agent", "abstp/1.0.0")
			req.SetPathValue("session_id", sessionID)
			req.SetPathValue("track_index", "0")
			if tt.initialRange != "" {
				req.Header.Set("Range", tt.initialRange)
			}

			rec := httptest.NewRecorder()
			s.handleTrack(rec, req)

			if rec.Body.String() != tt.firstPart+tt.secondPart {
				t.Fatalf("expected combined %q, got %q", tt.firstPart+tt.secondPart, rec.Body.String())
			}
			if requestCount.Load() != 2 {
				t.Fatalf("expected exactly 2 upstream requests, got %d", requestCount.Load())
			}
		})
	}
}

func TestServer_Proxy_Strict206ValidationOnResume(t *testing.T) {
	part1 := "initial-chunk-"
	part2 := "resumed-chunk"
	var attempts atomic.Int32

	s := New("http://example.com", "token", "1.0.0", true)
	s.spoolBytes = 4
	s.retryDelays = []time.Duration{time.Millisecond, 2 * time.Millisecond}
	s.httpClient = &http.Client{
		Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
			att := attempts.Add(1)
			if att == 1 {
				resp := &http.Response{
					StatusCode: http.StatusOK,
					Header:     make(http.Header),
					Body: &errAfterReader{
						data: []byte(part1),
						err:  errors.New("connection drop 1"),
					},
				}
				resp.Header.Set("Content-Type", "audio/mpeg")
				return resp, nil
			}
			if att == 2 {
				resp := &http.Response{
					StatusCode: http.StatusOK,
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader("bad-replay")),
				}
				return resp, nil
			}
			resp := &http.Response{
				StatusCode:    http.StatusPartialContent,
				Header:        make(http.Header),
				Body:          io.NopCloser(strings.NewReader(part2)),
				ContentLength: -1,
			}
			resp.Header.Set("Content-Type", "audio/mpeg")
			return resp, nil
		}),
	}

	sessionID := "sess-strict-206"
	token, err := s.RegisterSession(sessionID, []string{"/audio-strict-206.mp3"})
	if err != nil {
		t.Fatalf("register: %v", err)
	}

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/track/"+sessionID+"/0?token="+token, http.NoBody)
	req.Header.Set("User-Agent", "abstp/1.0.0")
	req.SetPathValue("session_id", sessionID)
	req.SetPathValue("track_index", "0")
	rec := httptest.NewRecorder()
	s.handleTrack(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}
	expected := part1 + part2
	if rec.Body.String() != expected {
		t.Fatalf("expected body %q, got %q", expected, rec.Body.String())
	}
	if attempts.Load() != 3 {
		t.Fatalf("expected 3 attempts (drop -> 200 rejection -> 206 success), got %d", attempts.Load())
	}
}

func TestServer_Proxy_ContentLength_Forwarding(t *testing.T) {
	tests := []struct {
		name          string
		headerCL      string
		expectedCL    string
		contentLength int64
	}{
		{
			name:          "content length field present",
			contentLength: 18,
			headerCL:      "",
			expectedCL:    "18",
		},
		{
			name:          "content length header fallback",
			contentLength: -1,
			headerCL:      "25",
			expectedCL:    "25",
		},
		{
			name:          "no content length",
			contentLength: -1,
			headerCL:      "",
			expectedCL:    "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := New("http://example.com", "token", "1.0.0", false)
			s.httpClient = &http.Client{
				Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
					resp := &http.Response{
						StatusCode:    http.StatusOK,
						Header:        make(http.Header),
						Body:          io.NopCloser(strings.NewReader("audio-payload-data")),
						ContentLength: tt.contentLength,
					}
					resp.Header.Set("Content-Type", "audio/mpeg")
					if tt.headerCL != "" {
						resp.Header.Set("Content-Length", tt.headerCL)
					}
					return resp, nil
				}),
			}

			sessionID := "sess-cl"
			token, err := s.RegisterSession(sessionID, []string{"/audio-cl.mp3"})
			if err != nil {
				t.Fatalf("register: %v", err)
			}

			req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/track/"+sessionID+"/0?token="+token, http.NoBody)
			req.Header.Set("User-Agent", "abstp/1.0.0")
			req.SetPathValue("session_id", sessionID)
			req.SetPathValue("track_index", "0")
			rec := httptest.NewRecorder()
			s.handleTrack(rec, req)

			if cl := rec.Header().Get("Content-Length"); cl != tt.expectedCL {
				t.Fatalf("expected Content-Length %q, got %q", tt.expectedCL, cl)
			}
		})
	}
}

func TestServer_Proxy_RetryExhaustion(t *testing.T) {
	var attempts atomic.Int32
	s := New("http://example.com", "token", "test", false)
	s.retryDelays = []time.Duration{time.Millisecond, time.Millisecond}
	s.httpClient = &http.Client{
		Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
			attempts.Add(1)
			return &http.Response{
				StatusCode: http.StatusInternalServerError,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader("internal server error")),
			}, nil
		}),
	}

	sessionID := "sess-exhaust"
	token, err := s.RegisterSession(sessionID, []string{"/error.mp3"})
	if err != nil {
		t.Fatalf("register: %v", err)
	}

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/track/"+sessionID+"/0?token="+token, http.NoBody)
	req.Header.Set("User-Agent", "abstp/1.0.0")
	req.SetPathValue("session_id", sessionID)
	req.SetPathValue("track_index", "0")

	rec := httptest.NewRecorder()
	s.handleTrack(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("expected 502 Bad Gateway on exhaustion, got %d", rec.Code)
	}
	if int(attempts.Load()) != len(s.retryDelays)+1 {
		t.Fatalf("expected %d attempts, got %d", len(s.retryDelays)+1, attempts.Load())
	}
}

func TestServer_Proxy_UpstreamNetworkErrorRetry(t *testing.T) {
	var attempts atomic.Int32
	s := New("http://example.com", "token", "test", false)
	s.retryDelays = []time.Duration{time.Millisecond}
	s.httpClient = &http.Client{
		Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
			att := attempts.Add(1)
			if att == 1 {
				return nil, errors.New("temporary dial error")
			}
			return &http.Response{
				StatusCode:    http.StatusOK,
				Header:        make(http.Header),
				Body:          io.NopCloser(strings.NewReader("success-after-retry")),
				ContentLength: -1,
			}, nil
		}),
	}

	sessionID := "sess-neterr"
	token, err := s.RegisterSession(sessionID, []string{"/retry.mp3"})
	if err != nil {
		t.Fatalf("register: %v", err)
	}

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/track/"+sessionID+"/0?token="+token, http.NoBody)
	req.Header.Set("User-Agent", "abstp/1.0.0")
	req.SetPathValue("session_id", sessionID)
	req.SetPathValue("track_index", "0")

	rec := httptest.NewRecorder()
	s.handleTrack(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK after retry, got %d", rec.Code)
	}
	if rec.Body.String() != "success-after-retry" {
		t.Fatalf("unexpected body: %q", rec.Body.String())
	}
}

func TestServer_Proxy_ContextCancelled(_ *testing.T) {
	s := New("http://example.com", "token", "test", false)
	s.retryDelays = []time.Duration{200 * time.Millisecond}
	s.httpClient = &http.Client{
		Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
			return nil, errors.New("upstream dial failure")
		}),
	}

	sessionID := "sess-cancel"
	token, err := s.RegisterSession(sessionID, []string{"/cancel.mp3"})
	if err != nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	req := httptest.NewRequestWithContext(ctx, http.MethodGet, "/track/"+sessionID+"/0?token="+token, http.NoBody)
	req.Header.Set("User-Agent", "abstp/1.0.0")
	req.SetPathValue("session_id", sessionID)
	req.SetPathValue("track_index", "0")

	rec := httptest.NewRecorder()
	s.handleTrack(rec, req)
}

func TestReadSpool(t *testing.T) {
	tests := []struct {
		reader    io.Reader
		name      string
		spoolSize int
		wantLen   int
		wantEOF   bool
		wantErr   bool
	}{
		{
			name:      "eof before spool filled",
			reader:    strings.NewReader("sp"),
			spoolSize: 8,
			wantLen:   2,
			wantEOF:   true,
		},
		{
			name:      "spool buffer filled",
			reader:    strings.NewReader("spool-data-full"),
			spoolSize: 8,
			wantLen:   8,
			wantEOF:   false,
		},
		{
			name:      "empty reader",
			reader:    strings.NewReader(""),
			spoolSize: 8,
			wantLen:   0,
			wantEOF:   true,
		},
		{
			name:      "empty read without error",
			reader:    &emptyNoErrReader{},
			spoolSize: 8,
			wantErr:   true,
		},
		{
			name:      "read error mid spool",
			reader:    &errAfterReader{data: []byte("ab"), err: errors.New("spool drop")},
			spoolSize: 8,
			wantErr:   true,
		},
		{
			name:      "invalid spool size",
			reader:    strings.NewReader("x"),
			spoolSize: 0,
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, eof, err := readSpool(tt.reader, tt.spoolSize)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error from readSpool")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected readSpool error: %v", err)
			}
			if len(got) != tt.wantLen || eof != tt.wantEOF {
				t.Fatalf("readSpool = len %d, eof %v; want len %d, eof %v", len(got), eof, tt.wantLen, tt.wantEOF)
			}
		})
	}
}

func TestServer_Proxy_EarlySpoolRetry(t *testing.T) {
	tests := []struct {
		firstBody io.ReadCloser
		name      string
		wantBody  string
		firstCL   int64
	}{
		{
			name:      "upstream error before spool fills",
			firstBody: &errAfterReader{data: []byte("partial-prefix"), err: errors.New("early upstream drop")},
			wantBody:  "complete-payload-here",
		},
		{
			name:      "upstream eof before declared length",
			firstCL:   100,
			firstBody: io.NopCloser(strings.NewReader("short-payload")),
			wantBody:  "final-payload-complete",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var requestCount atomic.Int32
			s := New("http://example.com", "token", "test", true)
			s.retryDelays = []time.Duration{time.Millisecond}
			s.httpClient = &http.Client{
				Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
					if requestCount.Add(1) == 1 {
						resp := &http.Response{
							StatusCode:    http.StatusOK,
							Header:        make(http.Header),
							Body:          tt.firstBody,
							ContentLength: tt.firstCL,
						}
						resp.Header.Set("Content-Type", "audio/mpeg")
						return resp, nil
					}
					return &http.Response{
						StatusCode:    http.StatusOK,
						Header:        make(http.Header),
						Body:          io.NopCloser(strings.NewReader(tt.wantBody)),
						ContentLength: -1,
					}, nil
				}),
			}

			sessionID := "sess-early-spool"
			token, err := s.RegisterSession(sessionID, []string{"/early-spool.mp3"})
			if err != nil {
				t.Fatalf("register session: %v", err)
			}

			req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/track/"+sessionID+"/0?token="+token, http.NoBody)
			req.Header.Set("User-Agent", "abstp/1.0.0")
			req.SetPathValue("session_id", sessionID)
			req.SetPathValue("track_index", "0")

			rec := httptest.NewRecorder()
			s.handleTrack(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("expected status 200, got %d", rec.Code)
			}
			if rec.Body.String() != tt.wantBody {
				t.Fatalf("expected body %q without partial spill, got %q", tt.wantBody, rec.Body.String())
			}
			if requestCount.Load() != 2 {
				t.Fatalf("expected 2 upstream requests after early spool failure, got %d", requestCount.Load())
			}
		})
	}
}

func TestServer_Proxy_MidStreamEOFDropRetry(t *testing.T) {
	var requestCount atomic.Int32
	s := New("http://example.com", "token", "test", true)
	s.spoolBytes = 2
	s.retryDelays = []time.Duration{time.Millisecond}
	s.httpClient = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if requestCount.Add(1) == 1 {
				if rangeHdr := req.Header.Get("Range"); rangeHdr != "" {
					t.Errorf("expected no Range on first attempt, got %q", rangeHdr)
				}
				resp := &http.Response{
					StatusCode:    http.StatusOK,
					Header:        make(http.Header),
					Body:          io.NopCloser(strings.NewReader("abcde")),
					ContentLength: 10,
				}
				resp.Header.Set("Content-Type", "audio/mpeg")
				return resp, nil
			}
			if rangeHdr := req.Header.Get("Range"); rangeHdr != "bytes=5-" {
				t.Errorf("expected Range bytes=5- on resume, got %q", rangeHdr)
			}
			resp := &http.Response{
				StatusCode:    http.StatusPartialContent,
				Header:        make(http.Header),
				Body:          io.NopCloser(strings.NewReader("fghij")),
				ContentLength: 5,
			}
			resp.Header.Set("Content-Type", "audio/mpeg")
			return resp, nil
		}),
	}

	sessionID := "sess-mid-eof"
	token, err := s.RegisterSession(sessionID, []string{"/mid-eof.mp3"})
	if err != nil {
		t.Fatalf("register session: %v", err)
	}

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/track/"+sessionID+"/0?token="+token, http.NoBody)
	req.Header.Set("User-Agent", "abstp/1.0.0")
	req.SetPathValue("session_id", sessionID)
	req.SetPathValue("track_index", "0")

	rec := httptest.NewRecorder()
	s.handleTrack(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}
	if rec.Body.String() != "abcdefghij" {
		t.Fatalf("expected valid combined body, got %q", rec.Body.String())
	}
	if requestCount.Load() != 2 {
		t.Fatalf("expected 2 upstream requests after mid-stream EOF drop, got %d", requestCount.Load())
	}
}

func TestServer_Proxy_SpoolThenStreamComplete(t *testing.T) {
	var requestCount atomic.Int32
	s := New("http://example.com", "token", "test", true)
	s.spoolBytes = 2
	s.httpClient = &http.Client{
		Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
			requestCount.Add(1)
			resp := &http.Response{
				StatusCode:    http.StatusOK,
				Header:        make(http.Header),
				Body:          io.NopCloser(strings.NewReader("abcde")),
				ContentLength: 5,
			}
			resp.Header.Set("Content-Type", "audio/mpeg")
			return resp, nil
		}),
	}

	sessionID := "sess-spool-stream"
	token, err := s.RegisterSession(sessionID, []string{"/spool-stream.mp3"})
	if err != nil {
		t.Fatalf("register session: %v", err)
	}

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/track/"+sessionID+"/0?token="+token, http.NoBody)
	req.Header.Set("User-Agent", "abstp/1.0.0")
	req.SetPathValue("session_id", sessionID)
	req.SetPathValue("track_index", "0")

	rec := httptest.NewRecorder()
	s.handleTrack(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}
	if rec.Body.String() != "abcde" {
		t.Fatalf("expected full body without spurious retry, got %q", rec.Body.String())
	}
	if requestCount.Load() != 1 {
		t.Fatalf("expected 1 upstream request on complete spool+stream, got %d", requestCount.Load())
	}
}

func TestServer_Proxy_RetryExhaustAfterCommit(t *testing.T) {
	var requestCount atomic.Int32
	s := New("http://example.com", "token", "test", false)
	s.spoolBytes = 2
	s.retryDelays = []time.Duration{time.Millisecond, time.Millisecond}
	s.httpClient = &http.Client{
		Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
			status := http.StatusOK
			if requestCount.Load() > 0 {
				status = http.StatusPartialContent
			}
			requestCount.Add(1)
			resp := &http.Response{
				StatusCode:    status,
				Header:        make(http.Header),
				Body:          &errAfterReader{data: []byte("abcdef"), err: errors.New("repeated drop")},
				ContentLength: -1,
			}
			resp.Header.Set("Content-Type", "audio/mpeg")
			return resp, nil
		}),
	}

	sessionID := "sess-exhaust-commit"
	token, err := s.RegisterSession(sessionID, []string{"/exhaust-commit.mp3"})
	if err != nil {
		t.Fatalf("register session: %v", err)
	}

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/track/"+sessionID+"/0?token="+token, http.NoBody)
	req.Header.Set("User-Agent", "abstp/1.0.0")
	req.SetPathValue("session_id", sessionID)
	req.SetPathValue("track_index", "0")

	rec := httptest.NewRecorder()
	s.handleTrack(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected committed status 200, got %d", rec.Code)
	}
	if int(requestCount.Load()) != len(s.retryDelays)+1 {
		t.Fatalf("expected %d attempts after commit, got %d", len(s.retryDelays)+1, requestCount.Load())
	}
}

type errWriter struct {
	header http.Header
}

func (e *errWriter) Header() http.Header {
	if e.header == nil {
		e.header = make(http.Header)
	}
	return e.header
}

func (e *errWriter) Write(_ []byte) (int, error) {
	return 0, errors.New("client broken pipe")
}

func (e *errWriter) WriteHeader(_ int) {}

func TestServer_Proxy_ClientWriteError(t *testing.T) {
	body := strings.NewReader("payload data that cannot be written")
	var bytesWritten int64
	ew := &errWriter{}

	completed, err := streamResponseBody(ew, body, nil, &bytesWritten, nil, -1, false)
	if err == nil {
		t.Fatal("expected client write error")
	}
	if completed {
		t.Fatal("expected stream not completed")
	}
}

func TestStreamResponseBody_ProgressLog(t *testing.T) {
	var lastActivity atomic.Int64
	payload := strings.Repeat("p", 3*1024*1024)
	rw := httptest.NewRecorder()
	var bytesWritten int64

	completed, err := streamResponseBody(rw, strings.NewReader(payload), nil, &bytesWritten, &lastActivity, int64(len(payload)), true)
	if err != nil {
		t.Fatalf("stream response body: %v", err)
	}
	if !completed {
		t.Fatal("expected stream completed")
	}
	if bytesWritten != int64(len(payload)) {
		t.Fatalf("expected %d bytes written, got %d", len(payload), bytesWritten)
	}
	if rw.Body.Len() != len(payload) {
		t.Fatalf("expected %d bytes in recorder body, got %d", len(payload), rw.Body.Len())
	}
}

func TestParseRange(t *testing.T) {
	tests := []struct {
		input     string
		wantEnd   string
		wantStart int64
		wantHas   bool
	}{
		{
			input:     "",
			wantStart: 0,
			wantEnd:   "",
			wantHas:   false,
		},
		{
			input:     "invalid-range",
			wantStart: 0,
			wantEnd:   "",
			wantHas:   false,
		},
		{
			input:     "bytes=not-a-number",
			wantStart: 0,
			wantEnd:   "",
			wantHas:   false,
		},
		{
			input:     "bytes=100-",
			wantStart: 100,
			wantEnd:   "",
			wantHas:   true,
		},
		{
			input:     "bytes=200-500",
			wantStart: 200,
			wantEnd:   "500",
			wantHas:   true,
		},
		{
			input:     "bytes=-500",
			wantStart: 0,
			wantEnd:   "",
			wantHas:   true,
		},
		{
			input:     "bytes=-not-a-number",
			wantStart: 0,
			wantEnd:   "",
			wantHas:   false,
		},
		{
			input:     "bytes=12345",
			wantStart: 0,
			wantEnd:   "",
			wantHas:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := parseRange(tt.input)
			if got.has != tt.wantHas || got.start != tt.wantStart || got.end != tt.wantEnd {
				t.Fatalf("parseRange(%q) = %+v, want has=%v start=%d end=%q", tt.input, got, tt.wantHas, tt.wantStart, tt.wantEnd)
			}
		})
	}
}

type mockNonTCPListener struct {
	closeErr error
}

func (m *mockNonTCPListener) Accept() (net.Conn, error) {
	return nil, errors.New("mock closed")
}

func (m *mockNonTCPListener) Close() error {
	return m.closeErr
}

func (m *mockNonTCPListener) Addr() net.Addr {
	return &net.UnixAddr{Name: "/tmp/mock.sock", Net: "unix"}
}

type mockErrorAcceptListener struct {
	net.Listener
	acceptErr error
}

func (m *mockErrorAcceptListener) Accept() (net.Conn, error) {
	return nil, m.acceptErr
}

type mockErrCloseBody struct {
	io.Reader
}

func (m *mockErrCloseBody) Close() error {
	return errors.New("close error")
}

func TestStart_NonTCPAddr(t *testing.T) {
	cleanup := SetNetListen(func(_ context.Context, _, _ string) (net.Listener, error) {
		return &mockNonTCPListener{closeErr: errors.New("close failure")}, nil
	})
	defer cleanup()

	s := New("http://example.com", "token", "1.0", false)
	if _, err := s.Start(); err == nil {
		t.Error("expected error when listener is not TCP")
	}
}

func TestStart_NetListenError(t *testing.T) {
	cleanup := SetNetListen(func(_ context.Context, _, _ string) (net.Listener, error) {
		return nil, errors.New("net listen failed")
	})
	defer cleanup()

	s := New("http://example.com", "token", "1.0", false)
	if _, err := s.Start(); err == nil {
		t.Error("expected error when netListen fails")
	}
}

func TestDefaultNetListen_Error(t *testing.T) {
	t.Parallel()

	if _, err := defaultNetListen(context.Background(), "invalid-net", "127.0.0.1:0"); err == nil {
		t.Error("expected error with invalid network")
	}
}

func TestStart_ServeError(t *testing.T) {
	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen error: %v", err)
	}

	el := &mockErrorAcceptListener{
		Listener:  ln,
		acceptErr: errors.New("accept failure"),
	}

	cleanup := SetNetListen(func(_ context.Context, _, _ string) (net.Listener, error) {
		return el, nil
	})
	defer cleanup()

	s := New("http://srv.example.net", "token", "1.0", false)
	if _, err := s.Start(); err != nil {
		t.Fatalf("unexpected Start error: %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	if shutErr := s.Shutdown(context.Background()); shutErr != nil {
		t.Fatalf("unexpected Shutdown error: %v", shutErr)
	}
}

func TestRegisterSession_TokenError(t *testing.T) {
	cleanup := SetGenerateToken(func() (string, error) {
		return "", errors.New("entropy failure")
	})
	defer cleanup()

	s := New("http://tok.example.org", "token", "1.0", false)
	if _, err := s.RegisterSession("sess-err", []string{"/t.mp3"}); err == nil {
		t.Error("expected error when generateToken fails")
	}
}

func TestDefaultGenerateToken_Error(t *testing.T) {
	cleanup := session.SetRandRead(func(_ []byte) (int, error) {
		return 0, errors.New("entropy error")
	})
	defer cleanup()

	if _, err := defaultGenerateToken(); err == nil {
		t.Error("expected error when randRead fails")
	}
}

type errCloseListener struct {
	net.Listener
}

func (e *errCloseListener) Close() error {
	closeErr := e.Listener.Close()
	return errors.Join(errors.New("listener close failure"), closeErr)
}

func TestShutdown_Error(t *testing.T) {
	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen error: %v", err)
	}

	el := &errCloseListener{Listener: ln}
	cleanup := SetNetListen(func(_ context.Context, _, _ string) (net.Listener, error) {
		return el, nil
	})
	defer cleanup()

	s := New("http://shut.example.net", "token", "1.0", false)
	if _, err := s.Start(); err != nil {
		t.Fatalf("start error: %v", err)
	}

	time.Sleep(10 * time.Millisecond)

	if err := s.Shutdown(context.Background()); err == nil {
		t.Error("expected error when listener Close fails during shutdown")
	}
}

func TestResolveUpstreamURL_Errors(t *testing.T) {
	tests := []struct {
		name     string
		absURL   string
		trackURL string
	}{
		{
			name:     "invalid abs url",
			absURL:   ":",
			trackURL: "/audio.mp3",
		},
		{
			name:     "invalid target url",
			absURL:   "http://parse.example.com",
			trackURL: "http://[::1",
		},
		{
			name:     "host mismatch",
			absURL:   "http://host.example.net",
			trackURL: "http://other.example.org/audio.mp3",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := New(tt.absURL, "token", "1.0", false)
			if _, err := s.resolveUpstreamURL(tt.trackURL); err == nil {
				t.Error("expected error")
			}
		})
	}
}

func TestResolveTrack_EmptyURL(t *testing.T) {
	s := New("http://empty.example.org", "token", "1.0", false)
	if _, status, _ := s.resolveTrack([]string{""}, "0"); status != http.StatusNotFound {
		t.Errorf("expected 404, got %d", status)
	}
}

func TestHandleTrack_CanceledContext(t *testing.T) {
	s := New("http://ctx.example.net", "token", "1.0", false)
	sessionID := "sess-ctx-cancel"
	token, err := s.RegisterSession(sessionID, []string{"/track.mp3"})
	if err != nil {
		t.Fatalf("register session: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	req := httptest.NewRequestWithContext(ctx, http.MethodGet, "/track/"+sessionID+"/0?token="+token, http.NoBody)
	req.Header.Set("User-Agent", "abstp/1.0.0")
	req.SetPathValue("session_id", sessionID)
	req.SetPathValue("track_index", "0")

	rec := httptest.NewRecorder()
	s.handleTrack(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 unwritten on canceled context, got %d", rec.Code)
	}
}

func TestNewUpstreamRequest_Errors(t *testing.T) {
	tests := []struct {
		name      string
		absURL    string
		targetURL string
		useNilCtx bool
	}{
		{
			name:      "invalid target url",
			absURL:    "http://req1.example.com",
			targetURL: ":",
		},
		{
			name:      "invalid base url",
			absURL:    ":",
			targetURL: "http://req2.example.net/audio.mp3",
		},
		{
			name:      "unauthorized host",
			absURL:    "http://req3.example.org",
			targetURL: "http://other.example.net/audio.mp3",
		},
		{
			name:      "nil context",
			absURL:    "http://req4.example.com",
			targetURL: "http://req4.example.com/audio.mp3",
			useNilCtx: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := New(tt.absURL, "token", "1.0", false)
			var ctx context.Context
			if !tt.useNilCtx {
				ctx = context.Background()
			}
			if _, err := s.newUpstreamRequest(ctx, tt.targetURL, 0, rangeHeader{}, false); err == nil {
				t.Error("expected error")
			}
		})
	}
}

func TestExecuteAttempt_NewRequestError(t *testing.T) {
	s := New("http://attempt.example.net", "token", "1.0", false)
	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/track/sess/0", http.NoBody)
	var bytesWritten int64
	var headersSent bool
	var lastActivity atomic.Int64

	ok, err := s.executeAttempt(req.Context(), rec, ":", rangeHeader{}, nil, &bytesWritten, &headersSent, &lastActivity)
	if ok || err != nil {
		t.Errorf("expected false, nil on bad upstream URL, got ok=%v err=%v", ok, err)
	}
}

func TestExecuteAttempt_BodyCloseError(t *testing.T) {
	s := New("http://close.example.org", "token", "1.0", false)
	s.httpClient = &http.Client{
		Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode:    http.StatusOK,
				Header:        make(http.Header),
				Body:          &mockErrCloseBody{Reader: strings.NewReader("sample audio data")},
				ContentLength: -1,
			}, nil
		}),
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/track/sess/0", http.NoBody)
	var bytesWritten int64
	var headersSent bool
	var lastActivity atomic.Int64

	ok, err := s.executeAttempt(req.Context(), rec, "http://close.example.org/audio.mp3", rangeHeader{}, rec, &bytesWritten, &headersSent, &lastActivity)
	if !ok || err != nil {
		t.Errorf("expected ok=true err=nil, got ok=%v err=%v", ok, err)
	}
}

func TestExecuteAttempt_CommitWriteError(t *testing.T) {
	tests := []struct {
		body      string
		spoolSize int
	}{
		{
			spoolSize: 8,
			body:      "abc",
		},
		{
			spoolSize: 4,
			body:      "longer-payload",
		},
	}

	for _, tt := range tests {
		t.Run(tt.body, func(t *testing.T) {
			s := New("http://commit.example.com", "token", "1.0", false)
			s.spoolBytes = tt.spoolSize
			s.httpClient = &http.Client{
				Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
					return &http.Response{
						StatusCode:    http.StatusOK,
						Header:        make(http.Header),
						Body:          io.NopCloser(strings.NewReader(tt.body)),
						ContentLength: -1,
					}, nil
				}),
			}

			req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/track/sess/0", http.NoBody)
			var bytesWritten int64
			var headersSent bool
			var lastActivity atomic.Int64

			ok, err := s.executeAttempt(req.Context(), &errWriter{}, "http://commit.example.com/audio.mp3", rangeHeader{}, nil, &bytesWritten, &headersSent, &lastActivity)
			if ok {
				t.Error("expected ok=false")
			}
			if err == nil {
				t.Error("expected write error")
			}
		})
	}
}

func TestServer_Proxy_RetryResetAfterHealthy(t *testing.T) {
	t.Parallel()

	var attempts atomic.Int32
	s := New("http://abs.example.org", "token", "test", false)
	s.spoolBytes = 2
	s.retryDelays = []time.Duration{time.Millisecond, time.Millisecond}
	s.minHealthyDuration = 10 * time.Millisecond
	s.minHealthyBytes = 50

	s.httpClient = &http.Client{
		Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
			att := attempts.Add(1)
			switch att {
			case 1:
				data := bytes.Repeat([]byte("a"), 60)
				return &http.Response{
					StatusCode:    http.StatusOK,
					Header:        make(http.Header),
					Body:          &errAfterReader{data: data, err: errors.New("drop after healthy bytes")},
					ContentLength: -1,
				}, nil
			case 2:
				time.Sleep(15 * time.Millisecond)
				return &http.Response{
					StatusCode:    http.StatusPartialContent,
					Header:        make(http.Header),
					Body:          &errAfterReader{data: []byte("b"), err: errors.New("drop after healthy duration")},
					ContentLength: -1,
				}, nil
			case 3:
				return nil, errors.New("immediate drop 1")
			case 4:
				return nil, errors.New("immediate drop 2")
			default:
				return nil, errors.New("unexpected attempt")
			}
		}),
	}

	sessionID := "sess-healthy-reset"
	token, err := s.RegisterSession(sessionID, []string{"/healthy.mp3"})
	if err != nil {
		t.Fatalf("register session: %v", err)
	}

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/track/"+sessionID+"/0?token="+token, http.NoBody)
	req.Header.Set("User-Agent", "abstp/1.0.0")
	req.SetPathValue("session_id", sessionID)
	req.SetPathValue("track_index", "0")

	rec := httptest.NewRecorder()
	s.handleTrack(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected committed status 200, got %d", rec.Code)
	}
	if attempts.Load() != 4 {
		t.Fatalf("expected 4 attempts (2 healthy resets + 2 exhausted failures), got %d", attempts.Load())
	}
}
