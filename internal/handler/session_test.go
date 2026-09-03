package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/underhax/audiobookshelf-transcoder-proxy/internal/absclient"
	"github.com/underhax/audiobookshelf-transcoder-proxy/internal/session"
)

func TestSessionStart(t *testing.T) {
	t.Parallel()

	tests := []struct {
		roundTrip  roundTripFunc
		name       string
		body       string
		wantSubstr string
		wantCode   int
	}{
		{
			name:       "invalid json body",
			body:       "not-json",
			wantCode:   http.StatusBadRequest,
			wantSubstr: "invalid request body",
		},
		{
			name:       "empty itemId",
			body:       `{"itemId":""}`,
			wantCode:   http.StatusBadRequest,
			wantSubstr: "itemId is required",
		},
		{
			name: "abs client start session error",
			body: `{"itemId":"book-abs-fail"}`,
			roundTrip: func(_ *http.Request) (*http.Response, error) {
				return nil, errors.New("abs offline")
			},
			wantCode:   http.StatusBadGateway,
			wantSubstr: "failed to start abs session",
		},
		{
			name: "successful session start single track",
			body: `{"itemId":"book-1","currentTime":10.5,"duration":100.0,"speed":1.25}`,
			roundTrip: func(_ *http.Request) (*http.Response, error) {
				res := absclient.PlayResponse{
					ID: "sess-abc-123",
					AudioTracks: []absclient.AudioTrack{
						{Index: 0, Duration: 100.0, ContentURL: "/single-track-1.mp3"},
					},
				}
				data, err := json.Marshal(res)
				if err != nil {
					return nil, fmt.Errorf("marshal res: %w", err)
				}
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     make(http.Header),
					Body:       io.NopCloser(bytes.NewReader(data)),
				}, nil
			},
			wantCode:   http.StatusOK,
			wantSubstr: "sess-abc-123",
		},
		{
			name: "successful session start multi track seek to second track",
			body: `{"itemId":"book-2","currentTime":65.0,"duration":120.0,"speed":0}`,
			roundTrip: func(_ *http.Request) (*http.Response, error) {
				res := absclient.PlayResponse{
					ID: "sess-multi-456",
					AudioTracks: []absclient.AudioTrack{
						{Index: 0, Duration: 60.0, ContentURL: "/multi-track-1.mp3"},
						{Index: 1, Duration: 60.0, ContentURL: "/multi-track-2.mp3"},
					},
				}
				data, err := json.Marshal(res)
				if err != nil {
					return nil, fmt.Errorf("marshal res: %w", err)
				}
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     make(http.Header),
					Body:       io.NopCloser(bytes.NewReader(data)),
				}, nil
			},
			wantCode:   http.StatusOK,
			wantSubstr: "sess-multi-456",
		},
		{
			name: "podcast start session with episodeId",
			body: `{"itemId":"pod-1","episodeId":"ep-1","currentTime":0,"duration":0}`,
			roundTrip: func(r *http.Request) (*http.Response, error) {
				if !strings.Contains(r.URL.Path, "ep-1") {
					t.Errorf("expected ep-1 in path, got %s", r.URL.Path)
				}
				res := absclient.PlayResponse{
					ID:          "sess-pod-ep-1",
					CurrentTime: 12.0,
					Duration:    600.0,
					AudioTracks: []absclient.AudioTrack{
						{Index: 0, Duration: 600.0, ContentURL: "/pod-ep-1.mp3"},
					},
				}
				data, err := json.Marshal(res)
				if err != nil {
					return nil, fmt.Errorf("marshal res: %w", err)
				}
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     make(http.Header),
					Body:       io.NopCloser(bytes.NewReader(data)),
				}, nil
			},
			wantCode:   http.StatusOK,
			wantSubstr: "sess-pod-ep-1",
		},
		{
			name: "snake case json payload support",
			body: `{"item_id":"book-snake","episode_id":"ep-snake","current_time":42.0,"speed":1.5}`,
			roundTrip: func(r *http.Request) (*http.Response, error) {
				if !strings.Contains(r.URL.Path, "ep-snake") {
					t.Errorf("expected ep-snake in path, got %s", r.URL.Path)
				}
				res := absclient.PlayResponse{
					ID:          "sess-snake-1",
					CurrentTime: 42.0,
					Duration:    300.0,
					AudioTracks: []absclient.AudioTrack{
						{Index: 0, Duration: 300.0, ContentURL: "/snake.mp3"},
					},
				}
				data, err := json.Marshal(res)
				if err != nil {
					return nil, fmt.Errorf("marshal res: %w", err)
				}
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     make(http.Header),
					Body:       io.NopCloser(bytes.NewReader(data)),
				}, nil
			},
			wantCode:   http.StatusOK,
			wantSubstr: "sess-snake-1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h, _ := newTestEnv(t, tt.roundTrip)
			routes := h.Routes()

			req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/proxy/session/start", strings.NewReader(tt.body))
			req.Header.Set("Authorization", "Bearer proxy-secret-key")
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()

			routes.ServeHTTP(rec, req)

			if rec.Code != tt.wantCode {
				t.Fatalf("expected status %d, got %d: %s", tt.wantCode, rec.Code, rec.Body.String())
			}
			if tt.wantSubstr != "" && !strings.Contains(rec.Body.String(), tt.wantSubstr) {
				t.Errorf("expected response to contain %q, got %q", tt.wantSubstr, rec.Body.String())
			}
		})
	}
}

