// Package main launches the Audiobookshelf Transcoder Proxy (abstp).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	"github.com/underhax/audiobookshelf-transcoder-proxy/internal/absclient"
	"github.com/underhax/audiobookshelf-transcoder-proxy/internal/config"
	"github.com/underhax/audiobookshelf-transcoder-proxy/internal/handler"
	"github.com/underhax/audiobookshelf-transcoder-proxy/internal/session"
	"github.com/underhax/audiobookshelf-transcoder-proxy/internal/trackproxy"
)

// Version indicates the current binary release version injected via linker flags during compilation.
var Version = "dev"

func defaultNotifySignals(c chan<- os.Signal, sig ...os.Signal) {
	signal.Notify(c, sig...)
}

func defaultStartTrackProxy(tp *trackproxy.Server) (int, error) {
	port, err := tp.Start()
	if err != nil {
		return 0, fmt.Errorf("track proxy start: %w", err)
	}
	return port, nil
}

func defaultShutdownTrackProxy(ctx context.Context, tp *trackproxy.Server) error {
	if err := tp.Shutdown(ctx); err != nil {
		return fmt.Errorf("track proxy shutdown: %w", err)
	}
	return nil
}

var (
	stdout             io.Writer = os.Stdout
	executeHealthcheck           = defaultHealthcheck
	shutdownServer               = defaultShutdown
	runFunc                      = run
	logFatalf                    = log.Fatalf
	notifySignals                = defaultNotifySignals
	startTrackProxy              = defaultStartTrackProxy
	shutdownTrackProxy           = defaultShutdownTrackProxy
)

func defaultHealthcheck(ctx context.Context, listenAddr string, client *http.Client) error {
	_, port, err := net.SplitHostPort(listenAddr)
	if err != nil {
		return fmt.Errorf("parse listen address: %w", err)
	}

	urlStr := fmt.Sprintf("http://127.0.0.1:%s/health", port)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, urlStr, http.NoBody)
	if err != nil {
		return fmt.Errorf("create healthcheck request: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("execute healthcheck: %w", err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			log.Printf("close healthcheck body: %v", closeErr)
		}
	}()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("healthcheck returned non-200 status: %d", resp.StatusCode)
	}
	return nil
}

func defaultShutdown(ctx context.Context, srv *http.Server) error {
	if err := srv.Shutdown(ctx); err != nil {
		return fmt.Errorf("server shutdown: %w", err)
	}
	return nil
}

func printHelp(out io.Writer) {
	text := "Audiobookshelf Transcoder Proxy (abstp)\n\n" +
		"Usage:\n  abstp [flags]\n\n" +
		"Flags:\n" +
		"  -h, --help             Display help information and exit\n" +
		"  -v, --version          Print application version and exit\n" +
		"      --healthcheck      Perform liveness probe against running instance and exit\n\n" +
		"Environment Variables:\n" +
		"  ABSTP_ABS_URL          Audiobookshelf base URL (required, e.g. https://abs.example.org)\n" +
		"  ABSTP_ABS_TOKEN        Audiobookshelf user/API token (required, or ABSTP_ABS_TOKEN_FILE)\n" +
		"  ABSTP_ABS_TOKEN_FILE   Path to file containing Audiobookshelf token\n" +
		"  ABSTP_API_KEY          Proxy authentication secret key (required, or ABSTP_API_KEY_FILE)\n" +
		"  ABSTP_API_KEY_FILE     Path to file containing proxy authentication secret key\n" +
		"  ABSTP_LISTEN_ADDR      Server listen address (default: 127.0.0.1:8099)\n" +
		"  ABSTP_EXTERNAL_URL     Client-accessible external base URL (default: http://<listen_addr>)\n" +
		"  ABSTP_FFMPEG_PATH      Path to ffmpeg binary (default: ffmpeg)\n" +
		"  ABSTP_TOKEN_TTL        Stream token TTL duration (default: 30s)\n" +
		"  ABSTP_BUFFER_DURATION  Initial stream buffer duration (default: 10s)\n" +
		"  ABSTP_MAX_CONNS        Max concurrent incoming HTTP connections (default: 100)\n" +
		"  ABSTP_MAX_STREAMS      Max concurrent active transcoding streams (default: 5)\n" +
		"  ABSTP_IN_DOCKER        Running inside Docker container (true/false, default: false)\n"
	if _, err := io.WriteString(out, text); err != nil {
		log.Printf("write help error: %v", err)
	}
}

