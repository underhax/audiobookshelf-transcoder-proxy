// Package absclient provides an HTTP client for interacting with the Audiobookshelf REST API.
package absclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"maps"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	defaultLibrariesCacheTTL    = 5 * time.Minute
	defaultLibraryItemsCacheTTL = 1 * time.Minute
	defaultProgressCacheTTL     = 5 * time.Second
)

// Client encapsulates HTTP communication and session synchronization with the upstream Audiobookshelf server.
type Client struct {
	progressCachedAt   time.Time
	librariesCachedAt  time.Time
	cachedLibraryItems map[string]cachedLibraryItemsEntry
	cachedByItem       map[string]rawMediaProgressEntry
	httpClient         *http.Client
	cachedByEpisode    map[string]rawMediaProgressEntry
	version            string
	token              string
	baseURL            string
	cachedLibraries    []Library
	librariesTTL       time.Duration
	libraryItemsTTL    time.Duration
	progressTTL        time.Duration
	librariesMu        sync.Mutex
	libraryItemsMu     sync.Mutex
	progressMu         sync.Mutex
}

// New initializes an Audiobookshelf API client with credentials and HTTP transport settings.
func New(baseURL, token, version string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	cleanBase := strings.TrimRight(baseURL, "/")
	return &Client{
		baseURL:            cleanBase,
		token:              token,
		version:            version,
		httpClient:         httpClient,
		cachedLibraryItems: make(map[string]cachedLibraryItemsEntry),
		librariesTTL:       defaultLibrariesCacheTTL,
		libraryItemsTTL:    defaultLibraryItemsCacheTTL,
		progressTTL:        defaultProgressCacheTTL,
	}
}

// BaseURL returns the resolved target URL for Audiobookshelf requests.
func (c *Client) BaseURL() string {
	return c.baseURL
}

// Version returns the client application version string used for user agent headers and transcoding identification.
func (c *Client) Version() string {
	return c.version
}

// SetBaseURL updates the base URL when subpath probing dynamically discovers reverse proxy prefixes.
func (c *Client) SetBaseURL(u string) {
	c.baseURL = strings.TrimRight(u, "/")
}

func (c *Client) probeURL(ctx context.Context, probeTarget string) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, probeTarget, http.NoBody)
	if err != nil {
		return false
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return false
	}
	if closeErr := resp.Body.Close(); closeErr != nil {
		log.Printf("close probe body: %v", closeErr)
	}
	return resp.StatusCode == http.StatusOK
}

// DetectBaseURL verifies if Audiobookshelf responds at the root or requires the /audiobookshelf subpath.
func (c *Client) DetectBaseURL(ctx context.Context) string {
	if strings.HasSuffix(c.baseURL, "/audiobookshelf") {
		return c.baseURL
	}

	if c.probeURL(ctx, c.baseURL+"/ping") {
		return c.baseURL
	}

	subpathURL := c.baseURL + "/audiobookshelf"
	if c.probeURL(ctx, subpathURL+"/ping") {
		c.baseURL = subpathURL
	}

	return c.baseURL
}

