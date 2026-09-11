package absclient

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type roundTripFunc func(req *http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type errReaderCloser struct {
	errOnRead  bool
	errOnClose bool
}

func (e *errReaderCloser) Read(_ []byte) (int, error) {
	if e.errOnRead {
		return 0, errors.New("read failed")
	}
	return 0, io.EOF
}

func (e *errReaderCloser) Close() error {
	if e.errOnClose {
		return errors.New("close failed")
	}
	return nil
}

func newMockHTTPClient(fn roundTripFunc) *http.Client {
	return &http.Client{
		Transport: fn,
	}
}

func TestNewClient_Defaults(t *testing.T) {
	t.Parallel()

	c := New("http://abs.example.org:13378/", "my-token", "1.0.0", nil)
	if c == nil {
		t.Fatal("expected non-nil client")
	}
}

func TestDetectBaseURL(t *testing.T) {
	t.Parallel()

	cSub := New("http://sub.abs.example.org/audiobookshelf", "tok", "1.0.0", nil)
	if got := cSub.DetectBaseURL(context.Background()); got != "http://sub.abs.example.org/audiobookshelf" {
		t.Errorf("expected unchanged subpath URL, got %s", got)
	}

	cRoot := New("http://root.abs.example.net", "tok", "1.0.0", newMockHTTPClient(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path == "/ping" {
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       &errReaderCloser{errOnClose: true},
			}, nil
		}
		return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader(""))}, nil
	}))
	if got := cRoot.DetectBaseURL(context.Background()); got != "http://root.abs.example.net" {
		t.Errorf("expected root URL, got %s", got)
	}

	cAuto := New("http://auto.abs.example.com", "tok", "1.0.0", newMockHTTPClient(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path == "/audiobookshelf/ping" {
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       &errReaderCloser{errOnClose: true},
			}, nil
		}
		return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader(""))}, nil
	}))
	if got := cAuto.DetectBaseURL(context.Background()); got != "http://auto.abs.example.com/audiobookshelf" {
		t.Errorf("expected detected subpath URL, got %s", got)
	}

	cFail := New("http://fail.abs.example.org", "tok", "1.0.0", newMockHTTPClient(func(_ *http.Request) (*http.Response, error) {
		return nil, errors.New("network error")
	}))
	if got := cFail.DetectBaseURL(context.Background()); got != "http://fail.abs.example.org" {
		t.Errorf("expected fallback URL, got %s", got)
	}

	cAcc := New("http://acc.abs.example.net", "tok", "1.0.0", nil)
	if cAcc.BaseURL() != "http://acc.abs.example.net" {
		t.Errorf("expected BaseURL %s, got %s", "http://acc.abs.example.net", cAcc.BaseURL())
	}
	cAcc.SetBaseURL("http://acc.abs.example.net/custom/")
	if cAcc.BaseURL() != "http://acc.abs.example.net/custom" {
		t.Errorf("expected trimmed BaseURL, got %s", cAcc.BaseURL())
	}

	cBad := New("http://[::1]:namedport", "tok", "1.0.0", nil)
	_ = cBad.DetectBaseURL(context.Background())
}

func TestClient_VersionAndUserAgent(t *testing.T) {
	t.Parallel()

	capturedUserAgent := ""
	client := New("http://ua.abs.example.org", "test-token", "2.1.0", newMockHTTPClient(func(req *http.Request) (*http.Response, error) {
		capturedUserAgent = req.Header.Get("User-Agent")
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{"libraries":[]}`)),
		}, nil
	}))

	if got := client.Version(); got != "2.1.0" {
		t.Errorf("expected version 2.1.0, got %s", got)
	}

	_, err := client.GetLibraries(context.Background())
	if err != nil {
		t.Fatalf("unexpected error getting libraries: %v", err)
	}

	if capturedUserAgent != "abstp/2.1.0" {
		t.Errorf("expected User-Agent %q, got %q", "abstp/2.1.0", capturedUserAgent)
	}
}

func TestGetLibraries_CacheAndDeduplication(t *testing.T) {
	t.Parallel()

	var reqCount atomic.Int32
	client := New("http://cache-libs.abs.example.org", "tok", "1.0.0", newMockHTTPClient(func(_ *http.Request) (*http.Response, error) {
		reqCount.Add(1)
		time.Sleep(10 * time.Millisecond)
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{"libraries":[{"id":"lib1","name":"Audiobooks","mediaType":"book"}]}`)),
		}, nil
	}))

	client.librariesTTL = 50 * time.Millisecond

	var wg sync.WaitGroup
	for range 5 {
		wg.Go(func() {
			libs, err := client.GetLibraries(t.Context())
			if err != nil {
				t.Errorf("unexpected error: %v", err)
			}
			if len(libs) != 1 || libs[0].ID != "lib1" {
				t.Errorf("unexpected libs: %v", libs)
			}
		})
	}
	wg.Wait()

	if count := reqCount.Load(); count != 1 {
		t.Errorf("expected 1 network request for concurrent calls, got %d", count)
	}

	libs, err := client.GetLibraries(t.Context())
	if err != nil || len(libs) != 1 {
		t.Fatalf("unexpected cache hit result: %v, %v", libs, err)
	}
	if count := reqCount.Load(); count != 1 {
		t.Errorf("expected 1 network request for cached call, got %d", count)
	}

	time.Sleep(60 * time.Millisecond)

	libs2, err2 := client.GetLibraries(t.Context())
	if err2 != nil || len(libs2) != 1 {
		t.Fatalf("unexpected refreshed result: %v, %v", libs2, err2)
	}
	if count := reqCount.Load(); count != 2 {
		t.Errorf("expected 2 network requests after cache expiry, got %d", count)
	}

	client.librariesTTL = 0
	libs3, err3 := client.GetLibraries(t.Context())
	if err3 != nil || len(libs3) != 1 {
		t.Fatalf("unexpected fallback result: %v, %v", libs3, err3)
	}

	canceledCtx, cancel := context.WithCancel(t.Context())
	cancel()
	client.librariesCachedAt = time.Time{}
	if _, err := client.GetLibraries(canceledCtx); err == nil {
		t.Error("expected error for canceled context on cache miss")
	}
}

