// Package trackproxy provides an internal loopback HTTP proxy that isolates FFmpeg from upstream network disruptions.
package trackproxy

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/underhax/audiobookshelf-transcoder-proxy/internal/ffmpeg"
	"github.com/underhax/audiobookshelf-transcoder-proxy/internal/session"
	"github.com/underhax/audiobookshelf-transcoder-proxy/internal/validator"
)

const sessionIdleTimeout = 45 * time.Second
const defaultSpoolBytes = 256 * 1024
const progressLogInterval = 1024 * 1024
const defaultMinHealthyDuration = 5 * time.Second
const defaultMinHealthyBytes = 256 * 1024

type sessionState struct {
	token        string
	trackURLs    []string
	lastActivity atomic.Int64
}

// Server coordinates the internal loopback HTTP listener and upstream Audiobookshelf audio proxying.
type Server struct {
	listener           net.Listener
	server             *http.Server
	httpClient         *http.Client
	sessions           sync.Map
	absURL             string
	token              string
	version            string
	retryDelays        []time.Duration
	minHealthyDuration time.Duration
	prebufferBytes     int
	spoolBytes         int
	minHealthyBytes    int
	port               int
	debug              bool
}

// New constructs a Server configured with dedicated upstream HTTP transport settings and exponential backoff intervals.
func New(absURL, token, version string, debug bool) *Server {
	return &Server{
		absURL:  absURL,
		token:   token,
		version: version,
		debug:   debug,
		httpClient: &http.Client{
			Transport: &http.Transport{
				DialContext:         (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 15 * time.Second}).DialContext,
				TLSHandshakeTimeout: 10 * time.Second,
				IdleConnTimeout:     90 * time.Second,
				MaxIdleConns:        20,
				MaxIdleConnsPerHost: 10,
			},
		},
		retryDelays: []time.Duration{
			0,
			1 * time.Second,
			2 * time.Second,
			4 * time.Second,
			8 * time.Second,
			16 * time.Second,
			32 * time.Second,
		},
		prebufferBytes:     DefaultPrebufferBytes,
		spoolBytes:         defaultSpoolBytes,
		minHealthyDuration: defaultMinHealthyDuration,
		minHealthyBytes:    defaultMinHealthyBytes,
	}
}

func defaultNetListen(ctx context.Context, network, address string) (net.Listener, error) {
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, network, address)
	if err != nil {
		return nil, fmt.Errorf("net listen: %w", err)
	}
	return ln, nil
}

func defaultGenerateToken() (string, error) {
	token, err := session.GenerateToken()
	if err != nil {
		return "", fmt.Errorf("generate session token: %w", err)
	}
	return token, nil
}

var (
	netListenMu     sync.RWMutex
	netListen       = defaultNetListen
	generateTokenMu sync.RWMutex
	generateToken   = defaultGenerateToken
)

func callNetListen(ctx context.Context, network, address string) (net.Listener, error) {
	netListenMu.RLock()
	fn := netListen
	netListenMu.RUnlock()
	return fn(ctx, network, address)
}

func callGenerateToken() (string, error) {
	generateTokenMu.RLock()
	fn := generateToken
	generateTokenMu.RUnlock()
	return fn()
}

// SetNetListen overrides the network listener constructor for testing.
func SetNetListen(fn func(ctx context.Context, network, address string) (net.Listener, error)) func() {
	netListenMu.Lock()
	orig := netListen
	netListen = fn
	netListenMu.Unlock()
	return func() {
		netListenMu.Lock()
		netListen = orig
		netListenMu.Unlock()
	}
}

// SetGenerateToken overrides token generation logic for testing.
func SetGenerateToken(fn func() (string, error)) func() {
	generateTokenMu.Lock()
	orig := generateToken
	generateToken = fn
	generateTokenMu.Unlock()
	return func() {
		generateTokenMu.Lock()
		generateToken = orig
		generateTokenMu.Unlock()
	}
}

