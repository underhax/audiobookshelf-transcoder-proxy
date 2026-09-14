package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/underhax/audiobookshelf-transcoder-proxy/internal/absclient"
	"github.com/underhax/audiobookshelf-transcoder-proxy/internal/ffmpeg"
	"github.com/underhax/audiobookshelf-transcoder-proxy/internal/ratelimit"
	"github.com/underhax/audiobookshelf-transcoder-proxy/internal/session"
	"github.com/underhax/audiobookshelf-transcoder-proxy/internal/trackproxy"
)

// StartSessionRequest encapsulates client playback parameters including item identifiers, requested speed, and start position.
type StartSessionRequest struct {
	ItemID       string  `json:"itemId"`
	SnakeItemID  string  `json:"item_id"`
	EpisodeID    string  `json:"episodeId,omitempty"`
	SnakeEpID    string  `json:"episode_id,omitempty"`
	Title        string  `json:"title,omitempty"`
	Author       string  `json:"author,omitempty"`
	Narrator     string  `json:"narrator,omitempty"`
	EpisodeTitle string  `json:"episodeTitle,omitempty"`
	SnakeEpTitle string  `json:"episode_title,omitempty"`
	MediaType    string  `json:"mediaType,omitempty"`
	SnakeMedType string  `json:"media_type,omitempty"`
	CurrentTime  float64 `json:"currentTime"`
	SnakeTime    float64 `json:"current_time"`
	Duration     float64 `json:"duration"`
	Speed        float64 `json:"speed"`
}

// StartSessionResponse delivers the single-use streaming URL and session metadata required for client audio playback.
type StartSessionResponse struct {
	SessionID   string  `json:"session_id"`
	StreamURL   string  `json:"stream_url"`
	CurrentTime float64 `json:"current_time"`
	Duration    float64 `json:"duration"`
}

// SessionActionRequest conveys session termination and synchronization commands initiated by clients.
type SessionActionRequest struct {
	SessionID      string `json:"sessionId"`
	SnakeSessionID string `json:"session_id"`
}

// HandleSessionStart creates a playback session on Audiobookshelf and prepares a proxy session.
func (h *Handler) HandleSessionStart(w http.ResponseWriter, r *http.Request) {
	var req StartSessionRequest
	bodyReader := io.LimitReader(r.Body, maxRequestBodySize)
	if err := json.NewDecoder(bodyReader).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
		return
	}

	normalizeSessionRequest(&req)

	if req.ItemID == "" {
		http.Error(w, `{"error":"itemId is required"}`, http.StatusBadRequest)
		return
	}

	playResp, err := h.absClient.StartSession(r.Context(), req.ItemID, req.EpisodeID)
	if err != nil {
		slog.Error("start abs session failed", "error", err)
		http.Error(w, `{"error":"failed to start abs session"}`, http.StatusBadGateway)
		return
	}

	externalBase, err := h.resolveExternalBaseURL(r)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusBadRequest)
		return
	}

	currentTime := req.CurrentTime
	if currentTime <= 0 {
		currentTime = playResp.CurrentTime
	}

	duration := req.Duration
	if duration <= 0 {
		duration = playResp.Duration
	}

	seekOffset, trackIdx := calculateSeekOffset(playResp.AudioTracks, currentTime)
	meta := resolveSessionMetadata(&req, playResp)

	coverItem := req.ItemID
	if playResp.LibraryItemID != "" {
		coverItem = playResp.LibraryItemID
	}

	sess := &session.Session{
		ID:                 playResp.ID,
		ItemID:             coverItem,
		EpisodeID:          req.EpisodeID,
		Title:              meta.title,
		Author:             meta.author,
		Narrator:           meta.narrator,
		EpisodeTitle:       meta.episodeTitle,
		MediaType:          meta.mediaType,
		CoverURL:           fmt.Sprintf("%s/api/proxy/covers/%s", externalBase, coverItem),
		AudioTracks:        playResp.AudioTracks,
		StartingTrackIndex: trackIdx,
		SeekOffset:         seekOffset,
		CurrentTime:        currentTime,
		Duration:           duration,
		Speed:              req.Speed,
	}
	sess.SetLastSyncPosition(currentTime)

	token, err := h.store.Create(sess)
	if err != nil {
		slog.Error("store session error", "error", err)
		http.Error(w, `{"error":"failed to generate session"}`, http.StatusInternalServerError)
		return
	}

	streamURL := fmt.Sprintf("%s/stream/%s.aac?token=%s", externalBase, sess.ID, token)

	slog.Debug("session created", "id", sess.ID, "item_id", sess.ItemID, "stream_url", streamURL)

	respData := StartSessionResponse{
		SessionID:   sess.ID,
		StreamURL:   streamURL,
		CurrentTime: sess.CurrentTime,
		Duration:    sess.Duration,
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(respData); err != nil {
		slog.Error("encode session start response error", "error", err)
	}
}

