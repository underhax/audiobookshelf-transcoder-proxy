package absclient

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestGetPodcastEpisodes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		mockFn        roundTripFunc
		podcastID     string
		name          string
		wantEpisodes  int
		wantProgress  float64
		wantErr       bool
		wantISOFormat bool
	}{
		{
			name:      "empty podcast id",
			podcastID: "",
			wantErr:   true,
		},
		{
			name:      "success with progress and timestamps",
			podcastID: "pod-1",
			mockFn: func(r *http.Request) (*http.Response, error) {
				if r.URL.Path != "/api/items/pod-1" {
					t.Errorf("unexpected path: %s", r.URL.Path)
				}
				if r.URL.Query().Get("expanded") != "1" || r.URL.Query().Get("include") != "progress" {
					t.Errorf("unexpected query: %s", r.URL.RawQuery)
				}
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
							},
							{
								"id": "ep-2",
								"title": "Episode 2",
								"season": "1",
								"episode": "2",
								"publishedAt": 1713139200000,
								"audioFile": {"duration": 2400.0}
							}
						]
					},
					"userMediaProgress": [
						{"episodeId": "ep-1", "currentTime": 350.5}
					]
				}`
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader(respJSON)),
				}, nil
			},
			wantErr:       false,
			wantEpisodes:  2,
			wantProgress:  350.5,
			wantISOFormat: true,
		},
		{
			name:      "success with single object progress",
			podcastID: "pod-single",
			mockFn: func(_ *http.Request) (*http.Response, error) {
				respJSON := `{
					"media": {
						"episodes": [
							{
								"id": "ep-1",
								"title": "Episode 1",
								"duration": 1800.0
							}
						]
					},
					"userMediaProgress": {
						"episodeId": "ep-1",
						"currentTime": 420.0
					}
				}`
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader(respJSON)),
				}, nil
			},
			wantErr:       false,
			wantEpisodes:  1,
			wantProgress:  420.0,
			wantISOFormat: false,
		},
		{
			name:      "success with null progress",
			podcastID: "pod-null",
			mockFn: func(_ *http.Request) (*http.Response, error) {
				respJSON := `{
					"media": {
						"episodes": [
							{
								"id": "ep-1",
								"title": "Episode 1",
								"duration": 1800.0
							}
						]
					},
					"userMediaProgress": null
				}`
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader(respJSON)),
				}, nil
			},
			wantErr:       false,
			wantEpisodes:  1,
			wantProgress:  0.0,
			wantISOFormat: false,
		},
		{
			name:      "status non-200",
			podcastID: "pod-err",
			mockFn: func(_ *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusInternalServerError,
					Body:       io.NopCloser(strings.NewReader("server failure")),
				}, nil
			},
			wantErr: true,
		},
		{
			name:      "invalid json",
			podcastID: "pod-bad-json",
			mockFn: func(_ *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader("{bad json")),
				}, nil
			},
			wantErr: true,
		},
		{
			name:      "network transport error",
			podcastID: "pod-net-err",
			mockFn: func(_ *http.Request) (*http.Response, error) {
				return nil, errors.New("connection reset")
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c := New("http://abs.example.com", "test-token", "1.0.0", newMockHTTPClient(tt.mockFn))
			episodes, err := c.GetPodcastEpisodes(context.Background(), tt.podcastID)
			if (err != nil) != tt.wantErr {
				t.Fatalf("GetPodcastEpisodes() error = %v, wantErr = %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if len(episodes) != tt.wantEpisodes {
				t.Errorf("got %d episodes, want %d", len(episodes), tt.wantEpisodes)
			}
			if tt.wantEpisodes == 0 {
				return
			}
			if episodes[0].Progress != tt.wantProgress {
				t.Errorf("episodes[0].Progress = %v, want %v", episodes[0].Progress, tt.wantProgress)
			}
			if tt.wantISOFormat && episodes[0].PublishedAt == "" {
				t.Errorf("expected non-empty ISO publishedAt")
			}
		})
	}

	cBad := New("http://[::1]:namedport", "tok", "1.0.0", nil)
	if _, err := cBad.GetPodcastEpisodes(context.Background(), "pod-1"); err == nil {
		t.Error("expected error on invalid URL")
	}
}

func TestGetLibraries(t *testing.T) {
	t.Parallel()

	tests := []struct {
		mockFn        roundTripFunc
		name          string
		wantLibraries int
		wantErr       bool
	}{
		{
			name: "get libraries ok",
			mockFn: func(_ *http.Request) (*http.Response, error) {
				respJSON := `{"libraries":[{"id":"lib-1","name":"Audiobooks","mediaType":"book"},{"id":"lib-2","name":"Podcasts","mediaType":"podcast"}]}`
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(respJSON)),
				}, nil
			},
			wantErr:       false,
			wantLibraries: 2,
		},
		{
			name: "status 500",
			mockFn: func(_ *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusInternalServerError,
					Body:       io.NopCloser(strings.NewReader("server error")),
				}, nil
			},
			wantErr: true,
		},
		{
			name: "invalid json",
			mockFn: func(_ *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader("{invalid")),
				}, nil
			},
			wantErr: true,
		},
		{
			name: "get libraries network down",
			mockFn: func(_ *http.Request) (*http.Response, error) {
				return nil, errors.New("connection failed")
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c := New("http://abs.example.net", "tok", "1.0.0", newMockHTTPClient(tt.mockFn))
			libs, err := c.GetLibraries(context.Background())
			if (err != nil) != tt.wantErr {
				t.Fatalf("GetLibraries() error = %v, wantErr = %v", err, tt.wantErr)
			}
			if !tt.wantErr && len(libs) != tt.wantLibraries {
				t.Errorf("got %d libraries, want %d", len(libs), tt.wantLibraries)
			}
		})
	}

	cBad := New("http://[::1]:namedport", "tok", "1.0.0", nil)
	if _, err := cBad.GetLibraries(context.Background()); err == nil {
		t.Error("expected error on invalid URL")
	}
}

func TestGetMediaItems(t *testing.T) {
	t.Parallel()

	tests := []struct {
		transportErr  error
		name          string
		librariesResp string
		itemsResp     string
		wantItems     int
		librariesCode int
		itemsCode     int
		wantErr       bool
	}{
		{
			name:          "success books filter",
			librariesCode: http.StatusOK,
			librariesResp: `{"libraries":[{"id":"lib-1","name":"Books","mediaType":"book"},{"id":"lib-2","name":"Podcasts","mediaType":"podcast"}]}`,
			itemsCode:     http.StatusOK,
			itemsResp:     `{"results":[{"id":"b-1","media":{"metadata":{"title":"Book One","authorName":"Author A"},"duration":1200},"userMediaProgress":{"currentTime":100}}]}`,
			wantItems:     1,
		},
		{
			name:          "get libraries fails",
			librariesCode: http.StatusInternalServerError,
			wantErr:       true,
		},
		{
			name:          "get items status error",
			librariesCode: http.StatusOK,
			librariesResp: `{"libraries":[{"id":"lib-status-err","name":"Books","mediaType":"book"}]}`,
			itemsCode:     http.StatusInternalServerError,
			itemsResp:     "items error",
			wantErr:       true,
		},
		{
			name:          "get items bad json",
			librariesCode: http.StatusOK,
			librariesResp: `{"libraries":[{"id":"lib-bad-json","name":"Books","mediaType":"book"}]}`,
			itemsCode:     http.StatusOK,
			itemsResp:     "{bad json",
			wantErr:       true,
		},
		{
			name:          "get items transport error",
			librariesCode: http.StatusOK,
			librariesResp: `{"libraries":[{"id":"lib-net-err","name":"Books","mediaType":"book"}]}`,
			transportErr:  errors.New("network dropped"),
			wantErr:       true,
		},
		{
			name:          "new request error with invalid library id",
			librariesCode: http.StatusOK,
			librariesResp: `{"libraries":[{"id":"bad\u007fid","name":"Books","mediaType":"book"}]}`,
			wantErr:       true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mockFn := func(r *http.Request) (*http.Response, error) {
				if r.URL.Path == "/api/libraries" {
					if tt.librariesCode != http.StatusOK {
						return &http.Response{StatusCode: tt.librariesCode, Body: io.NopCloser(strings.NewReader("err"))}, nil
					}
					return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(tt.librariesResp))}, nil
				}
				if tt.transportErr != nil {
					return nil, tt.transportErr
				}
				code := tt.itemsCode
				if code == 0 {
					code = http.StatusOK
				}
				return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader(tt.itemsResp))}, nil
			}

			c := New("http://abs.example.org", "tok", "1.0.0", newMockHTTPClient(mockFn))
			items, err := c.GetMediaItems(context.Background(), "book")
			if (err != nil) != tt.wantErr {
				t.Fatalf("GetMediaItems() error = %v, wantErr = %v", err, tt.wantErr)
			}
			if !tt.wantErr && len(items) != tt.wantItems {
				t.Errorf("got %d items, want %d", len(items), tt.wantItems)
			}
		})
	}
}

func TestGetCover(t *testing.T) {
	t.Parallel()

	tests := []struct {
		mockFn          roundTripFunc
		itemID          string
		name            string
		wantContentType string
		wantErr         bool
	}{
		{
			name:    "empty item id",
			itemID:  "",
			wantErr: true,
		},
		{
			name:   "success cover image",
			itemID: "item-cov-1",
			mockFn: func(r *http.Request) (*http.Response, error) {
				if r.URL.Path != "/api/items/item-cov-1/cover" {
					t.Errorf("unexpected path: %s", r.URL.Path)
				}
				hdr := make(http.Header)
				hdr.Set("Content-Type", "image/jpeg")
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     hdr,
					Body:       io.NopCloser(strings.NewReader("fake-jpeg")),
				}, nil
			},
			wantErr:         false,
			wantContentType: "image/jpeg",
		},
		{
			name:   "not found cover 404",
			itemID: "item-cov-404",
			mockFn: func(_ *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusNotFound,
					Body:       &errReaderCloser{errOnClose: true},
				}, nil
			},
			wantErr: true,
		},
		{
			name:   "get cover dial failure",
			itemID: "item-cov-err",
			mockFn: func(_ *http.Request) (*http.Response, error) {
				return nil, errors.New("dial failure")
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c := New("http://abs.example.com", "tok", "1.0.0", newMockHTTPClient(tt.mockFn))
			body, cType, err := c.GetCover(context.Background(), tt.itemID)
			if (err != nil) != tt.wantErr {
				t.Fatalf("GetCover() error = %v, wantErr = %v", err, tt.wantErr)
			}
			if !tt.wantErr {
				if cType != tt.wantContentType {
					t.Errorf("got content type %s, want %s", cType, tt.wantContentType)
				}
				if closeErr := body.Close(); closeErr != nil {
					t.Errorf("close cover body error: %v", closeErr)
				}
			}
		})
	}

	cBad := New("http://[::1]:namedport", "tok", "1.0.0", nil)
	if _, _, err := cBad.GetCover(context.Background(), "item-1"); err == nil {
		t.Error("expected error on invalid URL")
	}
}