// Start binds an OS-assigned port on loopback 127.0.0.1 and begins serving requests asynchronously.
func (s *Server) Start() (int, error) {
	ln, err := callNetListen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		return 0, fmt.Errorf("bind loopback listener: %w", err)
	}
	s.listener = ln

	tcpAddr, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		if closeErr := ln.Close(); closeErr != nil {
			log.Printf("close loopback listener error: %v", closeErr)
		}
		return 0, errors.New("failed to cast loopback listener address to TCPAddr")
	}
	s.port = tcpAddr.Port

	mux := http.NewServeMux()
	mux.HandleFunc("GET /track/{session_id}/{track_index}", s.handleTrack)

	s.server = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	go func() {
		if serveErr := s.server.Serve(ln); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			log.Printf("trackproxy serve error: %v", serveErr)
		}
	}()

	return s.port, nil
}

// Port returns the TCP port bound by the loopback listener.
func (s *Server) Port() int {
	return s.port
}

// RegisterSession associates a session identifier with an ordered slice of audio track content URLs
// and returns a cryptographically secure token required to access them.
func (s *Server) RegisterSession(sessionID string, trackURLs []string) (string, error) {
	token, err := callGenerateToken()
	if err != nil {
		return "", fmt.Errorf("generate session token: %w", err)
	}

	state := &sessionState{
		trackURLs: slices.Clone(trackURLs),
		token:     token,
	}
	state.lastActivity.Store(time.Now().UnixNano())
	s.sessions.Store(sessionID, state)

	return token, nil
}

// UnregisterSession removes session state and tracked audio URLs upon stream termination.
func (s *Server) UnregisterSession(sessionID string) {
	s.sessions.Delete(sessionID)
}

// Shutdown gracefully terminates the loopback HTTP server and closes active connections.
func (s *Server) Shutdown(ctx context.Context) error {
	if s == nil || s.server == nil {
		return nil
	}
	if err := s.server.Shutdown(ctx); err != nil {
		return fmt.Errorf("shutdown track proxy server: %w", err)
	}
	return nil
}

type rangeHeader struct {
	raw   string
	end   string
	start int64
	has   bool
}

func parseRange(header string) rangeHeader {
	if !strings.HasPrefix(header, "bytes=") {
		return rangeHeader{}
	}
	raw := strings.TrimPrefix(header, "bytes=")
	parts := strings.SplitN(raw, "-", 2)
	if len(parts) != 2 {
		return rangeHeader{}
	}
	if parts[0] == "" {
		if _, err := strconv.ParseInt(parts[1], 10, 64); err != nil {
			return rangeHeader{}
		}
		return rangeHeader{
			raw: header,
			has: true,
		}
	}
	start, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return rangeHeader{}
	}
	return rangeHeader{
		raw:   header,
		start: start,
		end:   parts[1],
		has:   true,
	}
}

func (s *Server) resolveUpstreamURL(trackContentURL string) (string, error) {
	baseParsed, err := url.Parse(s.absURL)
	if err != nil {
		return "", fmt.Errorf("parse abs url: %w", err)
	}

	raw := ffmpeg.BuildMediaURL(s.absURL, trackContentURL)
	targetParsed, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("parse upstream url: %w", err)
	}

	if valErr := validator.ValidateHTTPURL(raw); valErr != nil {
		return "", fmt.Errorf("validate media url: %w", valErr)
	}

	if targetParsed.Host != baseParsed.Host || targetParsed.Scheme != baseParsed.Scheme {
		return "", errors.New("upstream host or scheme mismatch")
	}

	cleanURL := url.URL{
		Scheme:   baseParsed.Scheme,
		Host:     baseParsed.Host,
		Path:     targetParsed.Path,
		RawQuery: targetParsed.RawQuery,
	}
	return cleanURL.String(), nil
}