func (c *Client) newRequest(ctx context.Context, method, endpoint string, body any) (*http.Request, error) {
	var bodyReader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("marshal request body: %w", err)
		}
		bodyReader = bytes.NewReader(data)
	}

	reqURL := c.baseURL + endpoint
	req, err := http.NewRequestWithContext(ctx, method, reqURL, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("create http request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("User-Agent", "abstp/"+c.version)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	return req, nil
}

func (c *Client) sendAndReadBody(req *http.Request) (status int, body []byte, err error) {
	resp, doErr := c.httpClient.Do(req)
	if doErr != nil {
		return 0, nil, fmt.Errorf("execute request: %w", doErr)
	}

	bodyBytes, readErr := io.ReadAll(io.LimitReader(resp.Body, 1048576))
	closeErr := resp.Body.Close()
	if readErr != nil {
		return 0, nil, fmt.Errorf("read response body: %w", readErr)
	}
	if closeErr != nil {
		return 0, nil, fmt.Errorf("close response body: %w", closeErr)
	}

	return resp.StatusCode, bodyBytes, nil
}

// StartSession requests Audiobookshelf to start a playback session for an item or a specific podcast episode.
func (c *Client) StartSession(ctx context.Context, itemID, episodeID string) (*PlayResponse, error) {
	if itemID == "" {
		return nil, errors.New("itemID cannot be empty")
	}

	payload := PlayRequest{
		DeviceInfo: DeviceInfo{
			DeviceID:      "abstp",
			ClientName:    "abstp",
			ClientVersion: c.version,
		},
		ForceDirectPlay: true,
		SupportedMimeTypes: []string{
			"audio/flac",
			"audio/mpeg",
			"audio/mp4",
			"audio/ogg",
			"audio/aac",
			"audio/x-m4b",
		},
	}

	endpoint := "/api/items/" + itemID + "/play"
	if episodeID != "" {
		endpoint = fmt.Sprintf("/api/items/%s/play/%s", itemID, episodeID)
	}
	req, err := c.newRequest(ctx, http.MethodPost, endpoint, payload)
	if err != nil {
		return nil, err
	}

	status, bodyBytes, err := c.sendAndReadBody(req)
	if err != nil {
		return nil, err
	}

	if status != http.StatusOK {
		return nil, fmt.Errorf("start session failed with status %d: %s", status, string(bodyBytes))
	}

	var playResp PlayResponse
	if err := json.Unmarshal(bodyBytes, &playResp); err != nil {
		return nil, fmt.Errorf("decode play response: %w", err)
	}

	return &playResp, nil
}

// SyncSession synchronizes the listening progress for an open session with Audiobookshelf.
func (c *Client) SyncSession(ctx context.Context, sessionID string, progress SyncRequest) error {
	if sessionID == "" {
		return errors.New("sessionID cannot be empty")
	}

	endpoint := "/api/session/" + sessionID + "/sync"
	req, err := c.newRequest(ctx, http.MethodPost, endpoint, progress)
	if err != nil {
		return err
	}

	status, bodyBytes, err := c.sendAndReadBody(req)
	if err != nil {
		return err
	}

	if status != http.StatusOK {
		return fmt.Errorf("sync session failed with status %d: %s", status, string(bodyBytes))
	}

	return nil
}

// CloseSession closes an active playback session on Audiobookshelf.
func (c *Client) CloseSession(ctx context.Context, sessionID string) error {
	if sessionID == "" {
		return errors.New("sessionID cannot be empty")
	}

	endpoint := "/api/session/" + sessionID + "/close"
	req, err := c.newRequest(ctx, http.MethodPost, endpoint, nil)
	if err != nil {
		return err
	}

	status, bodyBytes, err := c.sendAndReadBody(req)
	if err != nil {
		return err
	}

	if status != http.StatusOK {
		return fmt.Errorf("close session failed with status %d: %s", status, string(bodyBytes))
	}

	return nil
}

// Library conveys Audiobookshelf library grouping and media categorization metadata.
type Library struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	MediaType string `json:"mediaType"`
}

// MediaItem represents an individual book or podcast containing playback duration, progress offset, and proxy cover URIs.
type MediaItem struct {
	ID         string  `json:"id"`
	Title      string  `json:"title"`
	Author     string  `json:"author,omitempty"`
	Narrator   string  `json:"narrator,omitempty"`
	MediaType  string  `json:"mediaType"`
	CoverURL   string  `json:"coverUrl"`
	Duration   float64 `json:"duration"`
	Progress   float64 `json:"progress"`
	IsFinished bool    `json:"isFinished"`
}

// PodcastEpisode contains episode metadata and user-specific listening progress returned by Audiobookshelf.
type PodcastEpisode struct {
	ID          string  `json:"id"`
	Title       string  `json:"title"`
	Season      string  `json:"season,omitempty"`
	Episode     string  `json:"episode,omitempty"`
	PublishedAt string  `json:"publishedAt,omitempty"`
	Duration    float64 `json:"duration"`
	Progress    float64 `json:"progress"`
	IsFinished  bool    `json:"isFinished"`
}

// InProgressItem represents an audiobook or podcast episode currently in progress for the authenticated user.
type InProgressItem struct {
	ID           string  `json:"id"`
	Title        string  `json:"title"`
	Author       string  `json:"author,omitempty"`
	Narrator     string  `json:"narrator,omitempty"`
	MediaType    string  `json:"mediaType"`
	CoverURL     string  `json:"coverUrl"`
	EpisodeID    string  `json:"episodeId,omitempty"`
	EpisodeTitle string  `json:"episodeTitle,omitempty"`
	Duration     float64 `json:"duration"`
	Progress     float64 `json:"progress"`
	CurrentTime  float64 `json:"currentTime"`
}