func TestFetchProgressLookups_CacheAndDeduplication(t *testing.T) {
	t.Parallel()

	var reqCount atomic.Int32
	client := New("http://cache-prog.abs.example.org", "tok", "1.0.0", newMockHTTPClient(func(_ *http.Request) (*http.Response, error) {
		reqCount.Add(1)
		time.Sleep(10 * time.Millisecond)
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{"mediaProgress":[{"libraryItemId":"item1","episodeId":"ep1","currentTime":12.5}]}`)),
		}, nil
	}))

	client.progressTTL = 50 * time.Millisecond

	var wg sync.WaitGroup
	for range 5 {
		wg.Go(func() {
			byItem, byEpisode := client.fetchProgressLookups(t.Context())
			if len(byItem) != 1 || byItem["item1"].CurrentTime != 12.5 {
				t.Errorf("unexpected byItem: %v", byItem)
			}
			if len(byEpisode) != 1 || byEpisode["ep1"].CurrentTime != 12.5 {
				t.Errorf("unexpected byEpisode: %v", byEpisode)
			}
		})
	}
	wg.Wait()

	if count := reqCount.Load(); count != 1 {
		t.Errorf("expected 1 network request for concurrent progress calls, got %d", count)
	}

	byItem, byEpisode := client.fetchProgressLookups(t.Context())
	if len(byItem) != 1 || len(byEpisode) != 1 {
		t.Fatalf("unexpected cached progress: %v, %v", byItem, byEpisode)
	}
	if count := reqCount.Load(); count != 1 {
		t.Errorf("expected 1 network request for cached progress call, got %d", count)
	}

	time.Sleep(60 * time.Millisecond)

	byItem2, byEpisode2 := client.fetchProgressLookups(t.Context())
	if len(byItem2) != 1 || len(byEpisode2) != 1 {
		t.Fatalf("unexpected refreshed progress: %v, %v", byItem2, byEpisode2)
	}
	if count := reqCount.Load(); count != 2 {
		t.Errorf("expected 2 network requests after progress cache expiry, got %d", count)
	}
}

func TestFetchProgressLookups_ContextAndErrors(t *testing.T) {
	t.Parallel()

	client := New("http://cache-ctx.abs.example.org", "tok", "1.0.0", newMockHTTPClient(func(_ *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{"mediaProgress":[{"libraryItemId":"item2","currentTime":5.0}]}`)),
		}, nil
	}))

	client.progressTTL = 0
	byItem, byEpisode := client.fetchProgressLookups(t.Context())
	if len(byItem) != 1 || len(byEpisode) != 0 {
		t.Fatalf("unexpected fallback progress: %v, %v", byItem, byEpisode)
	}

	canceledCtx, cancel := context.WithCancel(t.Context())
	cancel()
	client.progressCachedAt = time.Now().Add(-1 * time.Hour)
	byItemCached, _ := client.fetchProgressLookups(canceledCtx)
	if len(byItemCached) != 1 {
		t.Error("expected cached progress on canceled context when cache is populated")
	}

	client.cachedByItem = nil
	client.progressCachedAt = time.Time{}
	byItemEmpty, _ := client.fetchProgressLookups(canceledCtx)
	if len(byItemEmpty) != 0 {
		t.Error("expected empty progress for canceled context with no cache")
	}

	failClient := New("http://fail-prog.abs.example.net", "tok", "1.0.0", newMockHTTPClient(func(_ *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusInternalServerError,
			Body:       io.NopCloser(strings.NewReader("server error")),
		}, nil
	}))
	byItemFail, byEpisodeFail := failClient.fetchProgressLookups(t.Context())
	if len(byItemFail) != 0 || len(byEpisodeFail) != 0 {
		t.Errorf("expected empty maps on network error, got %v, %v", byItemFail, byEpisodeFail)
	}
}