func calculateSeekOffset(tracks []absclient.AudioTrack, currentTime float64) (seekOffset float64, trackIndex int) {
	accumulated := 0.0
	for i, track := range tracks {
		if currentTime < accumulated+track.Duration {
			return currentTime - accumulated, i
		}
		accumulated += track.Duration
	}
	if len(tracks) > 0 {
		return 0, len(tracks) - 1
	}
	return currentTime, 0
}

func defaultGenerateConcat(baseURL string, trackURLs []string, inpoint float64) (string, error) {
	path, err := ffmpeg.GenerateConcatFile(baseURL, trackURLs, inpoint)
	if err != nil {
		return "", fmt.Errorf("generate concat: %w", err)
	}
	return path, nil
}

var generateConcat = defaultGenerateConcat

func prepareInput(proxyPort int, sessionID, proxyToken string, tracks []absclient.AudioTrack, startIdx int, seekOffset float64) (inputPath string, cleanup func(), err error) {
	if len(tracks) == 0 {
		return "", func() {}, errors.New("no audio tracks available")
	}

	trackURLs := make([]string, 0, len(tracks)-startIdx)
	for i := startIdx; i < len(tracks); i++ {
		trackURLs = append(trackURLs, fmt.Sprintf("http://127.0.0.1:%d/track/%s/%d?token=%s", proxyPort, sessionID, i, proxyToken))
	}

	concatPath, err := generateConcat("", trackURLs, seekOffset)
	if err != nil {
		return "", nil, fmt.Errorf("generate concat file: %w", err)
	}

	cleanup = func() {
		cleanPath := filepath.Clean(concatPath)
		if rmErr := os.Remove(cleanPath); rmErr != nil && !errors.Is(rmErr, os.ErrNotExist) {
			slog.Debug("remove concat file error", "error", rmErr)
		}
	}

	return concatPath, cleanup, nil
}

func defaultTrackProxyRegisterSession(tp *trackproxy.Server, sessionID string, trackURLs []string) (string, error) {
	token, err := tp.RegisterSession(sessionID, trackURLs)
	if err != nil {
		return "", fmt.Errorf("track proxy register: %w", err)
	}
	return token, nil
}

var (
	trackProxyRegisterSessionMu sync.RWMutex
	trackProxyRegisterSession   = defaultTrackProxyRegisterSession
)

func callTrackProxyRegisterSession(tp *trackproxy.Server, sessionID string, trackURLs []string) (string, error) {
	trackProxyRegisterSessionMu.RLock()
	fn := trackProxyRegisterSession
	trackProxyRegisterSessionMu.RUnlock()
	return fn(tp, sessionID, trackURLs)
}

