package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/underhax/audiobookshelf-transcoder-proxy/internal/trackproxy"
)

type roundTripFunc func(req *http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func newTestHTTPClient(fn roundTripFunc) *http.Client {
	return &http.Client{
		Transport: fn,
	}
}

type errWriter struct{}

func (errWriter) Write(_ []byte) (int, error) {
	return 0, errors.New("write failure")
}

func validTestEnv() func(string) string {
	env := map[string]string{
		"ABSTP_ABS_URL":      "http://abs.example.com",
		"ABSTP_ABS_TOKEN":    "test-token",
		"ABSTP_API_KEY":      "secret-key",
		"ABSTP_LISTEN_ADDR":  "127.0.0.1:8099",
		"ABSTP_EXTERNAL_URL": "http://proxy.example.com:8099",
		"ABSTP_FFMPEG_PATH":  "echo",
	}
	return func(k string) string {
		return env[k]
	}
}

func TestCheckCommands(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		wantSubstr  string
		args        []string
		wantHandled bool
	}{
		{
			args:        nil,
			name:        "empty args",
			wantHandled: false,
		},
		{
			args:        []string{"unknown-cmd"},
			name:        "unknown command",
			wantHandled: false,
		},
		{
			args:        []string{"-help"},
			name:        "help flag",
			wantHandled: true,
			wantSubstr:  "ABSTP_IN_DOCKER",
		},
		{
			args:        []string{"version"},
			name:        "version flag",
			wantHandled: true,
			wantSubstr:  "abstp version",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			buf := &bytes.Buffer{}
			handled := checkCommands(tt.args, buf)
			if handled != tt.wantHandled {
				t.Errorf("got handled=%v, want %v", handled, tt.wantHandled)
			}
			if tt.wantSubstr != "" && !strings.Contains(buf.String(), tt.wantSubstr) {
				t.Errorf("expected substring %q in output: %s", tt.wantSubstr, buf.String())
			}
		})
	}
}

func TestPrintHelpAndVersion_WriteErrors(t *testing.T) {
	t.Parallel()

	ew := errWriter{}
	printHelp(ew)

	checkCommands([]string{"-v"}, ew)
}

func TestDefaultHealthcheck(t *testing.T) {
	t.Parallel()

	clientOK := newTestHTTPClient(func(_ *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"status":"ok"}`)),
		}, nil
	})
	if err := defaultHealthcheck(context.Background(), "127.0.0.1:8099", clientOK); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if err := defaultHealthcheck(context.Background(), "invalid-addr", clientOK); err == nil {
		t.Error("expected error for invalid listen address")
	}

	clientErr := newTestHTTPClient(func(_ *http.Request) (*http.Response, error) {
		return nil, errors.New("network unreachable")
	})
	if err := defaultHealthcheck(context.Background(), "127.0.0.1:8099", clientErr); err == nil {
		t.Error("expected error on client failure")
	}

	client503 := newTestHTTPClient(func(_ *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusServiceUnavailable,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"status":"unhealthy"}`)),
		}, nil
	})
	if err := defaultHealthcheck(context.Background(), "127.0.0.1:8099", client503); err == nil {
		t.Error("expected error on 503 status")
	}

	clientCloseErr := newTestHTTPClient(func(_ *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       errReadCloser{},
		}, nil
	})
	if err := defaultHealthcheck(context.Background(), "127.0.0.1:8099", clientCloseErr); err != nil {
		t.Errorf("unexpected error when body close fails: %v", err)
	}

	if err := defaultHealthcheck(context.Background(), "127.0.0.1:80 99", clientOK); err == nil {
		t.Error("expected error on invalid host port URL")
	}
}

type errReadCloser struct{}

func (errReadCloser) Read(_ []byte) (int, error) {
	return 0, io.EOF
}

func (errReadCloser) Close() error {
	return errors.New("close failure")
}

