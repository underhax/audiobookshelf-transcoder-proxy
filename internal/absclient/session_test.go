package absclient

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestStartSession(t *testing.T) {
	t.Parallel()

	tests := []struct {
		mockFn     roundTripFunc
		itemID     string
		episodeID  string
		name       string
		wantErr    bool
		wantTracks int
	}{
		{
			name:   "empty item id",
			itemID: "",
			mockFn: func(_ *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader("{}")),
				}, nil
			},
			wantErr: true,
		},
		{
			name:   "success response",
			itemID: "book-123",
			mockFn: func(r *http.Request) (*http.Response, error) {
				if r.Method != http.MethodPost {
					t.Errorf("expected POST, got %s", r.Method)
				}
				if r.Header.Get("Authorization") != "Bearer test-token" {
					t.Errorf("bad auth header: %s", r.Header.Get("Authorization"))
				}
				if r.URL.Path != "/api/items/book-123/play" {
					t.Errorf("unexpected URL path: %s", r.URL.Path)
				}

				respJSON := `{
					"id": "sess-1",
					"audioTracks": [
						{"index": 1, "startOffset": 0, "duration": 100, "title": "Track 1", "contentUrl": "/track1.mp3", "mimeType": "audio/mpeg"},
						{"index": 2, "startOffset": 100, "duration": 200, "title": "Track 2", "contentUrl": "/track2.mp3", "mimeType": "audio/mpeg"}
					]
				}`
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader(respJSON)),
				}, nil
			},
			wantErr:    false,
			wantTracks: 2,
		},
		{
			name:      "podcast episode with episodeId",
			itemID:    "pod-123",
			episodeID: "ep-456",
			mockFn: func(r *http.Request) (*http.Response, error) {
				if r.URL.Path != "/api/items/pod-123/play/ep-456" {
					t.Errorf("unexpected URL path: %s", r.URL.Path)
				}
				respJSON := `{
					"id": "sess-pod-1",
					"audioTracks": [
						{"index": 1, "startOffset": 0, "duration": 300, "title": "Ep 1", "contentUrl": "/ep1.mp3", "mimeType": "audio/mpeg"}
					]
				}`
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader(respJSON)),
				}, nil
			},
			wantErr:    false,
			wantTracks: 1,
		},
		{
			name:   "non 200 status",
			itemID: "book-404",
			mockFn: func(_ *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusNotFound,
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader("not found")),
				}, nil
			},
			wantErr: true,
		},
		{
			name:   "start session network down",
			itemID: "book-err",
			mockFn: func(_ *http.Request) (*http.Response, error) {
				return nil, errors.New("network down")
			},
			wantErr: true,
		},
		{
			name:   "bad json body",
			itemID: "book-bad-json",
			mockFn: func(_ *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader("invalid json")),
				}, nil
			},
			wantErr: true,
		},
		{
			name:   "body read error",
			itemID: "book-read-err",
			mockFn: func(_ *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     make(http.Header),
					Body:       &errReaderCloser{errOnRead: true},
				}, nil
			},
			wantErr: true,
		},
		{
			name:   "body close error",
			itemID: "book-close-err",
			mockFn: func(_ *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     make(http.Header),
					Body:       &errReaderCloser{errOnClose: true},
				}, nil
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			c := New("http://abs.example.com", "test-token", "1.0.0", newMockHTTPClient(tt.mockFn))
			res, err := c.StartSession(context.Background(), tt.itemID, tt.episodeID)

			if (err != nil) != tt.wantErr {
				t.Fatalf("StartSession() error = %v, wantErr = %v", err, tt.wantErr)
			}
			if !tt.wantErr && res != nil {
				if len(res.AudioTracks) != tt.wantTracks {
					t.Errorf("got %d tracks, want %d", len(res.AudioTracks), tt.wantTracks)
				}
			}
		})
	}
}

func TestStartSession_RequestError(t *testing.T) {
	t.Parallel()

	c := New("http://[::1]:namedport", "tok", "1.0.0", nil)
	_, err := c.StartSession(context.Background(), "book1", "")
	if err == nil {
		t.Fatal("expected error on invalid URL")
	}

	validCli := New("http://abs.example.com", "tok", "1.0.0", nil)
	if _, reqErr := validCli.newRequest(context.Background(), http.MethodPost, "/test", make(chan int)); reqErr == nil {
		t.Fatal("expected error when body cannot be marshaled to JSON")
	}
}