func (s *Server) getActiveSession(r *http.Request, sessionID string) (state *sessionState, statusCode int, errMsg string) {
	ua := r.UserAgent()
	if !strings.HasPrefix(ua, "abstp") && !strings.HasPrefix(ua, "Lavf") {
		return nil, http.StatusForbidden, "forbidden"
	}

	val, ok := s.sessions.Load(sessionID)
	if !ok {
		return nil, http.StatusNotFound, "session not found"
	}

	state, ok = val.(*sessionState)
	if !ok {
		return nil, http.StatusInternalServerError, "invalid session state"
	}

	last := time.Unix(0, state.lastActivity.Load())
	if time.Since(last) > sessionIdleTimeout {
		s.sessions.Delete(sessionID)
		return nil, http.StatusUnauthorized, "session expired"
	}

	reqToken := r.URL.Query().Get("token")
	if reqToken == "" || subtle.ConstantTimeCompare([]byte(reqToken), []byte(state.token)) != 1 {
		return nil, http.StatusUnauthorized, "unauthorized"
	}

	state.lastActivity.Store(time.Now().UnixNano())
	return state, 0, ""
}

func (s *Server) resolveTrack(trackURLs []string, trackIdxStr string) (upstreamURL string, statusCode int, errMsg string) {
	idx, err := strconv.Atoi(trackIdxStr)
	if err != nil || idx < 0 || idx >= len(trackURLs) {
		return "", http.StatusBadRequest, "invalid track index"
	}

	var trackContentURL string
	for i, u := range trackURLs {
		if i == idx {
			trackContentURL = u
			break
		}
	}
	if trackContentURL == "" {
		return "", http.StatusNotFound, "track not found"
	}

	upstreamURL, err = s.resolveUpstreamURL(trackContentURL)
	if err != nil {
		return "", http.StatusBadRequest, "invalid upstream url"
	}

	return upstreamURL, 0, ""
}

func (s *Server) handleTrack(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("session_id")
	trackIdxStr := r.PathValue("track_index")

	if s.debug {
		log.Println(strings.ReplaceAll(strings.ReplaceAll(fmt.Sprintf("[DEBUG] trackproxy: %s /track/%s/%s from %s, Range=%q, UA=%q", r.Method, sessionID, trackIdxStr, r.RemoteAddr, r.Header.Get("Range"), r.UserAgent()), "\n", " "), "\r", ""))
	}

	state, status, errMsg := s.getActiveSession(r, sessionID)
	if status != 0 {
		http.Error(w, fmt.Sprintf(`{"error":%q}`, errMsg), status)
		return
	}

	upstreamURL, status, errMsg := s.resolveTrack(state.trackURLs, trackIdxStr)
	if status != 0 {
		http.Error(w, fmt.Sprintf(`{"error":%q}`, errMsg), status)
		return
	}

	if s.debug {
		log.Println(strings.ReplaceAll(strings.ReplaceAll(fmt.Sprintf("[DEBUG] trackproxy: resolved upstream URL %s for track index %s", upstreamURL, trackIdxStr), "\n", " "), "\r", ""))
	}

	initRange := parseRange(r.Header.Get("Range"))
	s.proxyWithRetry(r.Context(), w, upstreamURL, initRange, &state.lastActivity)
}

func (s *Server) newUpstreamRequest(ctx context.Context, targetURL string, currentStart int64, initRange rangeHeader, hasBytes bool) (*http.Request, error) {
	parsed, err := url.Parse(targetURL)
	if err != nil {
		return nil, fmt.Errorf("parse target url: %w", err)
	}

	baseParsed, err := url.Parse(s.absURL)
	if err != nil {
		return nil, fmt.Errorf("parse base url: %w", err)
	}

	if parsed.Host != baseParsed.Host {
		return nil, errors.New("unauthorized upstream host")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.absURL, http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("create upstream request: %w", err)
	}
	req.URL.Path = parsed.Path
	req.URL.RawQuery = parsed.RawQuery

	req.Header.Set("Authorization", "Bearer "+s.token)
	userAgent := "abstp"
	if s.version != "" {
		userAgent = "abstp/" + s.version
	}
	req.Header.Set("User-Agent", userAgent)

	if hasBytes {
		if initRange.end != "" {
			req.Header.Set("Range", fmt.Sprintf("bytes=%d-%s", currentStart, initRange.end))
		} else {
			req.Header.Set("Range", fmt.Sprintf("bytes=%d-", currentStart))
		}
	} else if initRange.has {
		req.Header.Set("Range", initRange.raw)
	}

	return req, nil
}