func TestSessionStart_Metadata(t *testing.T) {
	t.Parallel()

	tests := []struct {
		roundTrip    roundTripFunc
		name         string
		body         string
		wantID       string
		wantTitle    string
		wantAuthor   string
		wantNarrator string
		wantCover    string
	}{
		{
			name: "session start populates metadata from play response and sets cover url",
			body: `{"itemId":"book-meta-1","currentTime":10.0}`,
			roundTrip: func(_ *http.Request) (*http.Response, error) {
				res := absclient.PlayResponse{
					ID:            "sess-meta-1",
					LibraryItemID: "item-meta-lib",
					DisplayTitle:  "ABS Title",
					DisplayAuthor: "ABS Author",
					MediaMetadata: &struct {
						Title        string   `json:"title"`
						AuthorName   string   `json:"authorName"`
						Author       string   `json:"author"`
						NarratorName string   `json:"narratorName"`
						Narrators    []string `json:"narrators"`
					}{
						Title:     "Meta Title",
						Narrators: []string{"Meta Narrator"},
					},
					AudioTracks: []absclient.AudioTrack{
						{Index: 0, Duration: 100.0, ContentURL: "/meta-track.mp3"},
					},
				}
				data, err := json.Marshal(res)
				if err != nil {
					return nil, fmt.Errorf("marshal res: %w", err)
				}
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     make(http.Header),
					Body:       io.NopCloser(bytes.NewReader(data)),
				}, nil
			},
			wantID:       "sess-meta-1",
			wantTitle:    "ABS Title",
			wantAuthor:   "ABS Author",
			wantNarrator: "Meta Narrator",
			wantCover:    "http://proxy.example.org:8099/api/proxy/covers/item-meta-lib",
		},
		{
			name: "session start with explicit request metadata",
			body: `{"item_id":"book-req-1","title":"Req Title","author":"Req Author","narrator":"Req Narrator","episode_title":"Req Ep","media_type":"podcast"}`,
			roundTrip: func(_ *http.Request) (*http.Response, error) {
				res := absclient.PlayResponse{
					ID: "sess-req-1",
					AudioTracks: []absclient.AudioTrack{
						{Index: 0, Duration: 50.0, ContentURL: "/req-track.mp3"},
					},
				}
				data, err := json.Marshal(res)
				if err != nil {
					return nil, fmt.Errorf("marshal res: %w", err)
				}
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     make(http.Header),
					Body:       io.NopCloser(bytes.NewReader(data)),
				}, nil
			},
			wantID:       "sess-req-1",
			wantTitle:    "Req Title",
			wantAuthor:   "Req Author",
			wantNarrator: "Req Narrator",
			wantCover:    "http://proxy.example.org:8099/api/proxy/covers/book-req-1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h, store := newTestEnv(t, tt.roundTrip)
			routes := h.Routes()

			req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/proxy/session/start", strings.NewReader(tt.body))
			req.Header.Set("Authorization", "Bearer proxy-secret-key")
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()

			routes.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
			}

			sess, ok := store.Get(tt.wantID)
			if !ok {
				t.Fatalf("session %s not found in store", tt.wantID)
			}
			if sess.Title != tt.wantTitle {
				t.Errorf("sess.Title = %q, want %q", sess.Title, tt.wantTitle)
			}
			if sess.Author != tt.wantAuthor {
				t.Errorf("sess.Author = %q, want %q", sess.Author, tt.wantAuthor)
			}
			if sess.Narrator != tt.wantNarrator {
				t.Errorf("sess.Narrator = %q, want %q", sess.Narrator, tt.wantNarrator)
			}
			if sess.CoverURL != tt.wantCover {
				t.Errorf("sess.CoverURL = %q, want %q", sess.CoverURL, tt.wantCover)
			}
		})
	}
}