func (h *Handler) registerTrackProxySession(sessionID string, tracks []absclient.AudioTrack) (port int, proxyToken string, cleanup func(), err error) {
	if h.trackProxy == nil {
		return 0, "", func() {}, nil
	}
	trackURLs := make([]string, len(tracks))
	for i, t := range tracks {
		trackURLs[i] = t.ContentURL
	}
	token, err := callTrackProxyRegisterSession(h.trackProxy, sessionID, trackURLs)
	if err != nil {
		return 0, "", func() {}, fmt.Errorf("register track proxy session: %w", err)
	}
	return h.trackProxy.Port(), token, func() {
		h.trackProxy.UnregisterSession(sessionID)
	}, nil
}

func (h *Handler) buildFFmpegParams(inputPath string, sess *session.Session) ffmpeg.Params {
	return ffmpeg.Params{
		FFmpegPath: h.cfg.FFmpegPath,
		InputPath:  inputPath,
		Speed:      sess.Speed,
	}
}

// HandleStream handles the client audio stream GET and HEAD requests.
func (h *Handler) HandleStream(w http.ResponseWriter, r *http.Request) {
	rawSessionID := r.PathValue("session_id")
	sessionID := strings.TrimSuffix(strings.TrimSuffix(rawSessionID, ".aac"), ".mp3")
	token := r.URL.Query().Get("token")

	slog.Debug("incoming stream request", "method", r.Method, "path", r.URL.Path, "remote_addr", r.RemoteAddr, "user_agent", r.UserAgent())

	sess, ok := h.validateStreamRequest(w, r, sessionID, token)
	if !ok {
		return
	}

	w.Header().Set("Content-Type", "audio/aac")
	w.Header().Set("Cache-Control", "no-cache, no-store")
	w.Header().Set("Accept-Ranges", "none")
	h.writeICYHeaders(w, r, sess)

	if isProbeRequest(r) {
		slog.Debug("probe request answered 200 OK", "session_id", sessionID)
		w.WriteHeader(http.StatusOK)
		return
	}

	select {
	case h.streamsSem <- struct{}{}:
		defer func() { <-h.streamsSem }()
	default:
		http.Error(w, `{"error":"maximum active streams reached"}`, http.StatusTooManyRequests)
		return
	}

	slog.Debug("stream token validated", "session_id", sessionID, "reusable", h.cfg.DevReusableToken)

	proxyPort, proxyToken, unregister, err := h.registerTrackProxySession(sessionID, sess.AudioTracks)
	if err != nil {
		slog.Error("register track proxy session failed", "error", err)
		http.Error(w, `{"error":"failed to initialize media proxy"}`, http.StatusInternalServerError)
		return
	}
	defer unregister()

	inputPath, cleanup, err := prepareInput(proxyPort, sessionID, proxyToken, sess.AudioTracks, sess.StartingTrackIndex, sess.SeekOffset)
	if err != nil {
		slog.Error("prepare input failed", "error", err)
		http.Error(w, `{"error":"failed to prepare media input"}`, http.StatusInternalServerError)
		return
	}
	defer cleanup()

	streamCtx, cancelStream := context.WithCancel(r.Context())
	defer cancelStream()
	sess.Cancel = cancelStream

	params := h.buildFFmpegParams(inputPath, sess)

	cmd, stdout, stderrBuf, err := ffmpeg.StartProcess(streamCtx, &params)
	if err != nil {
		slog.Error("start ffmpeg error", "error", err)
		http.Error(w, `{"error":"failed to start transcoder"}`, http.StatusInternalServerError)
		return
	}
	sess.Cmd = cmd
	defer terminateProcess(sessionID, stdout, cmd, stderrBuf)

	w.Header().Set("Transfer-Encoding", "chunked")
	w.WriteHeader(http.StatusOK)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}

	syncStop := make(chan struct{})
	defer close(syncStop)
	go h.runSyncLoop(streamCtx, sess, syncStop)

	slog.Debug("streaming started", "session_id", sessionID)

	streamStart := time.Now()

	burstBytes := int64(h.cfg.BufferDuration.Seconds() * float64(ratelimit.DefaultBytesPerSecond))
	rw := ratelimit.NewWriter(streamCtx, w, burstBytes, ratelimit.DefaultBytesPerSecond)

	stopProgress := h.startProgressLogger(streamCtx, sess, sessionID, streamStart)
	defer stopProgress()

	termReason := h.pipeStreamToClient(streamCtx, stdout, rw, sess)

	if sess.BytesSent.Load() == 0 {
		slog.Debug("client disconnected before receiving audio data (probe), session retained", "session_id", sessionID)
		return
	}

	h.handleDisconnectSync(r.Context(), sess, termReason)

	elapsed := time.Since(streamStart)
	h.logStreamEnd(sessionID, sess, elapsed, termReason, stderrBuf)

	if !h.cfg.DevReusableToken {
		h.store.Delete(sess.ID)
	}
}