func TestDefaultShutdown(t *testing.T) {
	t.Parallel()

	srv := &http.Server{
		ReadHeaderTimeout: 5 * time.Second,
	}

	if err := defaultShutdown(context.Background(), srv); err != nil {
		t.Fatalf("unexpected shutdown error: %v", err)
	}

	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	el := &errListener{Listener: ln}
	srvRunning := &http.Server{ReadHeaderTimeout: 5 * time.Second}
	serverErrChan := make(chan error, 1)
	go func() {
		serverErrChan <- srvRunning.Serve(el)
	}()

	time.Sleep(10 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	if shutErr := defaultShutdown(ctx, srvRunning); shutErr == nil {
		t.Error("expected error when listener close fails")
	}

	<-serverErrChan
}

type errListener struct {
	net.Listener
}

func (e *errListener) Close() error {
	closeErr := e.Listener.Close()
	return errors.Join(errors.New("listener close error"), closeErr)
}

func TestRun_CommandsAndFlags(t *testing.T) {
	buf := &bytes.Buffer{}
	origStdout := stdout
	stdout = buf
	defer func() { stdout = origStdout }()

	if err := run([]string{"help"}, validTestEnv()); err != nil {
		t.Fatalf("run help error: %v", err)
	}
	if !strings.Contains(buf.String(), "Usage:") {
		t.Errorf("expected Usage in output: %s", buf.String())
	}

	buf.Reset()
	if err := run([]string{"--version"}, validTestEnv()); err != nil {
		t.Fatalf("run version error: %v", err)
	}
	if !strings.Contains(buf.String(), "abstp version") {
		t.Errorf("expected version in output: %s", buf.String())
	}

	if err := run([]string{"-unknown-flag"}, validTestEnv()); err == nil {
		t.Error("expected error for unknown flag")
	}

	emptyEnv := func(_ string) string { return "" }
	if err := run(nil, emptyEnv); err == nil {
		t.Error("expected config error when env is empty")
	}
}

func TestRun_Healthcheck(t *testing.T) {
	origHC := executeHealthcheck
	origStdout := stdout
	defer func() {
		executeHealthcheck = origHC
		stdout = origStdout
	}()

	tests := []struct {
		mockHC     func(context.Context, string, *http.Client) error
		outWriter  io.Writer
		name       string
		wantSubstr string
		wantErr    bool
	}{
		{
			mockHC: func(_ context.Context, _ string, _ *http.Client) error {
				return nil
			},
			outWriter:  &bytes.Buffer{},
			name:       "success",
			wantSubstr: "healthcheck ok",
			wantErr:    false,
		},
		{
			mockHC: func(_ context.Context, _ string, _ *http.Client) error {
				return errors.New("instance offline")
			},
			outWriter: &bytes.Buffer{},
			name:      "failure",
			wantErr:   true,
		},
		{
			mockHC: func(_ context.Context, _ string, _ *http.Client) error {
				return nil
			},
			outWriter: errWriter{},
			name:      "write error",
			wantErr:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			executeHealthcheck = tt.mockHC
			stdout = tt.outWriter

			err := run([]string{"-healthcheck"}, validTestEnv())
			if tt.wantErr && err == nil {
				t.Error("expected error")
			}
			if !tt.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
			if buf, ok := tt.outWriter.(*bytes.Buffer); ok && tt.wantSubstr != "" {
				if !strings.Contains(buf.String(), tt.wantSubstr) {
					t.Errorf("expected substring %q in output: %s", tt.wantSubstr, buf.String())
				}
			}
		})
	}
}

func TestRun_ServerStartupAndGracefulShutdown(t *testing.T) {
	origNotify := notifySignals
	defer func() { notifySignals = origNotify }()

	notifySignals = func(c chan<- os.Signal, _ ...os.Signal) {
		c <- syscall.SIGINT
	}

	err := run(nil, validTestEnv(), syscall.SIGINT)
	if err != nil {
		t.Fatalf("unexpected run error: %v", err)
	}
}

func TestRun_ServerStartupConfigurations(t *testing.T) {
	tests := []struct {
		name        string
		docker      bool
		debug       bool
		devReusable bool
	}{
		{
			name:   "in docker",
			docker: true,
		},
		{
			name:        "with debug and dev reusable token",
			debug:       true,
			devReusable: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			origNotify := notifySignals
			defer func() { notifySignals = origNotify }()

			notifySignals = func(c chan<- os.Signal, _ ...os.Signal) {
				c <- syscall.SIGINT
			}

			env := func(k string) string {
				switch {
				case tt.docker && k == "ABSTP_IN_DOCKER",
					tt.debug && k == "ABSTP_DEBUG",
					tt.devReusable && k == "ABSTP_DEV_REUSABLE_TOKEN":
					return "true"
				default:
					return validTestEnv()(k)
				}
			}

			err := run(nil, env, syscall.SIGINT)
			if err != nil {
				t.Fatalf("unexpected run error: %v", err)
			}
		})
	}
}