func TestSessionStart_StoreError(t *testing.T) {
	t.Parallel()

	roundTrip := func(_ *http.Request) (*http.Response, error) {
		res := absclient.PlayResponse{
			ID: "",
			AudioTracks: []absclient.AudioTrack{
				{Index: 0, Duration: 100.0, ContentURL: "/store-fail-track-1.mp3"},
			},
		}
		data, err := json.Marshal(res)
		if err != nil {
			return nil, fmt.Errorf("marshal: %w", err)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(bytes.NewReader(data)),
		}, nil
	}

	h, _ := newTestEnv(t, roundTrip)
	h.store = session.NewStore(-1 * time.Second)
	routes := h.Routes()

	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/proxy/session/start", strings.NewReader(`{"itemId":"book-1"}`))
	req.Header.Set("Authorization", "Bearer proxy-secret-key")
	rec := httptest.NewRecorder()

	routes.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 on store failure, got %d", rec.Code)
	}
}

func TestHandleSessionStart_EncodeError(t *testing.T) {
	t.Parallel()

	h, _ := newTestEnv(t, func(_ *http.Request) (*http.Response, error) {
		res := absclient.PlayResponse{
			ID: "sess-ok",
			AudioTracks: []absclient.AudioTrack{
				{Index: 0, Duration: 10, ContentURL: "/track.mp3"},
			},
		}
		data, err := json.Marshal(res)
		if err != nil {
			return nil, fmt.Errorf("marshal: %w", err)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(bytes.NewReader(data)),
		}, nil
	})

	ew := &errResponseWriter{}
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/proxy/session/start", strings.NewReader(`{"itemId":"book-123"}`))
	req.Header.Set("Authorization", "Bearer proxy-secret-key")

	h.HandleSessionStart(ew, req)
}

func TestHandleSessionStart_Debug(t *testing.T) {
	t.Parallel()

	h, _ := newTestEnv(t, func(_ *http.Request) (*http.Response, error) {
		res := absclient.PlayResponse{
			ID: "sess-debug-start",
			AudioTracks: []absclient.AudioTrack{
				{Index: 0, Duration: 10, ContentURL: "/debug-track-start.mp3"},
			},
		}
		data, err := json.Marshal(res)
		if err != nil {
			return nil, fmt.Errorf("marshal: %w", err)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(bytes.NewReader(data)),
		}, nil
	})
	h.cfg.Debug = true

	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/proxy/session/start", strings.NewReader(`{"itemId":"book-debug"}`))
	req.Header.Set("Authorization", "Bearer proxy-secret-key")
	rec := httptest.NewRecorder()

	h.HandleSessionStart(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", rec.Code)
	}
}

func TestSessionStop_ValidationAndErrors(t *testing.T) {
	t.Parallel()

	h, _ := newTestEnv(t, nil)
	routes := h.Routes()

	reqBadJSON := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/proxy/session/stop", strings.NewReader("not-json"))
	reqBadJSON.Header.Set("Authorization", "Bearer proxy-secret-key")
	recBadJSON := httptest.NewRecorder()
	routes.ServeHTTP(recBadJSON, reqBadJSON)
	if recBadJSON.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for bad json in stop, got %d", recBadJSON.Code)
	}

	reqEmptyID := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/proxy/session/stop", strings.NewReader(`{"sessionId":""}`))
	reqEmptyID.Header.Set("Authorization", "Bearer proxy-secret-key")
	recEmptyID := httptest.NewRecorder()
	routes.ServeHTTP(recEmptyID, reqEmptyID)
	if recEmptyID.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for empty sessionId in stop, got %d", recEmptyID.Code)
	}

	reqNotFound := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/proxy/session/stop", strings.NewReader(`{"sessionId":"non-existent-session"}`))
	reqNotFound.Header.Set("Authorization", "Bearer proxy-secret-key")
	recNotFound := httptest.NewRecorder()
	routes.ServeHTTP(recNotFound, reqNotFound)
	if recNotFound.Code != http.StatusNotFound {
		t.Errorf("expected 404 for non-existent session in stop, got %d", recNotFound.Code)
	}
}