// ChapterItem represents a single chapter or track within an audiobook.
type ChapterItem struct {
	Title    string  `json:"title"`
	Start    float64 `json:"start"`
	End      float64 `json:"end"`
	Duration float64 `json:"duration"`
	ID       int     `json:"id"`
}

type bookExpandedResponse struct {
	Media struct {
		Chapters []rawChapter `json:"chapters"`
	} `json:"media"`
}

type rawChapter struct {
	Title string  `json:"title"`
	Start float64 `json:"start"`
	End   float64 `json:"end"`
	ID    int     `json:"id"`
}

type itemsInProgressResponse struct {
	LibraryItems []rawInProgressLibraryItem `json:"libraryItems"`
}

type rawInProgressEpisode struct {
	ID        string  `json:"id"`
	Title     string  `json:"title"`
	Duration  float64 `json:"duration"`
	AudioFile struct {
		Duration float64 `json:"duration"`
	} `json:"audioFile"`
}

type rawInProgressLibraryItem struct {
	RecentEpisode *rawInProgressEpisode `json:"recentEpisode"`
	ID            string                `json:"id"`
	MediaType     string                `json:"mediaType"`
	Media         struct {
		Metadata struct {
			Title        string `json:"title"`
			AuthorName   string `json:"authorName"`
			Author       string `json:"author"`
			NarratorName string `json:"narratorName"`
		} `json:"metadata"`
		Duration float64 `json:"duration"`
	} `json:"media"`
	ProgressLastUpdate int64 `json:"progressLastUpdate"`
}

type mediaProgressListResponse struct {
	MediaProgress []rawMediaProgressEntry `json:"mediaProgress"`
}

type rawMediaProgressEntry struct {
	LibraryItemID string  `json:"libraryItemId"`
	EpisodeID     string  `json:"episodeId"`
	CurrentTime   float64 `json:"currentTime"`
	Duration      float64 `json:"duration"`
	Progress      float64 `json:"progress"`
	IsFinished    bool    `json:"isFinished"`
}

type expandedItemResponse struct {
	Media struct {
		Episodes []rawEpisode `json:"episodes"`
	} `json:"media"`
	UserMediaProgress json.RawMessage `json:"userMediaProgress"`
}

type rawEpisode struct {
	ID        string  `json:"id"`
	Title     string  `json:"title"`
	Season    string  `json:"season"`
	Episode   string  `json:"episode"`
	Duration  float64 `json:"duration"`
	AudioFile struct {
		Duration float64 `json:"duration"`
	} `json:"audioFile"`
	PublishedAt int64 `json:"publishedAt"`
}

type rawEpisodeProgress struct {
	EpisodeID   string  `json:"episodeId"`
	CurrentTime float64 `json:"currentTime"`
	IsFinished  bool    `json:"isFinished"`
}

type librariesResponse struct {
	Libraries []Library `json:"libraries"`
}

type rawLibraryItemResult struct {
	UserMediaProgress *struct {
		CurrentTime float64 `json:"currentTime"`
	} `json:"userMediaProgress"`
	ID    string `json:"id"`
	Media struct {
		Metadata struct {
			Title        string `json:"title"`
			AuthorName   string `json:"authorName"`
			NarratorName string `json:"narratorName"`
		} `json:"metadata"`
		Duration float64 `json:"duration"`
	} `json:"media"`
}

type cachedLibraryItemsEntry struct {
	cachedAt time.Time
	items    []rawLibraryItemResult
}

type libraryItemsResponse struct {
	Results []rawLibraryItemResult `json:"results"`
}

// GetLibraries fetches configured library collections to discover available audiobook and podcast repositories.
func (c *Client) GetLibraries(ctx context.Context) ([]Library, error) {
	c.librariesMu.Lock()
	defer c.librariesMu.Unlock()

	ttl := c.librariesTTL
	if ttl <= 0 {
		ttl = defaultLibrariesCacheTTL
	}

	if time.Since(c.librariesCachedAt) < ttl && c.cachedLibraries != nil {
		libs := make([]Library, len(c.cachedLibraries))
		copy(libs, c.cachedLibraries)
		return libs, nil
	}

	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("context canceled: %w", err)
	}

	req, err := c.newRequest(ctx, http.MethodGet, "/api/libraries", nil)
	if err != nil {
		return nil, err
	}

	status, body, err := c.sendAndReadBody(req)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("get libraries failed with status %d: %s", status, string(body))
	}

	var resp librariesResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("unmarshal libraries response: %w", err)
	}

	c.cachedLibraries = resp.Libraries
	c.librariesCachedAt = time.Now()

	libs := make([]Library, len(c.cachedLibraries))
	copy(libs, c.cachedLibraries)
	return libs, nil
}

