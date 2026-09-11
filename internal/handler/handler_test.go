package handler

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/underhax/audiobookshelf-transcoder-proxy/internal/absclient"
	"github.com/underhax/audiobookshelf-transcoder-proxy/internal/config"
	"github.com/underhax/audiobookshelf-transcoder-proxy/internal/session"
	"github.com/underhax/audiobookshelf-transcoder-proxy/internal/trackproxy"
)

type roundTripFunc func(req *http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func newTestClient(fn roundTripFunc) *http.Client {
	if fn == nil {
		fn = func(_ *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{}`)),
			}, nil
		}
	}
	return &http.Client{
		Transport: fn,
	}
}

func newTestEnv(t *testing.T, roundTrip roundTripFunc) (*Handler, *session.Store) {
	t.Helper()

	cfg := &config.Config{
		ABSURL:         "http://abs.example.org:13378",
		ABSToken:       "abs-secret-token",
		APIKey:         "proxy-secret-key",
		ListenAddr:     "127.0.0.1:8099",
		ExternalURL:    "http://proxy.example.org:8099",
		FFmpegPath:     "echo",
		BufferDuration: 10 * time.Second,
	}

	store := session.NewStore(30 * time.Second)
	absCli := absclient.New(cfg.ABSURL, cfg.ABSToken, "1.0.0", newTestClient(roundTrip))
	tp := trackproxy.New(cfg.ABSURL, cfg.ABSToken, "1.0.0", cfg.Debug)
	h := NewHandler(cfg, store, absCli, tp)

	return h, store
}

func TestSecurityMiddleware_MaxConns(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		MaxConns:   1,
		MaxStreams: 1,
	}
	h := NewHandler(cfg, nil, nil, nil)
	routes := h.Routes()

	h.connsSem <- struct{}{}

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/health", http.NoBody)
	rec := httptest.NewRecorder()

	routes.ServeHTTP(rec, req)

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429 Too Many Requests, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "server overloaded") {
		t.Errorf("expected server overloaded message, got %s", rec.Body.String())
	}

	<-h.connsSem
}

type errResponseWriter struct{}

func (e *errResponseWriter) Header() http.Header {
	return make(http.Header)
}

func (e *errResponseWriter) Write(_ []byte) (int, error) {
	return 0, errors.New("write failed")
}

func (e *errResponseWriter) WriteHeader(_ int) {}

func TestRoot(t *testing.T) {
	t.Parallel()

	h, _ := newTestEnv(t, nil)
	routes := h.Routes()

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", http.NoBody)
	rec := httptest.NewRecorder()

	routes.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", rec.Code)
	}
	if rec.Body.String() != "OK\n" {
		t.Errorf("expected body OK\\n, got %q", rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/plain") {
		t.Errorf("expected text/plain content type, got %s", ct)
	}
	if csp := rec.Header().Get("Content-Security-Policy"); csp != "default-src 'none'; frame-ancestors 'none';" {
		t.Errorf("unexpected CSP header: %q", csp)
	}
	if nosniff := rec.Header().Get("X-Content-Type-Options"); nosniff != "nosniff" {
		t.Errorf("unexpected X-Content-Type-Options: %q", nosniff)
	}
	if frame := rec.Header().Get("X-Frame-Options"); frame != "DENY" {
		t.Errorf("unexpected X-Frame-Options: %q", frame)
	}
}

func TestRoot_WriteError(t *testing.T) {
	t.Parallel()

	h, _ := newTestEnv(t, nil)
	ew := &errResponseWriter{}
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", http.NoBody)
	h.HandleRoot(ew, req)
}

func TestFavicon(t *testing.T) {
	t.Parallel()

	h, _ := newTestEnv(t, nil)
	routes := h.Routes()

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/favicon.ico", http.NoBody)
	rec := httptest.NewRecorder()

	routes.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204 No Content, got %d", rec.Code)
	}
}

func TestHealth(t *testing.T) {
	t.Parallel()

	h, _ := newTestEnv(t, nil)
	routes := h.Routes()

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/health", http.NoBody)
	rec := httptest.NewRecorder()

	routes.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"status":"ok"`) {
		t.Errorf("unexpected body: %s", rec.Body.String())
	}
}

func TestHealth_WriteError(t *testing.T) {
	t.Parallel()

	h, _ := newTestEnv(t, nil)
	ew := &errResponseWriter{}
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/health", http.NoBody)
	h.HandleHealth(ew, req)
}

func TestRequireAuth(t *testing.T) {
	t.Parallel()

	h, _ := newTestEnv(t, nil)
	routes := h.Routes()

	tests := []struct {
		authHeader string
		name       string
		wantCode   int
	}{
		{
			name:       "missing auth header",
			authHeader: "",
			wantCode:   http.StatusUnauthorized,
		},
		{
			name:       "malformed auth header format",
			authHeader: "Basic 12345",
			wantCode:   http.StatusUnauthorized,
		},
		{
			name:       "wrong bearer token",
			authHeader: "Bearer wrong-key",
			wantCode:   http.StatusUnauthorized,
		},
		{
			name:       "valid bearer token",
			authHeader: "Bearer proxy-secret-key",
			wantCode:   http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/proxy/session/start", http.NoBody)
			if tt.authHeader != "" {
				req.Header.Set("Authorization", tt.authHeader)
			}
			rec := httptest.NewRecorder()

			routes.ServeHTTP(rec, req)

			if rec.Code != tt.wantCode {
				t.Errorf("got code %d, want %d", rec.Code, tt.wantCode)
			}
		})
	}
}

func TestSecurePathMiddleware(t *testing.T) {
	t.Parallel()

	h, _ := newTestEnv(t, nil)
	routes := h.Routes()

	tests := []struct {
		name       string
		path       string
		requestURI string
		wantStatus int
	}{
		{
			name:       "valid health path",
			path:       "/health",
			requestURI: "/health",
			wantStatus: http.StatusOK,
		},
		{
			name:       "path too long over 256 chars",
			path:       "/" + strings.Repeat("a", 257),
			requestURI: "/" + strings.Repeat("a", 257),
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "double slash in path",
			path:       "//health",
			requestURI: "//health",
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "dot dot path traversal",
			path:       "/../etc/passwd",
			requestURI: "/../etc/passwd",
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "invalid character in path space",
			path:       "/api/proxy/covers/my file",
			requestURI: "/api/proxy/covers/my file",
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "invalid character in path special symbol",
			path:       "/api/proxy/covers/item$1",
			requestURI: "/api/proxy/covers/item$1",
			wantStatus: http.StatusBadRequest,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			req := &http.Request{
				Method: http.MethodGet,
				URL: &url.URL{
					Path: tc.path,
				},
				RequestURI: tc.requestURI,
			}
			rec := httptest.NewRecorder()
			routes.ServeHTTP(rec, req)
			if rec.Code != tc.wantStatus {
				t.Errorf("for path %q expected code %d, got %d", tc.path, tc.wantStatus, rec.Code)
			}
		})
	}
}

