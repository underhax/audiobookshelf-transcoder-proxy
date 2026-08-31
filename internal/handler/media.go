package handler

import (
	"encoding/json"
	"io"
	"log"
	"net/http"

	"github.com/underhax/audiobookshelf-transcoder-proxy/internal/absclient"
)

// HandleGetBooks returns the list of audiobooks from Audiobookshelf.
func (h *Handler) HandleGetBooks(w http.ResponseWriter, r *http.Request) {
	books, err := h.absClient.GetMediaItems(r.Context(), "book")
	if err != nil {
		log.Printf("get books failed: %v", err)
		http.Error(w, `{"error":"failed to get books"}`, http.StatusBadGateway)
		return
	}
	if books == nil {
		books = []absclient.MediaItem{}
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(books); err != nil {
		log.Printf("encode books error: %v", err)
	}
}

// HandleGetPodcasts returns the list of podcasts from Audiobookshelf.
func (h *Handler) HandleGetPodcasts(w http.ResponseWriter, r *http.Request) {
	podcasts, err := h.absClient.GetMediaItems(r.Context(), "podcast")
	if err != nil {
		log.Printf("get podcasts failed: %v", err)
		http.Error(w, `{"error":"failed to get podcasts"}`, http.StatusBadGateway)
		return
	}
	if podcasts == nil {
		podcasts = []absclient.MediaItem{}
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(podcasts); err != nil {
		log.Printf("encode podcasts error: %v", err)
	}
}

// HandleGetPodcastEpisodes returns the list of episodes for a given podcast with listening progress.
func (h *Handler) HandleGetPodcastEpisodes(w http.ResponseWriter, r *http.Request) {
	podcastID := r.PathValue("podcast_id")
	episodes, err := h.absClient.GetPodcastEpisodes(r.Context(), podcastID)
	if err != nil {
		log.Printf("get podcast episodes failed: %v", err)
		http.Error(w, `{"error":"failed to get podcast episodes"}`, http.StatusBadGateway)
		return
	}
	if episodes == nil {
		episodes = []absclient.PodcastEpisode{}
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(episodes); err != nil {
		log.Printf("encode podcast episodes error: %v", err)
	}
}

// HandleGetCover proxies the cover artwork from Audiobookshelf with HTTP caching headers.
func (h *Handler) HandleGetCover(w http.ResponseWriter, r *http.Request) {
	itemID := r.PathValue("item_id")
	body, contentType, err := h.absClient.GetCover(r.Context(), itemID)
	if err != nil {
		log.Printf("get cover failed: %v", err)
		http.Error(w, `{"error":"cover not found"}`, http.StatusNotFound)
		return
	}
	defer func() {
		if closeErr := body.Close(); closeErr != nil {
			log.Printf("close cover body error: %v", closeErr)
		}
	}()

	w.Header().Set("Cache-Control", "public, max-age=86400")
	if contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}
	if _, err := io.Copy(w, body); err != nil {
		log.Printf("stream cover error: %v", err)
	}
}