func checkCommands(args []string, out io.Writer) bool {
	if len(args) == 0 {
		return false
	}

	switch args[0] {
	case "help", "-h", "--help", "-help":
		printHelp(out)
		return true
	case "version", "-v", "--version", "-version":
		if _, err := fmt.Fprintf(out, "abstp version %s\n", Version); err != nil {
			log.Printf("write version error: %v", err)
		}
		return true
	default:
		return false
	}
}

func run(args []string, getenv func(string) string, sigs ...os.Signal) error {
	if checkCommands(args, stdout) {
		return nil
	}

	fs := flag.NewFlagSet("abstp", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	healthcheckFlag := fs.Bool("healthcheck", false, "Run healthcheck against local instance")

	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("parse flags: %w", err)
	}

	cfg, err := config.Load(getenv, exec.LookPath, Version)
	if err != nil {
		return fmt.Errorf("%w", err)
	}

	if *healthcheckFlag {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()

		if hcErr := executeHealthcheck(ctx, cfg.ListenAddr, http.DefaultClient); hcErr != nil {
			return fmt.Errorf("healthcheck failed: %w", hcErr)
		}
		if _, hcPrintErr := fmt.Fprintln(stdout, "healthcheck ok"); hcPrintErr != nil {
			log.Printf("write healthcheck ok error: %v", hcPrintErr)
		}
		return nil
	}

	absCli := absclient.New(cfg.ABSURL, cfg.ABSToken, Version, http.DefaultClient)
	detectCtx, detectCancel := context.WithTimeout(context.Background(), 3*time.Second)
	cfg.ABSURL = absCli.DetectBaseURL(detectCtx)
	detectCancel()
	log.Printf("Target Audiobookshelf URL: %s", cfg.ABSURL)

	trackProxy := trackproxy.New(cfg.ABSURL, cfg.ABSToken, Version, cfg.Debug)
	proxyPort, err := startTrackProxy(trackProxy)
	if err != nil {
		return fmt.Errorf("start track proxy: %w", err)
	}
	log.Printf("Internal track proxy listening on 127.0.0.1:%d", proxyPort)
	defer func() {
		shutCtx, shutCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutCancel()
		if shutErr := shutdownTrackProxy(shutCtx, trackProxy); shutErr != nil && !errors.Is(shutErr, http.ErrServerClosed) {
			log.Printf("shutdown track proxy error: %v", shutErr)
		}
	}()

	store := session.NewStore(cfg.TokenTTL)
	h := handler.NewHandler(&cfg, store, absCli, trackProxy)

	server := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           h.Routes(),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	serverErr := make(chan error, 1)
	startServer(server, &cfg, serverErr)

	quit := make(chan os.Signal, 1)
	if len(sigs) > 0 {
		notifySignals(quit, sigs...)
	}

	select {
	case err := <-serverErr:
		return err
	case sig := <-quit:
		log.Printf("Received signal %s, initiating graceful shutdown...", sig)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		if shutErr := shutdownServer(ctx, server); shutErr != nil {
			return shutErr
		}
		log.Println("abstp stopped gracefully")
		return nil
	}
}

func startServer(server *http.Server, cfg *config.Config, serverErr chan<- error) {
	go func() {
		log.Printf("Starting abstp on %s (version: %s)", cfg.ListenAddr, Version)
		if cfg.Debug {
			log.Println("[INFO] debug logging enabled (ABSTP_DEBUG=true)")
		}
		if cfg.DevReusableToken {
			log.Println("[WARN] dev reusable stream token enabled (ABSTP_DEV_REUSABLE_TOKEN=true)")
		}
		if !cfg.InDocker {
			fmt.Println("Press Ctrl+C to exit")
		}
		if listenErr := server.ListenAndServe(); listenErr != nil && !errors.Is(listenErr, http.ErrServerClosed) {
			serverErr <- fmt.Errorf("server error: %w", listenErr)
		}
	}()
}

func main() {
	if err := runFunc(os.Args[1:], os.Getenv, os.Interrupt, syscall.SIGTERM); err != nil {
		logFatalf("Error: %v", err)
	}
}
