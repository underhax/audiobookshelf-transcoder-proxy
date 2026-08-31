package handler

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/underhax/audiobookshelf-transcoder-proxy/internal/absclient"
	"github.com/underhax/audiobookshelf-transcoder-proxy/internal/ratelimit"
	"github.com/underhax/audiobookshelf-transcoder-proxy/internal/session"
)

type testCloser struct {
	err error
}

func (c *testCloser) Close() error {
	return c.err
}

type delayedReader struct {
	data   []byte
	called bool
}

func (d *delayedReader) Read(p []byte) (int, error) {
	if !d.called {
		d.called = true
		time.Sleep(20 * time.Millisecond)
		n := copy(p, d.data)
		return n, nil
	}
	return 0, io.EOF
}

type cancelOnReadReader struct {
	cancel context.CancelFunc
}

func (c *cancelOnReadReader) Read(_ []byte) (int, error) {
	c.cancel()
	return 0, errors.New("context cancelled during read")
}

func TestCalculateSeekOffset(t *testing.T) {
	t.Parallel()

	tracks := []absclient.AudioTrack{
		{Index: 0, Duration: 60.0},
		{Index: 1, Duration: 120.0},
		{Index: 2, Duration: 60.0},
	}

	offset, idx := calculateSeekOffset(tracks, 250.0)
	if idx != 2 || offset != 0 {
		t.Errorf("expected idx=2 offset=0 for out-of-bounds seek, got idx=%d offset=%f", idx, offset)
	}

	offsetEmpty, idxEmpty := calculateSeekOffset([]absclient.AudioTrack{}, 30.0)
	if idxEmpty != 0 || offsetEmpty != 30.0 {
		t.Errorf("expected idx=0 offset=30.0 for empty tracks, got idx=%d offset=%f", idxEmpty, offsetEmpty)
	}
}

func TestPrepareInput_EmptyTracks(t *testing.T) {
	t.Parallel()

	_, _, _, err := prepareInput("http://abs.example.com", []absclient.AudioTrack{}, 0)
	if err == nil {
		t.Fatal("expected error when audio tracks slice is empty")
	}
}

func TestPrepareInput_GenerateConcatError(t *testing.T) {
	t.Setenv("TMPDIR", "/non/existent/dir/xyz")
	tracks := []absclient.AudioTrack{
		{Index: 0, ContentURL: "/t1.mp3"},
		{Index: 1, ContentURL: "/t2.mp3"},
	}
	if _, _, _, err := prepareInput("http://abs.example.com", tracks, 0); err == nil {
		t.Fatal("expected error from defaultGenerateConcat when TMPDIR is invalid")
	}
}

func TestPrepareInput_CleanupError(t *testing.T) {
	dir := t.TempDir()
	childPath := filepath.Join(dir, "child.txt")
	if writeErr := os.WriteFile(childPath, []byte("data"), 0o600); writeErr != nil {
		t.Fatalf("write child: %v", writeErr)
	}

	orig := generateConcat
	generateConcat = func(_ string, _ []string) (string, error) {
		return dir, nil
	}
	defer func() { generateConcat = orig }()

	tracks := []absclient.AudioTrack{
		{Index: 0, ContentURL: "/cleanup-track-1.mp3"},
		{Index: 1, ContentURL: "/cleanup-track-2.mp3"},
	}
	_, _, cleanup, err := prepareInput("http://abs.example.com", tracks, 0)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	cleanup()
}

