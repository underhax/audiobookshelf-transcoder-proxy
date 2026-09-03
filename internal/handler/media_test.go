package handler

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type errCloseReader struct {
	io.Reader
}

func (e *errCloseReader) Close() error {
	return errors.New("cover close error")
}

func TestGetBooksAndPodcasts(t *testing.T) {
	t.Parallel()

	mockRoundTrip := func(req *http.Request) (*http.Response, error) {
		switch req.URL.Path {
		case "/api/libraries":
			body := `{"libraries":[{"id":"lib-1","name":"Audiobooks","mediaType":"book"},{"id":"lib-2","name":"Podcasts","mediaType":"podcast"}]}`
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(body)),
			}, nil
		case "/api/libraries/lib-1/items":
			body := `{"results":[{"id":"book-1","media":{"duration":120,"metadata":{"title":"Book One","authorName":"Author One"}},"userMediaProgress":{"currentTime":45}}]}`
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(body)),
			}, nil
		case "/api/libraries/lib-2/items":
			body := `{"results":[{"id":"pod-1","media":{"duration":300,"metadata":{"title":"Podcast One","authorName":"Host One"}},"userMediaProgress":null}]}`
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(body)),
			}, nil
		default:
			return &http.Response{
				StatusCode: http.StatusNotFound,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{}`)),
			}, nil
		}
	}

	h, _ := newTestEnv(t, mockRoundTrip)
	routes := h.Routes()

	unauthReq := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/proxy/books", http.NoBody)
	unauthRec := httptest.NewRecorder()
	routes.ServeHTTP(unauthRec, unauthReq)
	if unauthRec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for unauth books, got %d", unauthRec.Code)
	}

	booksReq := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/proxy/books", http.NoBody)
	booksReq.Header.Set("Authorization", "Bearer proxy-secret-key")
	booksRec := httptest.NewRecorder()
	routes.ServeHTTP(booksRec, booksReq)
	if booksRec.Code != http.StatusOK {
		t.Fatalf("expected 200 for books, got %d", booksRec.Code)
	}
	if !strings.Contains(booksRec.Body.String(), "Book One") {
		t.Errorf("expected Book One in response, got %s", booksRec.Body.String())
	}

	podReq := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/proxy/podcasts", http.NoBody)
	podReq.Header.Set("Authorization", "Bearer proxy-secret-key")
	podRec := httptest.NewRecorder()
	routes.ServeHTTP(podRec, podReq)
	if podRec.Code != http.StatusOK {
		t.Fatalf("expected 200 for podcasts, got %d", podRec.Code)
	}
	if !strings.Contains(podRec.Body.String(), "Podcast One") {
		t.Errorf("expected Podcast One in response, got %s", podRec.Body.String())
	}

	hErr, _ := newTestEnv(t, func(_ *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusInternalServerError, Body: io.NopCloser(strings.NewReader("err"))}, nil
	})
	rErr := hErr.Routes()

	bReqFail := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/proxy/books", http.NoBody)
	bReqFail.Header.Set("Authorization", "Bearer proxy-secret-key")
	bRecFail := httptest.NewRecorder()
	rErr.ServeHTTP(bRecFail, bReqFail)
	if bRecFail.Code != http.StatusBadGateway {
		t.Errorf("expected 502 for failed books, got %d", bRecFail.Code)
	}

	pReqFail := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/proxy/podcasts", http.NoBody)
	pReqFail.Header.Set("Authorization", "Bearer proxy-secret-key")
	pRecFail := httptest.NewRecorder()
	rErr.ServeHTTP(pRecFail, pReqFail)
	if pRecFail.Code != http.StatusBadGateway {
		t.Errorf("expected 502 for failed podcasts, got %d", pRecFail.Code)
	}

	ew := &errResponseWriter{}
	h.HandleGetBooks(ew, booksReq)
	h.HandleGetPodcasts(ew, podReq)

	hEmpty, _ := newTestEnv(t, func(_ *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"libraries":[]}`))}, nil
	})
	bRecEmpty := httptest.NewRecorder()
	hEmpty.Routes().ServeHTTP(bRecEmpty, booksReq)
	if bRecEmpty.Code != http.StatusOK || !strings.Contains(bRecEmpty.Body.String(), "[]") {
		t.Errorf("expected empty json array for books, got %s", bRecEmpty.Body.String())
	}
	pRecEmpty := httptest.NewRecorder()
	hEmpty.Routes().ServeHTTP(pRecEmpty, podReq)
	if pRecEmpty.Code != http.StatusOK || !strings.Contains(pRecEmpty.Body.String(), "[]") {
		t.Errorf("expected empty json array for podcasts, got %s", pRecEmpty.Body.String())
	}
}