func (c *Client) fetchLibraryItems(ctx context.Context, libID string) ([]rawLibraryItemResult, error) {
	c.libraryItemsMu.Lock()
	defer c.libraryItemsMu.Unlock()

	ttl := c.libraryItemsTTL
	if ttl <= 0 {
		ttl = defaultLibraryItemsCacheTTL
	}

	if entry, ok := c.cachedLibraryItems[libID]; ok && time.Since(entry.cachedAt) < ttl {
		items := make([]rawLibraryItemResult, len(entry.items))
		copy(items, entry.items)
		return items, nil
	}

	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("context canceled: %w", err)
	}

	endpoint := fmt.Sprintf("/api/libraries/%s/items", libID)
	req, err := c.newRequest(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}

	status, body, err := c.sendAndReadBody(req)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("get library items for %s failed with status %d: %s", libID, status, string(body))
	}

	var resp libraryItemsResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("unmarshal library items response: %w", err)
	}

	if c.cachedLibraryItems == nil {
		c.cachedLibraryItems = make(map[string]cachedLibraryItemsEntry)
	}
	c.cachedLibraryItems[libID] = cachedLibraryItemsEntry{
		cachedAt: time.Now(),
		items:    resp.Results,
	}

	items := make([]rawLibraryItemResult, len(resp.Results))
	copy(items, resp.Results)
	return items, nil
}

// GetMediaItems retrieves audiobooks or podcasts from all accessible libraries to build client catalog feeds.
func (c *Client) GetMediaItems(ctx context.Context, mediaType string) ([]MediaItem, error) {
	libs, err := c.GetLibraries(ctx)
	if err != nil {
		return nil, err
	}

	byItem, _ := c.fetchProgressLookups(ctx)

	var items []MediaItem
	for _, lib := range libs {
		if mediaType != "" && lib.MediaType != mediaType {
			continue
		}

		rawItems, fetchErr := c.fetchLibraryItems(ctx, lib.ID)
		if fetchErr != nil {
			return nil, fetchErr
		}

		for _, it := range rawItems {
			progress := 0.0
			isFinished := false
			if prog, ok := byItem[it.ID]; ok {
				progress = prog.CurrentTime
				isFinished = prog.IsFinished
				if isFinished && progress <= 0 && it.Media.Duration > 0 {
					progress = it.Media.Duration
				}
			} else if it.UserMediaProgress != nil {
				progress = it.UserMediaProgress.CurrentTime
			}
			items = append(items, MediaItem{
				ID:         it.ID,
				Title:      it.Media.Metadata.Title,
				Author:     it.Media.Metadata.AuthorName,
				Narrator:   it.Media.Metadata.NarratorName,
				MediaType:  lib.MediaType,
				Duration:   it.Media.Duration,
				Progress:   progress,
				CoverURL:   "/api/proxy/covers/" + it.ID,
				IsFinished: isFinished,
			})
		}
	}

	return items, nil
}

// GetCover fetches binary artwork data from Audiobookshelf for proxy caching and delivery.
func (c *Client) GetCover(ctx context.Context, itemID string) (body io.ReadCloser, contentType string, err error) {
	if itemID == "" {
		return nil, "", errors.New("itemID cannot be empty")
	}

	endpoint := "/api/items/" + itemID + "/cover"
	req, err := c.newRequest(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, "", err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("fetch cover request: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		if closeErr := resp.Body.Close(); closeErr != nil {
			log.Printf("close cover body error: %v", closeErr)
		}
		return nil, "", fmt.Errorf("fetch cover failed with status %d", resp.StatusCode)
	}

	return resp.Body, resp.Header.Get("Content-Type"), nil
}