func TestSessionStart_DynamicExternalURL(t *testing.T) {
	t.Parallel()

	cases := []struct {
		setupReq func(req *http.Request)
		name     string
		wantURL  string
		wantCode int
	}{
		{
			name: "inferred from Host header",
			setupReq: func(req *http.Request) {
				req.Host = "192.168.1.50:9099"
			},
			wantCode: http.StatusOK,
			wantURL:  "http://192.168.1.50:9099/stream/",
		},
		{
			name: "inferred from X-Forwarded headers",
			setupReq: func(req *http.Request) {
				req.Header.Set("X-Forwarded-Host", "abstp.example.org")
				req.Header.Set("X-Forwarded-Proto", "https")
			},
			wantCode: http.StatusOK,
			wantURL:  "https://abstp.example.org/stream/",
		},
		{
			name: "inferred from comma-separated X-Forwarded-Host",
			setupReq: func(req *http.Request) {
				req.Header.Set("X-Forwarded-Host", "proxy.example.net:8443, proxy2.example.net")
				req.Header.Set("X-Forwarded-Proto", "https")
			},
			wantCode: http.StatusOK,
			wantURL:  "https://proxy.example.net:8443/stream/",
		},
		{
			name: "inferred from direct TLS connection",
			setupReq: func(req *http.Request) {
				req.Host = "192.168.1.50:9099"
				req.TLS = &tls.ConnectionState{}
			},
			wantCode: http.StatusOK,
			wantURL:  "https://192.168.1.50:9099/stream/",
		},
		{
			name: "error when Host contains invalid characters",
			setupReq: func(req *http.Request) {
				req.Host = "evil.com/path;injection"
			},
			wantCode: http.StatusBadRequest,
		},
		{
			name: "error when X-Forwarded-Host contains invalid port",
			setupReq: func(req *http.Request) {
				req.Header.Set("X-Forwarded-Host", "abstp.example.org:99999")
			},
			wantCode: http.StatusBadRequest,
		},
		{
			name: "error when X-Forwarded-Proto is invalid",
			setupReq: func(req *http.Request) {
				req.Host = "valid.example.org"
				req.Header.Set("X-Forwarded-Proto", "javascript")
			},
			wantCode: http.StatusBadRequest,
		},
		{
			name: "fallback to listen address when host is empty",
			setupReq: func(req *http.Request) {
				req.Host = ""
			},
			wantCode: http.StatusOK,
			wantURL:  "http://127.0.0.1:8099/stream/",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			cfg := &config.Config{
				ABSURL:         "http://abs.example.org:13378",
				ABSToken:       "abs-secret-token",
				APIKey:         "proxy-secret-key",
				ListenAddr:     "127.0.0.1:8099",
				ExternalURL:    "",
				FFmpegPath:     "echo",
				BufferDuration: 10 * time.Second,
			}
			store := session.NewStore(30 * time.Second)
			roundTrip := func(_ *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     make(http.Header),
					Body: io.NopCloser(strings.NewReader(`{
						"id": "sess-dyn-1",
						"audioTracks": [{"index": 0, "startOffset": 0, "duration": 100, "contentUrl": "/track1.mp3"}]
					}`)),
				}, nil
			}
			absCli := absclient.New(cfg.ABSURL, cfg.ABSToken, "1.0.0", newTestClient(roundTrip))
			h := NewHandler(cfg, store, absCli, nil)
			routes := h.Routes()

			body := bytes.NewBufferString(`{"itemId":"book-dyn"}`)
			req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/proxy/session/start", body)
			req.Header.Set("Authorization", "Bearer proxy-secret-key")
			req.Header.Set("Content-Type", "application/json")
			tc.setupReq(req)

			rec := httptest.NewRecorder()
			routes.ServeHTTP(rec, req)

			if rec.Code != tc.wantCode {
				t.Fatalf("expected status %d, got %d: %s", tc.wantCode, rec.Code, rec.Body.String())
			}

			if tc.wantCode == http.StatusOK {
				var resp StartSessionResponse
				if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
					t.Fatalf("decode response: %v", err)
				}
				if !strings.HasPrefix(resp.StreamURL, tc.wantURL) {
					t.Errorf("expected streamURL prefix %q, got %q", tc.wantURL, resp.StreamURL)
				}
			}
		})
	}
}