func isProbeRequest(r *http.Request) bool {
	return r.Method == http.MethodHead
}

func (h *Handler) validateStreamRequest(w http.ResponseWriter, r *http.Request, sessionID, token string) (*session.Session, bool) {
	reusable := isProbeRequest(r) || h.cfg.DevReusableToken
	sess, err := h.store.ValidateToken(sessionID, token, reusable)
	if err != nil {
		slog.Debug("stream token validation failed", "session_id", sessionID, "error", err)
		http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusUnauthorized)
		return nil, false
	}
	return sess, true
}

func (h *Handler) handleDisconnectSync(ctx context.Context, sess *session.Session, termReason string) {
	speed := sess.Speed
	if speed <= 0 {
		speed = 1.0
	}
	var currentPos float64
	if termReason == "eof" {
		if sess.Duration > 0 {
			currentPos = sess.Duration
		} else {
			currentPos = calculateCurrentPosition(sess.CurrentTime, sess.BytesSent.Load(), speed, 0, sess.AudioElapsed(time.Now()))
		}
	} else {
		currentPos = calculateCurrentPosition(sess.CurrentTime, sess.BytesSent.Load(), speed, h.cfg.BufferDuration, sess.AudioElapsed(time.Now()))
		if sess.Duration > 0 && sess.Duration-currentPos <= h.cfg.BufferDuration.Seconds()*speed {
			currentPos = sess.Duration
		}
	}
	syncReq := absclient.SyncRequest{
		CurrentTime:  currentPos,
		TimeListened: max(0, currentPos-sess.GetLastSyncPosition()),
		Duration:     sess.Duration,
	}
	disconnectCtx, disconnectCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer disconnectCancel()
	if syncErr := h.absClient.SyncSession(disconnectCtx, sess.ID, syncReq); syncErr != nil {
		slog.Error("disconnect sync failed", "session_id", sess.ID, "error", syncErr)
	} else {
		sess.SetLastSyncPosition(currentPos)
	}
	if !h.cfg.DevReusableToken {
		if closeErr := h.absClient.CloseSession(disconnectCtx, sess.ID); closeErr != nil {
			slog.Error("disconnect close failed", "session_id", sess.ID, "error", closeErr)
		}
	}
}

func defaultProcessKill(p *os.Process) error {
	if err := p.Kill(); err != nil {
		return fmt.Errorf("kill process: %w", err)
	}
	return nil
}

var (
	processKillMu sync.RWMutex
	processKill   = defaultProcessKill
)

func callProcessKill(p *os.Process) error {
	processKillMu.RLock()
	fn := processKill
	processKillMu.RUnlock()
	return fn(p)
}

func terminateProcess(sessionID string, stdout io.Closer, cmd *exec.Cmd, stderrBuf *bytes.Buffer) {
	if closeErr := stdout.Close(); closeErr != nil && !errors.Is(closeErr, os.ErrClosed) {
		slog.Debug("close stdout error", "session_id", sessionID, "error", closeErr)
	}
	if cmd == nil {
		return
	}
	if cmd.Process != nil {
		if killErr := callProcessKill(cmd.Process); killErr != nil && !errors.Is(killErr, os.ErrProcessDone) {
			slog.Error("kill process error", "session_id", sessionID, "error", killErr)
		}
	}
	logProcessExit(sessionID, cmd, stderrBuf)
}