func TestGetPodcastEpisodes(t *testing.T) {
	t.Parallel()

	mockRoundTrip := func(req *http.Request) (*http.Response, error) {
		if req.URL.Path == "/api/items/pod-success" {
			respJSON := `{
				"media": {
					"episodes": [
						{
							"id": "ep-1",
							"title": "Episode 1",
							"season": "1",
							"episode": "1",
							"publishedAt": 1713139200,
							"audioFile": {"duration": 1800.0}
						}
					]
				},
				"userMediaProgress": [
					{"episodeId": "ep-1", "currentTime": 250.0}
				]
			}`
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(respJSON)),
			}, nil
		}
		if req.URL.Path == "/api/items/pod-fail" {
			return &http.Response{
				StatusCode: http.StatusInternalServerError,
				Body:       io.NopCloser(strings.NewReader("internal abs failure")),
			}, nil
		}
		return &http.Response{
			StatusCode: http.StatusNotFound,
			Body:       io.NopCloser(strings.NewReader("not found")),
		}, nil
	}

	h, _ := newTestEnv(t, mockRoundTrip)
	routes := h.Routes()

	unauthReq := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/proxy/podcasts/pod-success/episodes", http.NoBody)
	unauthRec := httptest.NewRecorder()
	routes.ServeHTTP(unauthRec, unauthReq)
	if unauthRec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for unauth episodes, got %d", unauthRec.Code)
	}

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/proxy/podcasts/pod-success/episodes", http.NoBody)
	req.Header.Set("Authorization", "Bearer proxy-secret-key")
	rec := httptest.NewRecorder()
	routes.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for episodes, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Episode 1") {
		t.Errorf("expected Episode 1 in response, got %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "2024-04-15") {
		t.Errorf("expected ISO date in response, got %s", rec.Body.String())
	}

	reqFail := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/proxy/podcasts/pod-fail/episodes", http.NoBody)
	reqFail.Header.Set("Authorization", "Bearer proxy-secret-key")
	recFail := httptest.NewRecorder()
	routes.ServeHTTP(recFail, reqFail)
	if recFail.Code != http.StatusBadGateway {
		t.Errorf("expected 502 for failed episodes, got %d", recFail.Code)
	}

	ew := &errResponseWriter{}
	h.HandleGetPodcastEpisodes(ew, req)

	hEmptyEp, _ := newTestEnv(t, func(_ *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"media":{"episodes":[]}}`))}, nil
	})
	epRecEmpty := httptest.NewRecorder()
	hEmptyEp.Routes().ServeHTTP(epRecEmpty, req)
	if epRecEmpty.Code != http.StatusOK || !strings.Contains(epRecEmpty.Body.String(), "[]") {
		t.Errorf("expected empty json array for episodes, got %s", epRecEmpty.Body.String())
	}
}

func TestGetCover(t *testing.T) {
	t.Parallel()

	mockRoundTrip := func(req *http.Request) (*http.Response, error) {
		if req.URL.Path == "/api/items/item-with-cover/cover" {
			hdr := make(http.Header)
			hdr.Set("Content-Type", "image/jpeg")
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     hdr,
				Body:       io.NopCloser(strings.NewReader("fake-image-bytes")),
			}, nil
		}
		return &http.Response{
			StatusCode: http.StatusNotFound,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("not found")),
		}, nil
	}

	h, _ := newTestEnv(t, mockRoundTrip)
	routes := h.Routes()

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/proxy/covers/item-with-cover", http.NoBody)
	rec := httptest.NewRecorder()
	routes.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for cover, got %d", rec.Code)
	}
	if rec.Header().Get("Cache-Control") == "" {
		t.Error("expected Cache-Control header on cover response")
	}
	if rec.Header().Get("Content-Type") != "image/jpeg" {
		t.Errorf("expected image/jpeg, got %s", rec.Header().Get("Content-Type"))
	}

	req = httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/proxy/covers/item-without-cover", http.NoBody)
	rec = httptest.NewRecorder()
	routes.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for missing cover, got %d", rec.Code)
	}

	ew := &errResponseWriter{}
	reqCoverErr := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/proxy/covers/item-with-cover", http.NoBody)
	reqCoverErr.SetPathValue("item_id", "item-with-cover")
	h.HandleGetCover(ew, reqCoverErr)
}

func TestGetCover_CloseError(t *testing.T) {
	t.Parallel()

	mockRoundTrip := func(_ *http.Request) (*http.Response, error) {
		hdr := make(http.Header)
		hdr.Set("Content-Type", "image/png")
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     hdr,
			Body:       &errCloseReader{Reader: strings.NewReader("fake-png")},
		}, nil
	}

	h, _ := newTestEnv(t, mockRoundTrip)
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/proxy/covers/item-err-close", http.NoBody)
	req.SetPathValue("item_id", "item-err-close")
	rec := httptest.NewRecorder()
	h.HandleGetCover(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
}

func TestGetInProgress(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		itemsResp string
		wantBody  string
		itemsCode int
		wantCode  int
		withAuth  bool
	}{
		{
			name:     "unauthorized request",
			wantCode: http.StatusUnauthorized,
			withAuth: false,
		},
		{
			name:      "success with active items",
			itemsResp: `{"libraryItems":[{"id":"book-prog-1","mediaType":"book","media":{"duration":500.0,"metadata":{"title":"Active Book","authorName":"Author One"}},"progressLastUpdate":1788410384658},{"id":"pod-prog-1","mediaType":"podcast","media":{"metadata":{"title":"Active Podcast","author":"Podcaster"}},"recentEpisode":{"id":"ep-prog-1","title":"Recent Ep","duration":200.0},"progressLastUpdate":1788185905651}]}`,
			wantBody:  "Active Book",
			wantCode:  http.StatusOK,
			withAuth:  true,
		},
		{
			name:      "upstream error",
			itemsResp: "err",
			itemsCode: http.StatusInternalServerError,
			wantCode:  http.StatusBadGateway,
			withAuth:  true,
		},
		{
			name:      "empty in-progress list",
			itemsResp: `{"libraryItems":[]}`,
			wantBody:  "[]",
			wantCode:  http.StatusOK,
			withAuth:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mockFn := func(req *http.Request) (*http.Response, error) {
				switch req.URL.Path {
				case "/api/me/items-in-progress":
					code := tt.itemsCode
					if code == 0 {
						code = http.StatusOK
					}
					return &http.Response{
						StatusCode: code,
						Header:     make(http.Header),
						Body:       io.NopCloser(strings.NewReader(tt.itemsResp)),
					}, nil
				case "/api/me/progress":
					progJSON := `{"mediaProgress":[{"libraryItemId":"book-prog-1","currentTime":150.0,"duration":500.0,"progress":0.3},{"libraryItemId":"pod-prog-1","episodeId":"ep-prog-1","currentTime":80.0,"duration":200.0,"progress":0.4}]}`
					return &http.Response{
						StatusCode: http.StatusOK,
						Header:     make(http.Header),
						Body:       io.NopCloser(strings.NewReader(progJSON)),
					}, nil
				default:
					return &http.Response{
						StatusCode: http.StatusNotFound,
						Header:     make(http.Header),
						Body:       io.NopCloser(strings.NewReader(`{}`)),
					}, nil
				}
			}

			h, _ := newTestEnv(t, mockFn)
			routes := h.Routes()

			req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/proxy/in-progress", http.NoBody)
			if tt.withAuth {
				req.Header.Set("Authorization", "Bearer proxy-secret-key")
			}
			rec := httptest.NewRecorder()
			routes.ServeHTTP(rec, req)

			if rec.Code != tt.wantCode {
				t.Fatalf("expected code %d, got %d", tt.wantCode, rec.Code)
			}
			if tt.wantBody != "" && !strings.Contains(rec.Body.String(), tt.wantBody) {
				t.Errorf("expected %s in body, got %s", tt.wantBody, rec.Body.String())
			}
		})
	}

	t.Run("encode error", func(t *testing.T) {
		t.Parallel()
		mockFn := func(_ *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"libraryItems":[{"id":"enc-1","mediaType":"book","media":{"duration":100,"metadata":{"title":"Enc Book","authorName":"Enc Author"}}}]}`)),
			}, nil
		}
		h, _ := newTestEnv(t, mockFn)
		ew := &errResponseWriter{}
		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/proxy/in-progress", http.NoBody)
		h.HandleGetInProgress(ew, req)
	})
}

