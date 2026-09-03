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

func TestGetInProgressItems(t *testing.T) {
	t.Parallel()

	tests := []struct {
		transportErr roundTripFunc
		name         string
		itemsResp    string
		progressResp string
		itemsCode    int
		progressCode int
		wantItems    int
		wantDuration float64
		wantProgress float64
		wantErr      bool
	}{
		{
			name:         "success with merged items",
			itemsResp:    `{"libraryItems":[{"id":"book-1","mediaType":"book","media":{"duration":1000.0,"metadata":{"title":"Book Title","authorName":"Book Author"}}},{"id":"pod-1","mediaType":"podcast","media":{"metadata":{"title":"Pod Title","author":"Pod Host"}},"recentEpisode":{"id":"ep-1","title":"Ep Title","duration":500.0}}]}`,
			progressResp: `{"mediaProgress":[{"libraryItemId":"book-1","currentTime":250.0,"duration":1000.0},{"libraryItemId":"pod-1","episodeId":"ep-1","currentTime":125.0,"duration":500.0}]}`,
			wantItems:    2,
			wantDuration: 1000.0,
			wantProgress: 250.0,
		},
		{
			name:      "error from items-in-progress endpoint",
			itemsCode: http.StatusInternalServerError,
			itemsResp: "server failure",
			wantErr:   true,
		},
		{
			name:      "invalid json from items-in-progress",
			itemsResp: "{invalid json",
			wantErr:   true,
		},
		{
			name: "transport error",
			transportErr: func(_ *http.Request) (*http.Response, error) {
				return nil, errors.New("network down")
			},
			wantErr: true,
		},
		{
			name:         "progress endpoint returns 500 gracefully",
			itemsResp:    `{"libraryItems":[{"id":"bk-2","mediaType":"book","media":{"duration":600.0,"metadata":{"title":"Fallback Book","authorName":"FB Author"}}}]}`,
			progressCode: http.StatusInternalServerError,
			progressResp: "fail",
			wantItems:    1,
			wantDuration: 600.0,
		},
		{
			name:         "progress endpoint returns invalid json",
			itemsResp:    `{"libraryItems":[{"id":"bk-3","mediaType":"book","media":{"duration":700.0,"metadata":{"title":"Bad Progress Book","author":"BP Author"}}}]}`,
			progressResp: "{broken",
			wantItems:    1,
			wantDuration: 700.0,
		},
		{
			name:         "podcast with audioFile duration fallback",
			itemsResp:    `{"libraryItems":[{"id":"pod-af","mediaType":"podcast","media":{"metadata":{"title":"AF Pod","author":"AF Host"}},"recentEpisode":{"id":"ep-af","title":"AF Ep","duration":0,"audioFile":{"duration":360.0}}}]}`,
			progressResp: `{"mediaProgress":[{"libraryItemId":"pod-af","episodeId":"ep-af","currentTime":90.0,"duration":0}]}`,
			wantItems:    1,
			wantDuration: 360.0,
			wantProgress: 90.0,
		},
		{
			name:         "podcast episode duration from progress when zero locally",
			itemsResp:    `{"libraryItems":[{"id":"pod-pd","mediaType":"podcast","media":{"metadata":{"title":"PD Pod","author":"PD Host"}},"recentEpisode":{"id":"ep-pd","title":"PD Ep","duration":0,"audioFile":{"duration":0}}}]}`,
			progressResp: `{"mediaProgress":[{"libraryItemId":"pod-pd","episodeId":"ep-pd","currentTime":50.0,"duration":400.0}]}`,
			wantItems:    1,
			wantDuration: 400.0,
			wantProgress: 50.0,
		},
		{
			name:         "podcast fallback to byItem when episode not in progress map",
			itemsResp:    `{"libraryItems":[{"id":"pod-fb","mediaType":"podcast","media":{"metadata":{"title":"FB Pod","author":"FB Host"}},"recentEpisode":{"id":"ep-fb","title":"FB Ep","duration":300.0}}]}`,
			progressResp: `{"mediaProgress":[{"libraryItemId":"pod-fb","currentTime":75.0,"duration":300.0}]}`,
			wantItems:    1,
			wantDuration: 300.0,
			wantProgress: 75.0,
		},
		{
			name:         "book with duration fallback from progress",
			itemsResp:    `{"libraryItems":[{"id":"bk-df","mediaType":"book","media":{"duration":0,"metadata":{"title":"DF Book","authorName":"DF Author"}}}]}`,
			progressResp: `{"mediaProgress":[{"libraryItemId":"bk-df","currentTime":30.0,"duration":900.0}]}`,
			wantItems:    1,
			wantDuration: 900.0,
			wantProgress: 30.0,
		},
		{
			name:         "empty library items returns empty slice",
			itemsResp:    `{"libraryItems":[]}`,
			progressResp: `{"mediaProgress":[]}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var mockFn roundTripFunc
			if tt.transportErr != nil {
				mockFn = tt.transportErr
			} else {
				iCode := tt.itemsCode
				if iCode == 0 {
					iCode = http.StatusOK
				}
				pCode := tt.progressCode
				if pCode == 0 {
					pCode = http.StatusOK
				}
				mockFn = inProgressMockTransport(iCode, tt.itemsResp, pCode, tt.progressResp)
			}
			c := New("http://abs.example.org", "tok", "1.0.0", newMockHTTPClient(mockFn))
			items, err := c.GetInProgressItems(context.Background())
			if (err != nil) != tt.wantErr {
				t.Fatalf("GetInProgressItems() error = %v, wantErr = %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if len(items) != tt.wantItems {
				t.Errorf("got %d items, want %d", len(items), tt.wantItems)
			}
			if len(items) == 0 {
				return
			}
			if items[0].Duration != tt.wantDuration {
				t.Errorf("items[0].Duration = %v, want %v", items[0].Duration, tt.wantDuration)
			}
			if items[0].CurrentTime != tt.wantProgress {
				t.Errorf("items[0].CurrentTime = %v, want %v", items[0].CurrentTime, tt.wantProgress)
			}
		})
	}

	cBad := New("http://[::1]:namedport", "tok", "1.0.0", nil)
	if _, err := cBad.GetInProgressItems(context.Background()); err == nil {
		t.Error("expected error on invalid URL")
	}

	byItem, byEpisode := cBad.fetchProgressLookups(context.Background())
	if len(byItem) != 0 || len(byEpisode) != 0 {
		t.Error("expected empty lookup maps on invalid URL")
	}
}

func inProgressMockTransport(itemsCode int, itemsResp string, progressCode int, progressResp string) roundTripFunc {
	return func(req *http.Request) (*http.Response, error) {
		switch req.URL.Path {
		case "/api/me/items-in-progress":
			return &http.Response{StatusCode: itemsCode, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(itemsResp))}, nil
		case "/api/me/progress":
			return &http.Response{StatusCode: progressCode, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(progressResp))}, nil
		default:
			return &http.Response{StatusCode: http.StatusNotFound, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{}`))}, nil
		}
	}
}