func logProcessExit(sessionID string, cmd *exec.Cmd, stderrBuf *bytes.Buffer) {
	waitErr := cmd.Wait()
	var stderrOutput string
	if stderrBuf != nil && stderrBuf.Len() > 0 {
		cleaned := strings.ReplaceAll(strings.TrimSpace(stderrBuf.String()), "\n", " | ")
		if cleaned != "" {
			stderrOutput = "; stderr: " + cleaned
		}
	}
	if waitErr == nil {
		code := 0
		if cmd.ProcessState != nil {
			code = cmd.ProcessState.ExitCode()
		}
		slog.Info("ffmpeg process exited cleanly", "session_id", sessionID, "exit_code", code)
		return
	}
	if errors.Is(waitErr, os.ErrProcessDone) || strings.Contains(waitErr.Error(), "already waited") || strings.Contains(waitErr.Error(), "not started") {
		return
	}
	if exitErr, ok := errors.AsType[*exec.ExitError](waitErr); ok {
		if strings.Contains(exitErr.Error(), "killed") || strings.Contains(exitErr.String(), "killed") {
			slog.Debug("ffmpeg process terminated", "session_id", sessionID, "reason", "killed")
			return
		}
		slog.Error("ffmpeg process exited with error", "session_id", sessionID, "exit_code", exitErr.ExitCode(), "error", exitErr, "stderr", stderrOutput)
		return
	}
	slog.Error("wait ffmpeg process error", "session_id", sessionID, "error", waitErr, "stderr", stderrOutput)
}

func (h *Handler) startKeepalive(ctx context.Context, rw *ratelimit.Writer, sess *session.Session, writeMu *sync.Mutex) context.CancelFunc {
	keepaliveCtx, keepaliveCancel := context.WithCancel(ctx)
	interval := h.keepaliveInterval
	if interval <= 0 {
		interval = 1 * time.Second
	}

	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-keepaliveCtx.Done():
				return
			case <-ticker.C:
				writeMu.Lock()
				if sess.BytesSent.Load() == 0 {
					_, writeErr := rw.Write([]byte{0})
					if writeErr != nil {
						slog.Debug("keepalive write error", "error", writeErr)
					}
				}
				writeMu.Unlock()
			}
		}
	}()

	return keepaliveCancel
}

func (h *Handler) pipeStreamToClient(streamCtx context.Context, stdout io.Reader, rw *ratelimit.Writer, sess *session.Session) string {
	buf := make([]byte, 32768)
	var writeMu sync.Mutex
	keepaliveCancel := h.startKeepalive(streamCtx, rw, sess, &writeMu)
	defer keepaliveCancel()

	for streamCtx.Err() == nil {
		nr, readErr := stdout.Read(buf)
		if streamCtx.Err() != nil {
			return "context_canceled"
		}
		if nr > 0 {
			writeMu.Lock()
			if sess.BytesSent.Load() == 0 {
				keepaliveCancel()
				sess.SetAudioStartTime(time.Now())
				if !h.cfg.DevReusableToken {
					h.store.MarkTokenUsed(sess.ID)
				}
			}
			sess.BytesSent.Add(int64(nr))
			_, writeErr := rw.Write(buf[:nr])
			writeMu.Unlock()

			if writeErr != nil {
				return "client_write_error"
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return "eof"
			}
			return "ffmpeg_read_error"
		}
	}
	return "context_canceled"
}

func (h *Handler) startProgressLogger(ctx context.Context, sess *session.Session, sessionID string, start time.Time) func() {
	progressStop := make(chan struct{})
	go h.logStreamProgress(ctx, sess, sessionID, start, progressStop)
	return func() { close(progressStop) }
}