func (s *Server) commitInitialHeaders(w http.ResponseWriter, resp *http.Response, flusher http.Flusher, bytesWritten *int64, headersSent *bool, lastActivity *atomic.Int64) (committed, completed bool, spooledBytes int64, err error) {
	spooled, eof, spoolErr := readSpool(resp.Body, s.spoolBytes)
	if spoolErr != nil {
		log.Println(strings.ReplaceAll(strings.ReplaceAll(fmt.Sprintf("[WARN] trackproxy: upstream spool read failed: %v", spoolErr), "\n", " "), "\r", ""))
		return false, false, 0, nil
	}

	if s.debug {
		log.Println(strings.ReplaceAll(strings.ReplaceAll(fmt.Sprintf("[DEBUG] trackproxy: spool read: bytes=%d, eof=%v, contentLength=%d", len(spooled), eof, resp.ContentLength), "\n", " "), "\r", ""))
	}

	if eof && (resp.ContentLength < 0 || int64(len(spooled)) == resp.ContentLength) {
		if s.debug {
			log.Println(strings.ReplaceAll(strings.ReplaceAll("[DEBUG] trackproxy: full body captured during spool, committing response", "\n", " "), "\r", ""))
		}
		forwardHeaders(w, resp)
		w.WriteHeader(resp.StatusCode)
		*headersSent = true
		if writeErr := writeBodyChunk(w, spooled, flusher, bytesWritten, lastActivity); writeErr != nil {
			return true, false, int64(len(spooled)), writeErr
		}
		return true, true, int64(len(spooled)), nil
	}

	if eof {
		log.Println(strings.ReplaceAll(strings.ReplaceAll(fmt.Sprintf("[WARN] trackproxy: upstream stream ended at %d bytes, expected %d, retrying without commit", len(spooled), resp.ContentLength), "\n", " "), "\r", ""))
		return false, false, int64(len(spooled)), nil
	}

	if s.debug {
		log.Println(strings.ReplaceAll(strings.ReplaceAll(fmt.Sprintf("[DEBUG] trackproxy: partial spool captured %d bytes, committing headers and streaming remainder", len(spooled)), "\n", " "), "\r", ""))
	}
	forwardHeaders(w, resp)
	w.WriteHeader(resp.StatusCode)
	*headersSent = true
	if writeErr := writeBodyChunk(w, spooled, flusher, bytesWritten, lastActivity); writeErr != nil {
		return true, false, int64(len(spooled)), writeErr
	}
	return true, false, int64(len(spooled)), nil
}

func (s *Server) isAttemptHealthy(duration time.Duration, bytes int64) bool {
	if s.minHealthyDuration > 0 && duration >= s.minHealthyDuration && bytes > 0 {
		return true
	}
	return s.minHealthyBytes > 0 && bytes >= int64(s.minHealthyBytes)
}

func waitRetryDelay(ctx context.Context, delay time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(delay):
		return true
	}
}