func TestRun_ShutdownError(t *testing.T) {
	origShutdown := shutdownServer
	origNotify := notifySignals
	defer func() {
		shutdownServer = origShutdown
		notifySignals = origNotify
	}()

	notifySignals = func(c chan<- os.Signal, _ ...os.Signal) {
		c <- syscall.SIGUSR1
	}

	shutdownServer = func(_ context.Context, _ *http.Server) error {
		return errors.New("forced shutdown failure")
	}

	err := run(nil, validTestEnv(), syscall.SIGUSR1)
	if err == nil || !strings.Contains(err.Error(), "forced shutdown failure") {
		t.Errorf("expected forced shutdown failure, got %v", err)
	}
}

func TestRun_ServerListenError(t *testing.T) {
	dummy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer dummy.Close()

	blockedEnv := func(k string) string {
		if k == "ABSTP_LISTEN_ADDR" {
			return dummy.Listener.Addr().String()
		}
		return validTestEnv()(k)
	}

	err := run(nil, blockedEnv, syscall.SIGTERM)
	if err == nil {
		t.Error("expected server listen error on already bound port")
	}
}

func TestMain_Execution(t *testing.T) {
	origRun := runFunc
	origFatal := logFatalf
	defer func() {
		runFunc = origRun
		logFatalf = origFatal
	}()

	runFunc = func(_ []string, _ func(string) string, _ ...os.Signal) error {
		return nil
	}
	main()

	fatalCalled := false
	runFunc = func(_ []string, _ func(string) string, _ ...os.Signal) error {
		return errors.New("critical startup error")
	}
	logFatalf = func(_ string, _ ...any) {
		fatalCalled = true
	}
	main()

	if !fatalCalled {
		t.Error("expected logFatalf to be called on runFunc failure")
	}
}

func TestRun_TrackProxyStartError(t *testing.T) {
	origStart := startTrackProxy
	defer func() { startTrackProxy = origStart }()

	startTrackProxy = func(_ *trackproxy.Server) (int, error) {
		return 0, errors.New("trackproxy start failure")
	}

	err := run(nil, validTestEnv(), syscall.SIGTERM)
	if err == nil || !strings.Contains(err.Error(), "start track proxy: trackproxy start failure") {
		t.Fatalf("expected trackproxy start error, got: %v", err)
	}
}

func TestRun_TrackProxyShutdownError(t *testing.T) {
	origShutdown := shutdownTrackProxy
	origNotify := notifySignals
	defer func() {
		shutdownTrackProxy = origShutdown
		notifySignals = origNotify
	}()

	notifySignals = func(c chan<- os.Signal, _ ...os.Signal) {
		c <- syscall.SIGINT
	}

	shutdownTrackProxy = func(_ context.Context, _ *trackproxy.Server) error {
		return errors.New("trackproxy shutdown failure")
	}

	err := run(nil, validTestEnv(), syscall.SIGINT)
	if err != nil {
		t.Fatalf("unexpected run error: %v", err)
	}
}

func TestDefaultStartTrackProxy_Error(t *testing.T) {
	cleanup := trackproxy.SetNetListen(func(_ context.Context, _, _ string) (net.Listener, error) {
		return nil, errors.New("listen failure")
	})
	defer cleanup()

	tp := trackproxy.New("http://start-err.example.net", "token", "1.0", false)
	if _, err := defaultStartTrackProxy(tp); err == nil {
		t.Error("expected error when tp.Start fails")
	}
}

func TestDefaultShutdownTrackProxy_Error(t *testing.T) {
	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen error: %v", err)
	}

	el := &errListener{Listener: ln}
	cleanup := trackproxy.SetNetListen(func(_ context.Context, _, _ string) (net.Listener, error) {
		return el, nil
	})
	defer cleanup()

	tp := trackproxy.New("http://shut-err.example.net", "token", "1.0", false)
	if _, err := tp.Start(); err != nil {
		t.Fatalf("start error: %v", err)
	}

	time.Sleep(10 * time.Millisecond)

	if err := defaultShutdownTrackProxy(context.Background(), tp); err == nil {
		t.Error("expected error when listener close fails")
	}
}