func (h *Handler) logStreamProgress(ctx context.Context, sess *session.Session, sessionID string, start time.Time, stop <-chan struct{}) {
	interval := h.progressInterval
	if interval <= 0 {
		interval = 60 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-stop:
			return
		case <-ticker.C:
			pos := calculateCurrentPosition(sess.CurrentTime, sess.BytesSent.Load(), sess.Speed, h.cfg.BufferDuration, sess.AudioElapsed(time.Now()))
			slog.Debug("stream progress",
				"session_id", sessionID,
				"elapsed", time.Since(start).Truncate(time.Second),
				"bytes", sess.BytesSent.Load(),
				"position", fmt.Sprintf("%.1f", pos),
			)
		}
	}
}

func (h *Handler) logStreamEnd(sessionID string, sess *session.Session, elapsed time.Duration, reason string, stderrBuf *bytes.Buffer) {
	slog.Info("stream ended",
		"session_id", sessionID,
		"elapsed", elapsed.Truncate(time.Second),
		"bytes", sess.BytesSent.Load(),
		"reason", reason,
	)

	if stderrBuf != nil && stderrBuf.Len() > 0 {
		cleaned := strings.ReplaceAll(strings.TrimSpace(stderrBuf.String()), "\n", " | ")
		if cleaned != "" {
			slog.Debug("ffmpeg stderr", "session_id", sessionID, "stderr", cleaned)
		}
	}
}

func calculateCurrentPosition(initialTime float64, bytesSent int64, speed float64, bufferDuration, clockElapsed time.Duration) float64 {
	playedSeconds := max(0, (float64(bytesSent)/float64(ratelimit.DefaultBytesPerSecond))-bufferDuration.Seconds())
	if clockElapsed > 0 {
		playedSeconds = min(playedSeconds, clockElapsed.Seconds())
	}
	if speed <= 0 {
		speed = 1.0
	}
	return initialTime + (playedSeconds * speed)
}

func (h *Handler) runSyncLoop(ctx context.Context, sess *session.Session, stop <-chan struct{}) {
	interval := h.syncInterval
	if interval <= 0 {
		interval = syncInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-stop:
			return
		case <-ticker.C:
			currentPos := calculateCurrentPosition(sess.CurrentTime, sess.BytesSent.Load(), sess.Speed, h.cfg.BufferDuration, sess.AudioElapsed(time.Now()))
			syncReq := absclient.SyncRequest{
				CurrentTime:  currentPos,
				TimeListened: max(0, currentPos-sess.GetLastSyncPosition()),
				Duration:     sess.Duration,
			}

			if err := h.absClient.SyncSession(ctx, sess.ID, syncReq); err != nil {
				slog.Error("periodic sync failed", "session_id", sess.ID, "error", err)
			} else {
				sess.SetLastSyncPosition(currentPos)
			}
		}
	}
}

// HandleSessionStop stops, syncs and closes the session on ABS.
func (h *Handler) HandleSessionStop(w http.ResponseWriter, r *http.Request) {
	h.handleSessionTerminate(w, r)
}

func parseSessionActionRequest(r *http.Request) (string, error) {
	var req SessionActionRequest
	bodyReader := io.LimitReader(r.Body, maxRequestBodySize)
	if err := json.NewDecoder(bodyReader).Decode(&req); err != nil {
		return "", errors.New("invalid request body: malformed JSON")
	}

	sessionID := req.SessionID
	if sessionID == "" {
		sessionID = req.SnakeSessionID
	}
	if sessionID == "" {
		return "", errors.New("valid sessionId is required")
	}
	return sessionID, nil
}

func stopSessionProcess(sess *session.Session) {
	if sess.Cancel != nil {
		sess.Cancel()
	}
	if sess.Cmd != nil && sess.Cmd.Process != nil {
		if killErr := callProcessKill(sess.Cmd.Process); killErr != nil && !errors.Is(killErr, os.ErrProcessDone) {
			slog.Error("kill ffmpeg process on terminate", "session_id", sess.ID, "error", killErr)
		}
	}
}

