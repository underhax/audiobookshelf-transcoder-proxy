// Package handler implements the HTTP endpoints for the audiobookshelf transcoder proxy.
package handler

import (
	"crypto/subtle"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/underhax/audiobookshelf-transcoder-proxy/internal/absclient"
	"github.com/underhax/audiobookshelf-transcoder-proxy/internal/config"
	"github.com/underhax/audiobookshelf-transcoder-proxy/internal/session"
	"github.com/underhax/audiobookshelf-transcoder-proxy/internal/validator"
)

const (
	maxRequestBodySize = 1048576
	syncInterval       = 30 * time.Second
)

// Handler coordinates request validation, background playback synchronization, and real-time FFmpeg audio streaming.
type Handler struct {
	absClient         *absclient.Client
	store             *session.Store
	cfg               *config.Config
	syncInterval      time.Duration
	keepaliveInterval time.Duration
}

// NewHandler initializes a new Handler.
func NewHandler(cfg *config.Config, store *session.Store, absClient *absclient.Client) *Handler {
	return &Handler{
		cfg:               cfg,
		store:             store,
		absClient:         absClient,
		syncInterval:      syncInterval,
		keepaliveInterval: 1 * time.Second,
	}
}

// Routes constructs and returns the HTTP serve mux for the proxy, protected by security middleware.
func (h *Handler) Routes() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /health", h.HandleHealth)
	mux.HandleFunc("GET /api/proxy/books", h.requireAuth(h.HandleGetBooks))
	mux.HandleFunc("GET /api/proxy/podcasts", h.requireAuth(h.HandleGetPodcasts))
	mux.HandleFunc("GET /api/proxy/podcasts/{podcast_id}/episodes", h.requireAuth(h.HandleGetPodcastEpisodes))
	mux.HandleFunc("GET /api/proxy/covers/{item_id}", h.HandleGetCover)
	mux.HandleFunc("POST /api/proxy/session/start", h.requireAuth(h.HandleSessionStart))
	mux.HandleFunc("GET /stream/{session_id}", h.HandleStream)
	mux.HandleFunc("POST /api/proxy/session/stop", h.requireAuth(h.HandleSessionStop))

	return securePathMiddleware(mux)
}

func securePathMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path

		if len(path) > 256 {
			http.Error(w, `{"error":"bad request: path too long"}`, http.StatusBadRequest)
			return
		}

		if strings.Contains(r.RequestURI, "//") || strings.Contains(r.RequestURI, "..") {
			http.Error(w, `{"error":"bad request: invalid path characters"}`, http.StatusBadRequest)
			return
		}

		for i := range path {
			c := path[i]
			valid := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '/' || c == '-' || c == '.' || c == '_'
			if !valid {
				http.Error(w, `{"error":"bad request: invalid character in path"}`, http.StatusBadRequest)
				return
			}
		}

		next.ServeHTTP(w, r)
	})
}

func (h *Handler) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		authHeader := r.Header.Get("Authorization")
		if authHeader == "" {
			http.Error(w, `{"error":"missing authorization header"}`, http.StatusUnauthorized)
			return
		}

		parts := strings.SplitN(authHeader, " ", 2)
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
			http.Error(w, `{"error":"invalid authorization header format"}`, http.StatusUnauthorized)
			return
		}

		providedKey := parts[1]
		if subtle.ConstantTimeCompare([]byte(providedKey), []byte(h.cfg.APIKey)) != 1 {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}

		next(w, r)
	}
}

// HandleHealth returns HTTP 200 OK for liveness and readiness probes.
func (h *Handler) HandleHealth(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write([]byte(`{"status":"ok"}`)); err != nil {
		log.Printf("write health response error: %v", err)
	}
}

func (h *Handler) resolveExternalBaseURL(r *http.Request) (string, error) {
	if h.cfg.ExternalURL != "" {
		return h.cfg.ExternalURL, nil
	}

	proto := "http"
	fwdProto := strings.ToLower(strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")))
	if fwdProto != "" {
		if fwdProto != "http" && fwdProto != "https" {
			return "", errors.New("invalid X-Forwarded-Proto header: must be http or https")
		}
		proto = fwdProto
	} else if r.TLS != nil {
		proto = "https"
	}

	host := strings.TrimSpace(r.Header.Get("X-Forwarded-Host"))
	if host != "" {
		if strings.Contains(host, ",") {
			host = strings.TrimSpace(strings.Split(host, ",")[0])
		}
	} else {
		host = strings.TrimSpace(r.Host)
	}

	if host == "" {
		host = h.cfg.ListenAddr
	}

	if err := validator.ValidateServerName(host); err != nil {
		return "", fmt.Errorf("invalid host %q: %w", host, err)
	}

	return proto + "://" + host, nil
}