func TestSyncSession(t *testing.T) {
	t.Parallel()

	tests := []struct {
		mockFn    roundTripFunc
		name      string
		sessionID string
		wantErr   bool
	}{
		{
			name:      "empty session id",
			sessionID: "",
			mockFn: func(_ *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader("{}")),
				}, nil
			},
			wantErr: true,
		},
		{
			name:      "sync session ok",
			sessionID: "play_sync_ok",
			mockFn: func(r *http.Request) (*http.Response, error) {
				if r.Method != http.MethodPost {
					t.Errorf("expected POST, got %s", r.Method)
				}
				if r.URL.Path != "/api/session/play_sync_ok/sync" {
					t.Errorf("unexpected path: %s", r.URL.Path)
				}
				var req SyncRequest
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Errorf("decode error: %v", err)
				}
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader("")),
				}, nil
			},
			wantErr: false,
		},
		{
			name:      "transport error on sync",
			sessionID: "play_sync_err",
			mockFn: func(_ *http.Request) (*http.Response, error) {
				return nil, errors.New("network failure")
			},
			wantErr: true,
		},
		{
			name:      "status error on sync",
			sessionID: "play_sync_status_fail",
			mockFn: func(_ *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusNotFound,
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader("session not found")),
				}, nil
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			c := New("http://abs.example.com", "test-token", "1.0.0", newMockHTTPClient(tt.mockFn))
			err := c.SyncSession(context.Background(), tt.sessionID, SyncRequest{
				CurrentTime:  100.0,
				TimeListened: 30.0,
				Duration:     1000.0,
			})
			if (err != nil) != tt.wantErr {
				t.Fatalf("SyncSession() error = %v, wantErr = %v", err, tt.wantErr)
			}
		})
	}
}

func TestSyncSession_RequestError(t *testing.T) {
	t.Parallel()

	c := New("http://[::1]:namedport", "tok", "1.0.0", nil)
	err := c.SyncSession(context.Background(), "session1", SyncRequest{})
	if err == nil {
		t.Fatal("expected error on invalid URL")
	}
}

func TestCloseSession(t *testing.T) {
	t.Parallel()

	tests := []struct {
		mockFn    roundTripFunc
		name      string
		sessionID string
		wantErr   bool
	}{
		{
			name:      "empty session id",
			sessionID: "",
			mockFn: func(_ *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader("{}")),
				}, nil
			},
			wantErr: true,
		},
		{
			name:      "success",
			sessionID: "play_close_ok",
			mockFn: func(r *http.Request) (*http.Response, error) {
				if r.Method != http.MethodPost {
					t.Errorf("expected POST, got %s", r.Method)
				}
				if r.URL.Path != "/api/session/play_close_ok/close" {
					t.Errorf("unexpected path: %s", r.URL.Path)
				}
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader("")),
				}, nil
			},
			wantErr: false,
		},
		{
			name:      "transport error on close",
			sessionID: "play_close_err",
			mockFn: func(_ *http.Request) (*http.Response, error) {
				return nil, errors.New("network failure")
			},
			wantErr: true,
		},
		{
			name:      "status error on close",
			sessionID: "play_close_status_fail",
			mockFn: func(_ *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusInternalServerError,
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader("close error")),
				}, nil
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			c := New("http://abs.example.com", "test-token", "1.0.0", newMockHTTPClient(tt.mockFn))
			err := c.CloseSession(context.Background(), tt.sessionID)
			if (err != nil) != tt.wantErr {
				t.Fatalf("CloseSession() error = %v, wantErr = %v", err, tt.wantErr)
			}
		})
	}
}

func TestCloseSession_RequestError(t *testing.T) {
	t.Parallel()

	c := New("http://[::1]:namedport", "tok", "1.0.0", nil)
	err := c.CloseSession(context.Background(), "session1")
	if err == nil {
		t.Fatal("expected error on invalid URL")
	}
}