func TestGetBookChapters(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		bookID   string
		respBody string
		wantBody string
		respCode int
		wantCode int
		withAuth bool
	}{
		{
			name:     "unauthorized request",
			bookID:   "book-1",
			wantCode: http.StatusUnauthorized,
			withAuth: false,
		},
		{
			name:     "success with chapters",
			bookID:   "book-1",
			respBody: `{"media":{"chapters":[{"id":0,"title":"Chapter 1","start":0,"end":120.5}]}}`,
			wantBody: "Chapter 1",
			wantCode: http.StatusOK,
			withAuth: true,
		},
		{
			name:     "upstream error",
			bookID:   "book-err",
			respBody: "internal error",
			respCode: http.StatusInternalServerError,
			wantCode: http.StatusBadGateway,
			withAuth: true,
		},
		{
			name:     "empty chapters list",
			bookID:   "book-empty",
			respBody: `{"media":{"chapters":[]}}`,
			wantBody: "[]",
			wantCode: http.StatusOK,
			withAuth: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mockFn := func(_ *http.Request) (*http.Response, error) {
				code := tt.respCode
				if code == 0 {
					code = http.StatusOK
				}
				return &http.Response{
					StatusCode: code,
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader(tt.respBody)),
				}, nil
			}

			h, _ := newTestEnv(t, mockFn)
			routes := h.Routes()

			path := "/api/proxy/books/" + tt.bookID + "/chapters"
			req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, path, http.NoBody)
			if tt.withAuth {
				req.Header.Set("Authorization", "Bearer proxy-secret-key")
			}
			rec := httptest.NewRecorder()
			routes.ServeHTTP(rec, req)

			if rec.Code != tt.wantCode {
				t.Fatalf("expected code %d, got %d", tt.wantCode, rec.Code)
			}
			if tt.wantBody != "" && !strings.Contains(rec.Body.String(), tt.wantBody) {
				t.Errorf("expected %s in body, got %s", tt.wantBody, rec.Body.String())
			}
		})
	}

	t.Run("encode error", func(t *testing.T) {
		t.Parallel()
		mockFn := func(_ *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"media":{"chapters":[{"id":0,"title":"Ch Enc","start":0,"end":10}]}}`)),
			}, nil
		}
		h, _ := newTestEnv(t, mockFn)
		ew := &errResponseWriter{}
		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/proxy/books/book-1/chapters", http.NoBody)
		req.SetPathValue("book_id", "book-1")
		h.HandleGetBookChapters(ew, req)
	})
}