func TestRunSyncLoop(t *testing.T) {
	t.Parallel()

	syncCalls := 0
	roundTrip := func(req *http.Request) (*http.Response, error) {
		if strings.Contains(req.URL.Path, "/sync") {
			syncCalls++
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{}`)),
		}, nil
	}

	h, _ := newTestEnv(t, roundTrip)
	h.syncInterval = 10 * time.Millisecond

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	sess := &session.Session{
		ID:          "sess-sync-loop-test",
		CurrentTime: 10.0,
		Duration:    100.0,
		Speed:       1.0,
	}
	sess.BytesSent.Store(80000)

	stop := make(chan struct{})

	done := make(chan struct{})
	go func() {
		h.runSyncLoop(ctx, sess, stop)
		close(done)
	}()

	time.Sleep(35 * time.Millisecond)
	close(stop)

	<-done

	if syncCalls == 0 {
		t.Errorf("expected at least 1 sync call in sync loop, got %d", syncCalls)
	}
}

func TestRunSyncLoop_Error(t *testing.T) {
	t.Parallel()

	roundTrip := func(_ *http.Request) (*http.Response, error) {
		return nil, errors.New("simulated sync error")
	}

	h, _ := newTestEnv(t, roundTrip)
	h.syncInterval = 5 * time.Millisecond

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	sess := &session.Session{
		ID:          "sess-sync-err",
		CurrentTime: 0,
		Duration:    50,
		Speed:       1.0,
	}

	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		h.runSyncLoop(ctx, sess, stop)
		close(done)
	}()

	time.Sleep(15 * time.Millisecond)
	close(stop)
	<-done
}

func TestRunSyncLoop_ContextCancel(t *testing.T) {
	t.Parallel()

	h, _ := newTestEnv(t, nil)
	h.syncInterval = 100 * time.Millisecond

	ctx, cancel := context.WithCancel(t.Context())
	sess := &session.Session{ID: "sess-loop-cancel"}
	stop := make(chan struct{})
	done := make(chan struct{})

	go func() {
		h.runSyncLoop(ctx, sess, stop)
		close(done)
	}()

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for runSyncLoop to exit on context cancellation")
	}
}

func TestRunSyncLoop_DefaultIntervalFallback(t *testing.T) {
	t.Parallel()

	h, _ := newTestEnv(t, nil)
	h.syncInterval = 0

	sess := &session.Session{ID: "sess-loop-def-fallback"}
	stop := make(chan struct{})
	close(stop)

	h.runSyncLoop(t.Context(), sess, stop)
}

func TestTerminateProcess(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	cmdNil := exec.CommandContext(ctx, "echo", "test")
	closerErr := &testCloser{err: errors.New("close failure")}
	terminateProcess(closerErr, cmdNil)

	cmdRun := exec.CommandContext(ctx, "sleep", "5")
	if err := cmdRun.Start(); err != nil {
		t.Fatalf("start sleep error: %v", err)
	}
	closerOk := &testCloser{err: nil}
	terminateProcess(closerOk, cmdRun)
}

func TestTerminateProcess_ProcessKillError(t *testing.T) {
	processKillMu.Lock()
	orig := processKill
	processKill = func(_ *os.Process) error {
		return errors.New("mocked kill error")
	}
	processKillMu.Unlock()
	defer func() {
		processKillMu.Lock()
		processKill = orig
		processKillMu.Unlock()
	}()

	cmd := exec.CommandContext(t.Context(), "true")
	if err := cmd.Run(); err != nil {
		t.Fatalf("run true: %v", err)
	}
	closer := &testCloser{}
	terminateProcess(closer, cmd)

	if cmd.Process != nil {
		if err := defaultProcessKill(cmd.Process); err != nil && !errors.Is(err, os.ErrProcessDone) {
			t.Logf("default process kill error: %v", err)
		}
	}
}

func TestCalculateCurrentPosition(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		initialTime    float64
		bytesSent      int64
		speed          float64
		bufferDuration time.Duration
		want           float64
	}{
		{
			name:           "zero bytes sent preserves initial time",
			initialTime:    100.0,
			bytesSent:      0,
			speed:          1.0,
			bufferDuration: 10 * time.Second,
			want:           100.0,
		},
		{
			name:           "bytes sent within buffer duration does not advance position",
			initialTime:    50.0,
			bytesSent:      40000,
			speed:          1.0,
			bufferDuration: 10 * time.Second,
			want:           50.0,
		},
		{
			name:           "bytes sent exactly matching buffer duration does not advance position",
			initialTime:    0.0,
			bytesSent:      80000,
			speed:          1.0,
			bufferDuration: 10 * time.Second,
			want:           0.0,
		},
		{
			name:           "bytes sent exceeding buffer advances position with normal speed",
			initialTime:    10.0,
			bytesSent:      160000,
			speed:          1.0,
			bufferDuration: 10 * time.Second,
			want:           20.0,
		},
		{
			name:           "bytes sent exceeding buffer advances position with custom playback speed",
			initialTime:    100.0,
			bytesSent:      160000,
			speed:          1.5,
			bufferDuration: 10 * time.Second,
			want:           115.0,
		},
		{
			name:           "fallback to default 1.0 speed when non-positive speed is provided",
			initialTime:    100.0,
			bytesSent:      160000,
			speed:          0.0,
			bufferDuration: 10 * time.Second,
			want:           110.0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := calculateCurrentPosition(tt.initialTime, tt.bytesSent, tt.speed, tt.bufferDuration)
			if got != tt.want {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPipeStreamToClient_KeepaliveAndWriteError(t *testing.T) {
	t.Parallel()

	h, store := newTestEnv(t, nil)
	h.cfg.Debug = true
	h.keepaliveInterval = 5 * time.Millisecond

	sess := &session.Session{
		ID:    "sess-pipe-keepalive",
		Speed: 1.0,
	}
	if _, err := store.Create(sess); err != nil {
		t.Fatalf("create sess: %v", err)
	}

	ew := &errResponseWriter{}
	rw := ratelimit.NewWriter(context.Background(), ew, 1000, 1000)

	dr := &delayedReader{
		data: []byte("final-audio-chunk"),
	}

	ctx, cancel := context.WithTimeout(t.Context(), 25*time.Millisecond)
	defer cancel()

	h.pipeStreamToClient(ctx, dr, rw, sess)
}

func TestPipeStreamToClient_ContextCancelledDuringRead(t *testing.T) {
	t.Parallel()

	h, store := newTestEnv(t, nil)
	sess := &session.Session{
		ID:    "sess-pipe-cancel",
		Speed: 1.0,
	}
	if _, err := store.Create(sess); err != nil {
		t.Fatalf("create sess: %v", err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	cr := &cancelOnReadReader{cancel: cancel}
	buf := &bytes.Buffer{}
	rw := ratelimit.NewWriter(ctx, buf, 1000, 1000)

	h.pipeStreamToClient(ctx, cr, rw, sess)
}

func TestPipeStreamToClient_DefaultIntervalFallback(t *testing.T) {
	t.Parallel()

	h, store := newTestEnv(t, nil)
	h.keepaliveInterval = 0

	sess := &session.Session{
		ID:    "sess-pipe-def-interval",
		Speed: 1.0,
	}
	if _, err := store.Create(sess); err != nil {
		t.Fatalf("create sess: %v", err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	rw := ratelimit.NewWriter(ctx, &bytes.Buffer{}, 1000, 1000)
	h.pipeStreamToClient(ctx, strings.NewReader("data"), rw, sess)
}