func (s *Server) handleRetryFailure(ctx context.Context, consecutiveFailures *int, bytesTransferred int64) bool {
	if *consecutiveFailures >= len(s.retryDelays) {
		if s.debug {
			log.Println(strings.ReplaceAll(strings.ReplaceAll(fmt.Sprintf("[WARN] trackproxy: max retry attempts (%d) reached without progress, aborting", len(s.retryDelays)), "\n", " "), "\r", ""))
		}
		return false
	}

	delay := s.retryDelays[*consecutiveFailures]
	*consecutiveFailures++

	if s.debug {
		log.Println(strings.ReplaceAll(strings.ReplaceAll(fmt.Sprintf("[DEBUG] trackproxy: retry %d/%d after %v delay (bytes written: %d)", *consecutiveFailures, len(s.retryDelays), delay, bytesTransferred), "\n", " "), "\r", ""))
	}

	return waitRetryDelay(ctx, delay)
}

func closeBody(body io.Closer) {
	if err := body.Close(); err != nil {
		log.Printf("close body error: %v", err)
	}
}

func (s *Server) waitOrAbort(ctx context.Context, consecutiveFailures *int, bytesTransferred int64) error {
	if !s.handleRetryFailure(ctx, consecutiveFailures, bytesTransferred) {
		if ctx.Err() != nil {
			return fmt.Errorf("context cancelled during retry: %w", ctx.Err())
		}
		return errors.New("retries exhausted")
	}
	return nil
}

func (s *Server) requestInitial(ctx context.Context, upstreamURL string, initRange rangeHeader, headersSent bool) (*http.Response, error) {
	req, err := s.newUpstreamRequest(ctx, upstreamURL, initRange.start, initRange, false)
	if err != nil {
		return nil, fmt.Errorf("create initial upstream request: %w", err)
	}

	if s.debug {
		log.Println(strings.ReplaceAll(strings.ReplaceAll(fmt.Sprintf("[DEBUG] trackproxy: upstream request GET %s range=%q hasBytes=false", req.URL.String(), initRange.raw), "\n", " "), "\r", ""))
	}

	resp, err := s.httpClient.Do(req)
	if err != nil {
		log.Println(strings.ReplaceAll(strings.ReplaceAll(fmt.Sprintf("[WARN] trackproxy: upstream request failed: %v", err), "\n", " "), "\r", ""))
		return nil, fmt.Errorf("do upstream initial: %w", err)
	}

	if s.debug {
		log.Println(strings.ReplaceAll(strings.ReplaceAll(fmt.Sprintf("[DEBUG] trackproxy: upstream response: status=%d, Content-Length=%d, Content-Range=%q, headersSent=%v", resp.StatusCode, resp.ContentLength, resp.Header.Get("Content-Range"), headersSent), "\n", " "), "\r", ""))
	}

	if resp.StatusCode >= http.StatusInternalServerError {
		log.Println(strings.ReplaceAll(strings.ReplaceAll(fmt.Sprintf("[WARN] trackproxy: upstream returned server error status %d", resp.StatusCode), "\n", " "), "\r", ""))
		closeBody(resp.Body)
		return nil, errors.New("upstream server error")
	}

	return resp, nil
}

func (s *Server) waitOrAbortConnect(ctx context.Context, consecutiveFailures *int, bytesWritten int64) (bool, error) {
	if retryErr := s.waitOrAbort(ctx, consecutiveFailures, bytesWritten); retryErr != nil {
		if ctx.Err() != nil {
			return false, retryErr
		}
		return false, nil
	}
	return true, nil
}

func (s *Server) connectInitial(ctx context.Context, w http.ResponseWriter, upstreamURL string, initRange rangeHeader, flusher http.Flusher, bytesWritten *int64, headersSent *bool, lastActivity *atomic.Int64) (body io.ReadCloser, expectedLen int64, completed bool, err error) {
	consecutiveFailures := 0
	for {
		resp, err := s.requestInitial(ctx, upstreamURL, initRange, *headersSent)
		if err != nil {
			retry, retryErr := s.waitOrAbortConnect(ctx, &consecutiveFailures, *bytesWritten)
			if !retry {
				return nil, 0, false, retryErr
			}
			continue
		}

		committed, completed, spooledBytes, commitErr := s.commitInitialHeaders(w, resp, flusher, bytesWritten, headersSent, lastActivity)
		if commitErr != nil {
			closeBody(resp.Body)
			return nil, 0, false, commitErr
		}
		if !committed {
			closeBody(resp.Body)
			retry, retryErr := s.waitOrAbortConnect(ctx, &consecutiveFailures, *bytesWritten)
			if !retry {
				return nil, 0, false, retryErr
			}
			continue
		}
		if completed {
			closeBody(resp.Body)
			return nil, 0, true, nil
		}

		expectedLen := int64(-1)
		if resp.ContentLength >= 0 {
			expectedLen = resp.ContentLength - spooledBytes
		}
		return resp.Body, expectedLen, false, nil
	}
}