func (h *Handler) handleSessionTerminate(w http.ResponseWriter, r *http.Request) {
	sessionID, err := parseSessionActionRequest(r)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusBadRequest)
		return
	}

	sess, ok := h.store.Get(sessionID)
	if !ok {
		http.Error(w, `{"error":"session not found"}`, http.StatusNotFound)
		return
	}

	stopSessionProcess(sess)

	speed := sess.Speed
	if speed <= 0 {
		speed = 1.0
	}
	currentPos := calculateCurrentPosition(sess.CurrentTime, sess.BytesSent.Load(), speed, h.cfg.BufferDuration, sess.AudioElapsed(time.Now()))
	if sess.Duration > 0 && sess.Duration-currentPos <= h.cfg.BufferDuration.Seconds()*speed {
		currentPos = sess.Duration
	}
	syncReq := absclient.SyncRequest{
		CurrentTime:  currentPos,
		TimeListened: max(0, currentPos-sess.GetLastSyncPosition()),
		Duration:     sess.Duration,
	}

	if syncErr := h.absClient.SyncSession(r.Context(), sess.ID, syncReq); syncErr != nil {
		slog.Error("final sync error", "session_id", sess.ID, "error", syncErr)
	} else {
		sess.SetLastSyncPosition(currentPos)
	}

	if closeErr := h.absClient.CloseSession(r.Context(), sess.ID); closeErr != nil {
		slog.Error("close abs session error", "session_id", sess.ID, "error", closeErr)
	}
	h.store.Delete(sess.ID)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if _, writeErr := w.Write([]byte(`{"status":"stopped"}`)); writeErr != nil {
		slog.Error("write terminate response error", "session_id", sess.ID, "error", writeErr)
	}
}

func cleanHeaderValue(s string) string {
	cleaned := strings.Map(func(r rune) rune {
		if r < 32 && r != '\t' {
			return ' '
		}
		return r
	}, s)
	return strings.Join(strings.Fields(cleaned), " ")
}

type sessionMeta struct {
	title        string
	author       string
	narrator     string
	episodeTitle string
	mediaType    string
}

func isPodcast(mediaType, episodeID string) bool {
	return mediaType == "podcast" || episodeID != ""
}

func normalizeSessionRequest(req *StartSessionRequest) {
	if req.ItemID == "" {
		req.ItemID = req.SnakeItemID
	}
	if req.EpisodeID == "" {
		req.EpisodeID = req.SnakeEpID
	}
	if req.EpisodeTitle == "" {
		req.EpisodeTitle = req.SnakeEpTitle
	}
	if req.MediaType == "" {
		req.MediaType = req.SnakeMedType
	}
	if req.CurrentTime == 0 && req.SnakeTime > 0 {
		req.CurrentTime = req.SnakeTime
	}
	if req.Speed <= 0 {
		req.Speed = 1.0
	}
}

func extractTitle(reqTitle string, playResp *absclient.PlayResponse) string {
	if reqTitle != "" {
		return cleanHeaderValue(reqTitle)
	}
	if playResp.DisplayTitle != "" {
		return cleanHeaderValue(playResp.DisplayTitle)
	}
	if playResp.MediaMetadata != nil {
		return cleanHeaderValue(playResp.MediaMetadata.Title)
	}
	return ""
}

func extractAuthor(reqAuthor string, playResp *absclient.PlayResponse) string {
	if reqAuthor != "" {
		return cleanHeaderValue(reqAuthor)
	}
	if playResp.DisplayAuthor != "" {
		return cleanHeaderValue(playResp.DisplayAuthor)
	}
	if playResp.MediaMetadata != nil {
		if playResp.MediaMetadata.AuthorName != "" {
			return cleanHeaderValue(playResp.MediaMetadata.AuthorName)
		}
		return cleanHeaderValue(playResp.MediaMetadata.Author)
	}
	return ""
}