func TestFetchLibraryItems_CacheAndDeduplication(t *testing.T) {
	t.Parallel()

	var reqCount atomic.Int32
	client := New("http://cache-items.abs.example.org", "tok", "1.0.0", newMockHTTPClient(func(_ *http.Request) (*http.Response, error) {
		reqCount.Add(1)
		time.Sleep(10 * time.Millisecond)
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{"results":[{"id":"it1","media":{"metadata":{"title":"Book 1"},"duration":100}}]}`)),
		}, nil
	}))

	client.libraryItemsTTL = 50 * time.Millisecond

	var wg sync.WaitGroup
	for range 5 {
		wg.Go(func() {
			items, err := client.fetchLibraryItems(t.Context(), "lib1")
			if err != nil {
				t.Errorf("unexpected error: %v", err)
			}
			if len(items) != 1 || items[0].ID != "it1" {
				t.Errorf("unexpected items: %v", items)
			}
		})
	}
	wg.Wait()

	if count := reqCount.Load(); count != 1 {
		t.Errorf("expected 1 network request for concurrent library items calls, got %d", count)
	}

	items, err := client.fetchLibraryItems(t.Context(), "lib1")
	if err != nil || len(items) != 1 {
		t.Fatalf("unexpected cached library items: %v, %v", items, err)
	}
	if count := reqCount.Load(); count != 1 {
		t.Errorf("expected 1 network request for cached library items call, got %d", count)
	}
}

func TestFetchLibraryItems_ExpirationAndFallback(t *testing.T) {
	t.Parallel()

	var reqCount atomic.Int32
	client := New("http://cache-items-exp.abs.example.org", "tok", "1.0.0", newMockHTTPClient(func(_ *http.Request) (*http.Response, error) {
		reqCount.Add(1)
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{"results":[{"id":"it1","media":{"metadata":{"title":"Book 1"},"duration":100}}]}`)),
		}, nil
	}))

	client.libraryItemsTTL = 20 * time.Millisecond
	if _, err := client.fetchLibraryItems(t.Context(), "lib1"); err != nil {
		t.Fatalf("unexpected initial fetch error: %v", err)
	}

	time.Sleep(30 * time.Millisecond)

	items2, err2 := client.fetchLibraryItems(t.Context(), "lib1")
	if err2 != nil || len(items2) != 1 {
		t.Fatalf("unexpected refreshed library items: %v, %v", items2, err2)
	}
	if count := reqCount.Load(); count != 2 {
		t.Errorf("expected 2 network requests after library items cache expiry, got %d", count)
	}

	client.libraryItemsTTL = 0
	items3, err3 := client.fetchLibraryItems(t.Context(), "lib1")
	if err3 != nil || len(items3) != 1 {
		t.Fatalf("unexpected fallback library items: %v, %v", items3, err3)
	}

	client.cachedLibraryItems = nil
	items4, err4 := client.fetchLibraryItems(t.Context(), "lib2")
	if err4 != nil || len(items4) != 1 {
		t.Fatalf("unexpected uninitialized cache result: %v, %v", items4, err4)
	}
}

func TestFetchLibraryItems_Errors(t *testing.T) {
	t.Parallel()

	client := New("http://cache-items-err.abs.example.org", "tok", "1.0.0", newMockHTTPClient(func(req *http.Request) (*http.Response, error) {
		if strings.Contains(req.URL.Path, "status-err") {
			return &http.Response{
				StatusCode: http.StatusBadGateway,
				Body:       io.NopCloser(strings.NewReader("bad gateway")),
			}, nil
		}
		if strings.Contains(req.URL.Path, "json-err") {
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader("invalid-json")),
			}, nil
		}
		return nil, errors.New("network failure")
	}))

	canceledCtx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := client.fetchLibraryItems(canceledCtx, "canceled-lib"); err == nil {
		t.Error("expected error for canceled context on library items cache miss")
	}

	if _, err := client.fetchLibraryItems(t.Context(), "status-err"); err == nil {
		t.Error("expected error for bad status on library items")
	}

	if _, err := client.fetchLibraryItems(t.Context(), "json-err"); err == nil {
		t.Error("expected error for invalid json on library items")
	}

	if _, err := client.fetchLibraryItems(t.Context(), "net-err"); err == nil {
		t.Error("expected error for network error on library items")
	}
}