func TestSessionStop_Success(t *testing.T) {
	t.Parallel()

	syncCalled := false
	closeCalled := false

	roundTrip := func(req *http.Request) (*http.Response, error) {
		if strings.Contains(req.URL.Path, "/sync") {
			syncCalled = true
		}
		if strings.Contains(req.URL.Path, "/close") {
			closeCalled = true
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{}`)),
		}, nil
	}

	h, store := newTestEnv(t, roundTrip)
	routes := h.Routes()

	sess := &session.Session{
		ID: "sess-to-stop-123",
		AudioTracks: []absclient.AudioTrack{
			{Index: 0, Duration: 100.0, ContentURL: "/t1.mp3"},
		},
		CurrentTime: 20.0,
		Duration:    100.0,
		Speed:       1.0,
	}
	_, err := store.Create(sess)
	if err != nil {
		t.Fatalf("create sess: %v", err)
	}

	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/proxy/session/stop", strings.NewReader(`{"sessionId":"sess-to-stop-123"}`))
	req.Header.Set("Authorization", "Bearer proxy-secret-key")
	rec := httptest.NewRecorder()

	routes.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for stop, got %d: %s", rec.Code, rec.Body.String())
	}
	if !syncCalled || !closeCalled {
		t.Errorf("expected syncCalled=%v and closeCalled=%v to be true", syncCalled, closeCalled)
	}

	sessSnake := &session.Session{
		ID: "sess-snake-stop",
	}
	if _, err := store.Create(sessSnake); err != nil {
		t.Fatalf("create sess snake: %v", err)
	}
	reqSnake := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/proxy/session/stop", strings.NewReader(`{"session_id":"sess-snake-stop"}`))
	reqSnake.Header.Set("Authorization", "Bearer proxy-secret-key")
	recSnake := httptest.NewRecorder()
	routes.ServeHTTP(recSnake, reqSnake)
	if recSnake.Code != http.StatusOK {
		t.Fatalf("expected 200 for snake case stop, got %d", recSnake.Code)
	}
}

func TestSessionTerminate_SyncAndCloseErrors(t *testing.T) {
	t.Parallel()

	roundTrip := func(_ *http.Request) (*http.Response, error) {
		return nil, errors.New("abs network down during close/sync")
	}

	h, store := newTestEnv(t, roundTrip)
	routes := h.Routes()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cmd := exec.CommandContext(ctx, "echo", "hello")
	sess := &session.Session{
		ID:     "sess-with-cancel-and-cmd",
		Cancel: cancel,
		Cmd:    cmd,
	}
	if _, err := store.Create(sess); err != nil {
		t.Fatalf("create sess: %v", err)
	}

	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/proxy/session/stop", strings.NewReader(`{"sessionId":"sess-with-cancel-and-cmd"}`))
	req.Header.Set("Authorization", "Bearer proxy-secret-key")
	rec := httptest.NewRecorder()

	routes.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 despite upstream sync failure, got %d", rec.Code)
	}
}

func TestSessionTerminate_ProcessKillError(t *testing.T) {
	processKillMu.Lock()
	orig := processKill
	processKill = func(_ *os.Process) error {
		return errors.New("mocked kill error")
	}
	processKillMu.Unlock()
	defer func() {
		processKillMu.Lock()
		processKill = orig
		processKillMu.Unlock()
	}()

	h, store := newTestEnv(t, func(_ *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{}`)),
		}, nil
	})

	cmd := exec.CommandContext(t.Context(), "true")
	if err := cmd.Run(); err != nil {
		t.Fatalf("run true: %v", err)
	}

	sess := &session.Session{
		ID:    "sess-term-kill-err",
		Cmd:   cmd,
		Speed: 1.0,
	}
	if _, err := store.Create(sess); err != nil {
		t.Fatalf("create sess: %v", err)
	}

	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/proxy/session/stop", strings.NewReader(`{"sessionId":"sess-term-kill-err"}`))
	req.Header.Set("Authorization", "Bearer proxy-secret-key")
	rec := httptest.NewRecorder()
	h.HandleSessionStop(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
}

func TestHandleSessionTerminate_WriteError(t *testing.T) {
	t.Parallel()

	h, store := newTestEnv(t, func(_ *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{}`)),
		}, nil
	})

	sess := &session.Session{ID: "sess-write-err"}
	if _, err := store.Create(sess); err != nil {
		t.Fatalf("create sess: %v", err)
	}

	ew := &errResponseWriter{}
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/proxy/session/stop", strings.NewReader(`{"sessionId":"sess-write-err"}`))
	req.Header.Set("Authorization", "Bearer proxy-secret-key")

	h.HandleSessionStop(ew, req)
}