// GetPodcastEpisodes retrieves individual episodes with user progress timestamps for client playlist rendering.
func (c *Client) GetPodcastEpisodes(ctx context.Context, podcastID string) ([]PodcastEpisode, error) {
	if podcastID == "" {
		return nil, errors.New("podcastID cannot be empty")
	}

	endpoint := fmt.Sprintf("/api/items/%s?expanded=1&include=progress", podcastID)
	req, err := c.newRequest(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}

	status, body, err := c.sendAndReadBody(req)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("get podcast episodes for %s failed with status %d: %s", podcastID, status, string(body))
	}

	var resp expandedItemResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("unmarshal podcast episodes response: %w", err)
	}

	progressMap := parseEpisodeProgressMap(resp.UserMediaProgress)

	var episodes []PodcastEpisode
	if len(resp.Media.Episodes) > 0 {
		episodes = make([]PodcastEpisode, 0, len(resp.Media.Episodes))
	}
	for _, ep := range resp.Media.Episodes {
		var publishedAtStr string
		if ep.PublishedAt > 0 {
			ts := ep.PublishedAt
			if ts > 1e11 {
				ts /= 1000
			}
			publishedAtStr = time.Unix(ts, 0).UTC().Format(time.RFC3339)
		}

		duration := ep.AudioFile.Duration
		if duration <= 0 {
			duration = ep.Duration
		}

		progress := 0.0
		isFinished := false
		if epProg, ok := progressMap[ep.ID]; ok {
			progress = epProg.CurrentTime
			isFinished = epProg.IsFinished
			if isFinished && progress <= 0 && duration > 0 {
				progress = duration
			}
		}

		episodes = append(episodes, PodcastEpisode{
			ID:          ep.ID,
			Title:       ep.Title,
			Season:      ep.Season,
			Episode:     ep.Episode,
			PublishedAt: publishedAtStr,
			Duration:    duration,
			Progress:    progress,
			IsFinished:  isFinished,
		})
	}

	return episodes, nil
}

func parseEpisodeProgressMap(raw json.RawMessage) map[string]rawEpisodeProgress {
	progressMap := make(map[string]rawEpisodeProgress)
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return progressMap
	}

	if trimmed[0] == '{' {
		var single rawEpisodeProgress
		if err := json.Unmarshal(trimmed, &single); err == nil && single.EpisodeID != "" {
			progressMap[single.EpisodeID] = single
		}
		return progressMap
	}

	if trimmed[0] == '[' {
		var list []rawEpisodeProgress
		if err := json.Unmarshal(trimmed, &list); err == nil {
			for _, p := range list {
				if p.EpisodeID != "" {
					progressMap[p.EpisodeID] = p
				}
			}
		}
	}

	return progressMap
}

// GetInProgressItems retrieves currently active audiobooks and podcast episodes with user playback positions.
func (c *Client) GetInProgressItems(ctx context.Context) ([]InProgressItem, error) {
	reqItems, err := c.newRequest(ctx, http.MethodGet, "/api/me/items-in-progress", nil)
	if err != nil {
		return nil, err
	}

	status, body, err := c.sendAndReadBody(reqItems)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("get items in progress failed with status %d: %s", status, string(body))
	}

	var inProgResp itemsInProgressResponse
	if unmarshalErr := json.Unmarshal(body, &inProgResp); unmarshalErr != nil {
		return nil, fmt.Errorf("unmarshal items in progress response: %w", unmarshalErr)
	}

	if len(inProgResp.LibraryItems) == 0 {
		return nil, nil
	}

	byItem, byEpisode := c.fetchProgressLookups(ctx)

	items := make([]InProgressItem, len(inProgResp.LibraryItems))
	for i := range inProgResp.LibraryItems {
		items[i] = buildInProgressItem(&inProgResp.LibraryItems[i], byItem, byEpisode)
	}

	return items, nil
}

func (c *Client) fetchProgressLookups(ctx context.Context) (byItem, byEpisode map[string]rawMediaProgressEntry) {
	c.progressMu.Lock()
	defer c.progressMu.Unlock()

	ttl := c.progressTTL
	if ttl <= 0 {
		ttl = defaultProgressCacheTTL
	}

	if time.Since(c.progressCachedAt) < ttl && c.cachedByItem != nil {
		return copyProgressLookups(c.cachedByItem, c.cachedByEpisode)
	}

	if ctx.Err() != nil {
		if c.cachedByItem != nil {
			return copyProgressLookups(c.cachedByItem, c.cachedByEpisode)
		}
		return make(map[string]rawMediaProgressEntry), make(map[string]rawMediaProgressEntry)
	}

	byItem = make(map[string]rawMediaProgressEntry)
	byEpisode = make(map[string]rawMediaProgressEntry)

	reqProg, err := c.newRequest(ctx, http.MethodGet, "/api/me/progress", nil)
	if err != nil {
		return byItem, byEpisode
	}

	pStatus, pBody, pErr := c.sendAndReadBody(reqProg)
	if pErr != nil || pStatus != http.StatusOK {
		return byItem, byEpisode
	}

	var progResp mediaProgressListResponse
	if json.Unmarshal(pBody, &progResp) != nil {
		return byItem, byEpisode
	}

	for _, p := range progResp.MediaProgress {
		if p.EpisodeID != "" {
			byEpisode[p.EpisodeID] = p
		}
		if p.LibraryItemID != "" {
			byItem[p.LibraryItemID] = p
		}
	}

	c.cachedByItem = byItem
	c.cachedByEpisode = byEpisode
	c.progressCachedAt = time.Now()

	return copyProgressLookups(byItem, byEpisode)
}