func (s *Server) pumpBodyToBuffer(ctx context.Context, buf *readAheadBuffer, body io.Reader, bytesFetched *int64, expectedLength int64) (bool, error) {
	attemptStart := *bytesFetched
	lastLogged := attemptStart
	tmp := make([]byte, 32*1024)
	if s.debug {
		log.Println(strings.ReplaceAll(strings.ReplaceAll(fmt.Sprintf("[DEBUG] trackproxy: stream start: attemptStart=%d, expectedLength=%d", attemptStart, expectedLength), "\n", " "), "\r", ""))
	}
	for {
		select {
		case <-ctx.Done():
			return false, fmt.Errorf("context cancelled during stream: %w", ctx.Err())
		default:
		}

		nr, readErr := body.Read(tmp)
		if nr > 0 {
			nw, writeErr := buf.Write(tmp[:nr])
			*bytesFetched += int64(nw)
			if writeErr != nil {
				return false, writeErr
			}
		}
		if s.debug && nr > 0 && *bytesFetched-lastLogged >= progressLogInterval {
			log.Println(strings.ReplaceAll(strings.ReplaceAll(fmt.Sprintf("[DEBUG] trackproxy: stream progress: attemptBytes=%d, totalBytes=%d", *bytesFetched-attemptStart, *bytesFetched), "\n", " "), "\r", ""))
			lastLogged = *bytesFetched
		}
		if readErr == nil {
			continue
		}
		if !errors.Is(readErr, io.EOF) {
			log.Println(strings.ReplaceAll(strings.ReplaceAll(fmt.Sprintf("[WARN] trackproxy: upstream stream read error: %v", readErr), "\n", " "), "\r", ""))
			return false, nil
		}
		if expectedLength >= 0 && *bytesFetched-attemptStart != expectedLength {
			log.Println(strings.ReplaceAll(strings.ReplaceAll(fmt.Sprintf("[WARN] trackproxy: upstream stream ended at %d bytes, expected %d", *bytesFetched-attemptStart, expectedLength), "\n", " "), "\r", ""))
			return false, nil
		}
		if s.debug {
			log.Println(strings.ReplaceAll(strings.ReplaceAll(fmt.Sprintf("[DEBUG] trackproxy: stream complete: attemptBytes=%d, expectedLength=%d", *bytesFetched-attemptStart, expectedLength), "\n", " "), "\r", ""))
		}
		return true, nil
	}
}