func extractNarrator(reqNarrator string, playResp *absclient.PlayResponse) string {
	if reqNarrator != "" {
		return cleanHeaderValue(reqNarrator)
	}
	if playResp.MediaMetadata != nil {
		if len(playResp.MediaMetadata.Narrators) > 0 {
			return cleanHeaderValue(strings.Join(playResp.MediaMetadata.Narrators, ", "))
		}
		if playResp.MediaMetadata.NarratorName != "" {
			return cleanHeaderValue(playResp.MediaMetadata.NarratorName)
		}
	}
	if playResp.LibraryItem != nil && playResp.LibraryItem.Media.Metadata.NarratorName != "" {
		return cleanHeaderValue(playResp.LibraryItem.Media.Metadata.NarratorName)
	}
	return ""
}

func resolveSessionMetadata(req *StartSessionRequest, playResp *absclient.PlayResponse) sessionMeta {
	mType := req.MediaType
	if mType == "" {
		mType = playResp.MediaType
	}

	epTitle := cleanHeaderValue(req.EpisodeTitle)
	if epTitle == "" && isPodcast(mType, req.EpisodeID) {
		epTitle = cleanHeaderValue(playResp.DisplayTitle)
	}

	return sessionMeta{
		title:        extractTitle(req.Title, playResp),
		author:       extractAuthor(req.Author, playResp),
		narrator:     extractNarrator(req.Narrator, playResp),
		episodeTitle: epTitle,
		mediaType:    mType,
	}
}

func writePodcastICYHeaders(w http.ResponseWriter, sess *session.Session) {
	w.Header().Set("icy-genre", "Podcast")

	epTitle := cleanHeaderValue(sess.EpisodeTitle)
	if epTitle == "" {
		epTitle = cleanHeaderValue(sess.Title)
	}
	if epTitle == "" {
		epTitle = "Podcast Episode"
	}
	w.Header().Set("icy-name", epTitle)

	podTitle := cleanHeaderValue(sess.Title)
	if podTitle == "" || podTitle == epTitle {
		podTitle = cleanHeaderValue(sess.Author)
	}
	if podTitle != "" {
		w.Header().Set("icy-description", podTitle)
	}
}

func writeAudiobookICYHeaders(w http.ResponseWriter, sess *session.Session) {
	w.Header().Set("icy-genre", "Audiobook")

	title := cleanHeaderValue(sess.Title)
	author := cleanHeaderValue(sess.Author)
	narrator := cleanHeaderValue(sess.Narrator)

	switch {
	case title != "" && author != "" && narrator != "":
		w.Header().Set("icy-name", fmt.Sprintf("%s • %s", title, author))
		w.Header().Set("icy-description", narrator)
	case title != "" && narrator != "":
		w.Header().Set("icy-name", title)
		w.Header().Set("icy-description", narrator)
	case title != "" && author != "":
		w.Header().Set("icy-name", title)
		w.Header().Set("icy-description", author)
	case title != "":
		w.Header().Set("icy-name", title)
	case author != "":
		w.Header().Set("icy-name", author)
	default:
		w.Header().Set("icy-name", "Audiobook")
	}
}

func (h *Handler) writeCoverICYHeaders(w http.ResponseWriter, r *http.Request, sess *session.Session) {
	coverURL := sess.CoverURL
	if coverURL == "" && sess.ItemID != "" {
		if extBase, err := h.resolveExternalBaseURL(r); err == nil {
			coverURL = fmt.Sprintf("%s/api/proxy/covers/%s", extBase, sess.ItemID)
		}
	}
	if coverURL != "" {
		cleanCover := cleanHeaderValue(coverURL)
		w.Header().Set("icy-logo", cleanCover)
		w.Header().Set("icy-url", cleanCover)
	}
}

func (h *Handler) writeICYHeaders(w http.ResponseWriter, r *http.Request, sess *session.Session) {
	if isPodcast(sess.MediaType, sess.EpisodeID) {
		writePodcastICYHeaders(w, sess)
	} else {
		writeAudiobookICYHeaders(w, sess)
	}
	h.writeCoverICYHeaders(w, r, sess)
}