func copyProgressLookups(srcItem, srcEpisode map[string]rawMediaProgressEntry) (byItem, byEpisode map[string]rawMediaProgressEntry) {
	byItem = make(map[string]rawMediaProgressEntry, len(srcItem))
	maps.Copy(byItem, srcItem)
	byEpisode = make(map[string]rawMediaProgressEntry, len(srcEpisode))
	maps.Copy(byEpisode, srcEpisode)
	return byItem, byEpisode
}

func buildInProgressItem(it *rawInProgressLibraryItem, byItem, byEpisode map[string]rawMediaProgressEntry) InProgressItem {
	author := it.Media.Metadata.AuthorName
	if author == "" {
		author = it.Media.Metadata.Author
	}

	item := InProgressItem{
		ID:        it.ID,
		Title:     it.Media.Metadata.Title,
		Author:    author,
		Narrator:  it.Media.Metadata.NarratorName,
		MediaType: it.MediaType,
		CoverURL:  "/api/proxy/covers/" + it.ID,
		Duration:  it.Media.Duration,
	}

	if it.MediaType == "podcast" && it.RecentEpisode != nil {
		populatePodcastInProgress(&item, it, byItem, byEpisode)
		return item
	}

	if prog, ok := byItem[it.ID]; ok {
		item.CurrentTime = prog.CurrentTime
		item.Progress = prog.CurrentTime
		if prog.Duration > 0 && item.Duration <= 0 {
			item.Duration = prog.Duration
		}
	}

	return item
}

func populatePodcastInProgress(item *InProgressItem, it *rawInProgressLibraryItem, byItem, byEpisode map[string]rawMediaProgressEntry) {
	item.EpisodeID = it.RecentEpisode.ID
	item.EpisodeTitle = it.RecentEpisode.Title
	epDur := it.RecentEpisode.Duration
	if epDur <= 0 {
		epDur = it.RecentEpisode.AudioFile.Duration
	}
	if epDur > 0 {
		item.Duration = epDur
	}

	if prog, ok := byEpisode[item.EpisodeID]; ok {
		item.CurrentTime = prog.CurrentTime
		item.Progress = prog.CurrentTime
		if prog.Duration > 0 && item.Duration <= 0 {
			item.Duration = prog.Duration
		}
		return
	}

	if prog, ok := byItem[it.ID]; ok {
		item.CurrentTime = prog.CurrentTime
		item.Progress = prog.CurrentTime
	}
}

// GetBookChapters retrieves chapter markers and timestamps for a specific audiobook.
func (c *Client) GetBookChapters(ctx context.Context, bookID string) ([]ChapterItem, error) {
	if bookID == "" {
		return nil, errors.New("book ID cannot be empty")
	}

	req, err := c.newRequest(ctx, http.MethodGet, "/api/items/"+url.PathEscape(bookID)+"?expanded=1", nil)
	if err != nil {
		return nil, err
	}

	status, body, err := c.sendAndReadBody(req)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("get book chapters failed with status %d: %s", status, string(body))
	}

	var resp bookExpandedResponse
	if unmarshalErr := json.Unmarshal(body, &resp); unmarshalErr != nil {
		return nil, fmt.Errorf("unmarshal book chapters response: %w", unmarshalErr)
	}

	if len(resp.Media.Chapters) == 0 {
		return nil, nil
	}

	chapters := make([]ChapterItem, len(resp.Media.Chapters))
	for i, ch := range resp.Media.Chapters {
		dur := ch.End - ch.Start
		if dur < 0 {
			dur = 0
		}
		chapters[i] = ChapterItem{
			Title:    ch.Title,
			Start:    ch.Start,
			End:      ch.End,
			Duration: dur,
			ID:       ch.ID,
		}
	}

	return chapters, nil
}