func (s *Server) resumeUpstream(ctx context.Context, upstreamURL string, initRange rangeHeader, bytesFetched int64) (io.ReadCloser, int64, error) {
	currentStart := initRange.start + bytesFetched
	if s.debug && bytesFetched > 0 {
		log.Println(strings.ReplaceAll(strings.ReplaceAll(fmt.Sprintf("[DEBUG] trackproxy: resuming upstream stream from byte offset %d", currentStart), "\n", " "), "\r", ""))
	}

	req, err := s.newUpstreamRequest(ctx, upstreamURL, currentStart, initRange, bytesFetched > 0)
	if err != nil {
		return nil, 0, fmt.Errorf("create resume request: %w", err)
	}

	if s.debug {
		log.Println(strings.ReplaceAll(strings.ReplaceAll(fmt.Sprintf("[DEBUG] trackproxy: upstream request GET %s range=%q hasBytes=%v", req.URL.String(), initRange.raw, bytesFetched > 0), "\n", " "), "\r", ""))
	}

	newResp, err := s.httpClient.Do(req)
	if err != nil {
		log.Println(strings.ReplaceAll(strings.ReplaceAll(fmt.Sprintf("[WARN] trackproxy: upstream request failed: %v", err), "\n", " "), "\r", ""))
		return nil, 0, fmt.Errorf("do upstream resume: %w", err)
	}

	if s.debug {
		log.Println(strings.ReplaceAll(strings.ReplaceAll(fmt.Sprintf("[DEBUG] trackproxy: upstream response: status=%d, Content-Length=%d, Content-Range=%q, headersSent=true", newResp.StatusCode, newResp.ContentLength, newResp.Header.Get("Content-Range")), "\n", " "), "\r", ""))
	}

	if newResp.StatusCode != http.StatusPartialContent {
		log.Println(strings.ReplaceAll(strings.ReplaceAll(fmt.Sprintf("[WARN] trackproxy: upstream returned status %d on resume (expected 206 Partial Content)", newResp.StatusCode), "\n", " "), "\r", ""))
		closeBody(newResp.Body)
		return nil, 0, errors.New("upstream resume not partial content")
	}

	return newResp.Body, newResp.ContentLength, nil
}

func (s *Server) pumpStream(ctx context.Context, buf *readAheadBuffer, body io.ReadCloser, expectedLength int64, bytesFetched *int64, attemptStart time.Time, consecutiveFailures *int) bool {
	startBytes := *bytesFetched
	completed, fetchErr := s.pumpBodyToBuffer(ctx, buf, body, bytesFetched, expectedLength)
	closeBody(body)

	if s.debug {
		log.Println(strings.ReplaceAll(strings.ReplaceAll(fmt.Sprintf("[DEBUG] trackproxy: attempt finished: completed=%v, clientErr=%v, bytesWritten=%d", completed, fetchErr, *bytesFetched), "\n", " "), "\r", ""))
	}

	if completed {
		buf.CloseWithError(io.EOF)
		return true
	}
	if fetchErr != nil && errors.Is(fetchErr, errBufferClosed) {
		return true
	}

	attemptBytes := *bytesFetched - startBytes
	if s.isAttemptHealthy(time.Since(attemptStart), attemptBytes) {
		*consecutiveFailures = 0
	}

	return false
}

func (s *Server) fetchToBuffer(ctx context.Context, buf *readAheadBuffer, upstreamURL string, initRange rangeHeader, initialBody io.ReadCloser, initialExpected, initialBytes int64, attemptStart time.Time) {
	bytesFetched := initialBytes
	consecutiveFailures := 0

	if s.pumpStream(ctx, buf, initialBody, initialExpected, &bytesFetched, attemptStart, &consecutiveFailures) {
		return
	}

	for {
		if !s.handleRetryFailure(ctx, &consecutiveFailures, bytesFetched) {
			buf.CloseWithError(errors.New("upstream stream aborted: max retries reached"))
			return
		}

		attemptStart = time.Now()
		newBody, newExpected, err := s.resumeUpstream(ctx, upstreamURL, initRange, bytesFetched)
		if err != nil {
			continue
		}

		if s.pumpStream(ctx, buf, newBody, newExpected, &bytesFetched, attemptStart, &consecutiveFailures) {
			return
		}
	}
}

func (s *Server) streamFromBuffer(ctx context.Context, w http.ResponseWriter, buf *readAheadBuffer, flusher http.Flusher, bytesWritten *int64, lastActivity *atomic.Int64) error {
	tmp := make([]byte, 32*1024)
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("context cancelled during playback: %w", ctx.Err())
		default:
		}

		nr, readErr := buf.Read(tmp)
		if nr > 0 {
			if writeErr := writeBodyChunk(w, tmp[:nr], flusher, bytesWritten, lastActivity); writeErr != nil {
				return writeErr
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return nil
			}
			return readErr
		}
	}
}

func (s *Server) proxyWithRetry(ctx context.Context, w http.ResponseWriter, upstreamURL string, initRange rangeHeader, lastActivity *atomic.Int64) {
	var flusher http.Flusher
	if f, ok := w.(http.Flusher); ok {
		flusher = f
	}

	var bytesWritten int64
	headersSent := false

	if s.debug {
		log.Println(strings.ReplaceAll(strings.ReplaceAll(fmt.Sprintf("[DEBUG] trackproxy: proxy start: range=%q", initRange.raw), "\n", " "), "\r", ""))
	}
	defer func() {
		if s.debug {
			log.Println(strings.ReplaceAll(strings.ReplaceAll(fmt.Sprintf("[DEBUG] trackproxy: proxy end: bytesWritten=%d, headersSent=%v", bytesWritten, headersSent), "\n", " "), "\r", ""))
		}
	}()

	attemptStart := time.Now()
	body, expectedLen, completed, err := s.connectInitial(ctx, w, upstreamURL, initRange, flusher, &bytesWritten, &headersSent, lastActivity)
	if err != nil || completed {
		return
	}
	if !headersSent || body == nil {
		http.Error(w, `{"error":"upstream connection failed"}`, http.StatusBadGateway)
		return
	}

	buf := newReadAheadBuffer(s.prebufferBytes)
	fetchCtx, fetchCancel := context.WithCancel(ctx)
	defer fetchCancel()

	fetchDone := make(chan struct{})
	go func() {
		defer close(fetchDone)
		s.fetchToBuffer(fetchCtx, buf, upstreamURL, initRange, body, expectedLen, bytesWritten, attemptStart)
	}()

	if streamErr := s.streamFromBuffer(ctx, w, buf, flusher, &bytesWritten, lastActivity); streamErr != nil && s.debug {
		log.Println(strings.ReplaceAll(strings.ReplaceAll(fmt.Sprintf("[DEBUG] trackproxy: stream error: %v", streamErr), "\n", " "), "\r", ""))
	}
	buf.CloseWithError(errBufferClosed)
	fetchCancel()
	<-fetchDone
}

func forwardHeaders(w http.ResponseWriter, resp *http.Response) {
	for _, h := range []string{"Content-Type", "Content-Range", "Accept-Ranges"} {
		if v := resp.Header.Get(h); v != "" {
			w.Header().Set(h, v)
		}
	}
	if resp.ContentLength >= 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(resp.ContentLength, 10))
	} else if cl := resp.Header.Get("Content-Length"); cl != "" {
		w.Header().Set("Content-Length", cl)
	}
}

func writeBodyChunk(w http.ResponseWriter, data []byte, flusher http.Flusher, bytesWritten *int64, lastActivity *atomic.Int64) error {
	if len(data) == 0 {
		return nil
	}
	nw, writeErr := w.Write(data)
	*bytesWritten += int64(nw)
	if lastActivity != nil {
		lastActivity.Store(time.Now().UnixNano())
	}
	if flusher != nil {
		flusher.Flush()
	}
	if writeErr != nil {
		return fmt.Errorf("write response chunk: %w", writeErr)
	}
	return nil
}

func readSpool(body io.Reader, spoolSize int) (spooled []byte, eof bool, err error) {
	if spoolSize <= 0 {
		return nil, false, errors.New("invalid spool size")
	}
	buf := make([]byte, spoolSize)
	total := 0
	for total < spoolSize {
		n, readErr := body.Read(buf[total:])
		if n > 0 {
			total += n
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return buf[:total], true, nil
			}
			return nil, false, fmt.Errorf("spool read: %w", readErr)
		}
		if n == 0 {
			return nil, false, errors.New("empty read without error")
		}
	}
	return buf, false, nil
}
