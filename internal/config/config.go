// Package config handles parsing and validation of environment variables for abstp.
package config

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/underhax/audiobookshelf-transcoder-proxy/internal/validator"
)

// Config maintains runtime proxy parameters and authentication settings validated on startup.
type Config struct {
	ABSURL           string
	ABSToken         string
	APIKey           string
	ListenAddr       string
	ExternalURL      string
	FFmpegPath       string
	TokenTTL         time.Duration
	BufferDuration   time.Duration
	MaxConns         int
	MaxStreams       int
	InDocker         bool
	Debug            bool
	DevReusableToken bool
}

func defaultReadFile(name string) ([]byte, error) {
	data, err := os.ReadFile(filepath.Clean(name))
	if err != nil {
		return nil, fmt.Errorf("read file %q: %w", name, err)
	}
	return data, nil
}

func defaultStatFile(name string) (os.FileInfo, error) {
	info, err := os.Stat(filepath.Clean(name))
	if err != nil {
		return nil, fmt.Errorf("stat file %q: %w", name, err)
	}
	return info, nil
}

var (
	readFile = defaultReadFile
	statFile = defaultStatFile
)

func parseSecret(getenv func(string) string, envKey, fileEnvKey, defaultSecretPath string) (string, error) {
	filePath := strings.TrimSpace(getenv(fileEnvKey))
	if filePath == "" && defaultSecretPath != "" {
		if _, err := statFile(defaultSecretPath); err == nil {
			filePath = defaultSecretPath
		}
	}

	if filePath != "" {
		data, err := readFile(filePath)
		if err != nil {
			return "", err
		}
		secret := strings.TrimSpace(string(data))
		if secret == "" {
			return "", fmt.Errorf("secret file %q is empty", filePath)
		}
		return secret, nil
	}

	if val := strings.TrimSpace(getenv(envKey)); val != "" {
		return val, nil
	}

	return "", fmt.Errorf("missing required environment variable: %s (or %s)", envKey, fileEnvKey)
}

func parseABSURL(getenv func(string) string) (string, error) {
	absURL := strings.TrimSpace(getenv("ABSTP_ABS_URL"))
	if absURL == "" {
		return "", errors.New("ABSTP_ABS_URL is required")
	}
	if err := validator.ValidateHTTPURL(absURL); err != nil {
		return "", fmt.Errorf("invalid ABSTP_ABS_URL %q: %w", absURL, err)
	}
	return strings.TrimRight(absURL, "/"), nil
}

func parseListenAddr(getenv func(string) string) (string, error) {
	listenAddr := strings.TrimSpace(getenv("ABSTP_LISTEN_ADDR"))
	if listenAddr == "" {
		listenAddr = "127.0.0.1:8099"
	}
	if err := validator.ValidateListenAddr(listenAddr); err != nil {
		return "", fmt.Errorf("invalid ABSTP_LISTEN_ADDR %q: %w", listenAddr, err)
	}
	return listenAddr, nil
}

func parseExternalURL(getenv func(string) string) (string, error) {
	externalURL := strings.TrimSpace(getenv("ABSTP_EXTERNAL_URL"))
	if externalURL == "" {
		return "", nil
	}
	if err := validator.ValidateHTTPURL(externalURL); err != nil {
		return "", fmt.Errorf("invalid ABSTP_EXTERNAL_URL %q: %w", externalURL, err)
	}
	return strings.TrimRight(externalURL, "/"), nil
}

func parseFFmpegPath(getenv func(string) string, lookPath func(string) (string, error)) (string, error) {
	ffmpegPath := getenv("ABSTP_FFMPEG_PATH")
	if ffmpegPath == "" {
		ffmpegPath = "ffmpeg"
	}
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	if _, err := lookPath(ffmpegPath); err != nil {
		if ffmpegPath == "ffmpeg" {
			return "", errors.New("ffmpeg binary not found in PATH (install ffmpeg or set ABSTP_FFMPEG_PATH)")
		}
		return "", fmt.Errorf("ffmpeg binary not found at %q", ffmpegPath)
	}
	return ffmpegPath, nil
}

func parseTokenTTL(getenv func(string) string) (time.Duration, error) {
	raw := strings.TrimSpace(getenv("ABSTP_TOKEN_TTL"))
	if raw == "" {
		return 30 * time.Second, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		return 0, errors.New("invalid ABSTP_TOKEN_TTL: must be a valid positive duration (e.g. 30s, 1m)")
	}
	return d, nil
}

func parseBufferDuration(getenv func(string) string) (time.Duration, error) {
	raw := strings.TrimSpace(getenv("ABSTP_BUFFER_DURATION"))
	if raw == "" {
		return 10 * time.Second, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d < 5*time.Second {
		return 0, errors.New("invalid ABSTP_BUFFER_DURATION: must be a valid duration of at least 5s (e.g. 5s, 10s, 30s)")
	}
	return d, nil
}

func parseInDocker(getenv func(string) string) (bool, error) {
	val := getenv("ABSTP_IN_DOCKER")
	if val == "" {
		return false, nil
	}
	parsed, err := strconv.ParseBool(val)
	if err != nil {
		return false, fmt.Errorf("invalid ABSTP_IN_DOCKER %q: must be a boolean (true/false)", val)
	}
	return parsed, nil
}

func parseDebug(getenv func(string) string) (bool, error) {
	val := getenv("ABSTP_DEBUG")
	if val == "" {
		return false, nil
	}
	parsed, err := strconv.ParseBool(val)
	if err != nil {
		return false, fmt.Errorf("invalid ABSTP_DEBUG %q: must be a boolean (true/false)", val)
	}
	return parsed, nil
}

func parseDevReusableToken(getenv func(string) string) (bool, error) {
	val := getenv("ABSTP_DEV_REUSABLE_TOKEN")
	if val == "" {
		return false, nil
	}
	parsed, err := strconv.ParseBool(val)
	if err != nil {
		return false, fmt.Errorf("invalid ABSTP_DEV_REUSABLE_TOKEN %q: must be a boolean (true/false)", val)
	}
	return parsed, nil
}

func parseMaxConns(getenv func(string) string) (int, error) {
	raw := strings.TrimSpace(getenv("ABSTP_MAX_CONNS"))
	if raw == "" {
		return 100, nil
	}
	val, err := strconv.Atoi(raw)
	if err != nil || val < 50 || val > 1000 {
		return 0, errors.New("invalid ABSTP_MAX_CONNS: must be an integer between 50 and 1000")
	}
	return val, nil
}

func parseMaxStreams(getenv func(string) string) (int, error) {
	raw := strings.TrimSpace(getenv("ABSTP_MAX_STREAMS"))
	if raw == "" {
		return 5, nil
	}
	val, err := strconv.Atoi(raw)
	if err != nil || val < 1 || val > 20 {
		return 0, errors.New("invalid ABSTP_MAX_STREAMS: must be an integer between 1 and 20")
	}
	return val, nil
}

func parseCoreSettings(getenv func(string) string) (absURL, listenAddr, externalURL string, err error) {
	absURL, err = parseABSURL(getenv)
	if err != nil {
		return "", "", "", err
	}
	listenAddr, err = parseListenAddr(getenv)
	if err != nil {
		return "", "", "", err
	}
	externalURL, err = parseExternalURL(getenv)
	if err != nil {
		return "", "", "", err
	}
	return absURL, listenAddr, externalURL, nil
}

func parseSecretKeys(getenv func(string) string) (absToken, apiKey string, err error) {
	absToken, err = parseSecret(getenv, "ABSTP_ABS_TOKEN", "ABSTP_ABS_TOKEN_FILE", "/run/secrets/abstp_abs_token")
	if err != nil {
		return "", "", err
	}
	apiKey, err = parseSecret(getenv, "ABSTP_API_KEY", "ABSTP_API_KEY_FILE", "/run/secrets/abstp_api_key")
	if err != nil {
		return "", "", err
	}
	return absToken, apiKey, nil
}

func parseLimits(getenv func(string) string) (tokenTTL, bufferDuration time.Duration, maxConns, maxStreams int, err error) {
	tokenTTL, err = parseTokenTTL(getenv)
	if err != nil {
		return 0, 0, 0, 0, err
	}
	bufferDuration, err = parseBufferDuration(getenv)
	if err != nil {
		return 0, 0, 0, 0, err
	}
	maxConns, err = parseMaxConns(getenv)
	if err != nil {
		return 0, 0, 0, 0, err
	}
	maxStreams, err = parseMaxStreams(getenv)
	if err != nil {
		return 0, 0, 0, 0, err
	}
	return tokenTTL, bufferDuration, maxConns, maxStreams, nil
}

func parseFlags(getenv func(string) string, version string) (inDocker, debug, devReusableToken bool, err error) {
	inDocker, err = parseInDocker(getenv)
	if err != nil {
		return false, false, false, err
	}
	debug, err = parseDebug(getenv)
	if err != nil {
		return false, false, false, err
	}
	devReusableToken, err = parseDevReusableToken(getenv)
	if err != nil {
		return false, false, false, err
	}
	if devReusableToken && version != "dev" {
		return false, false, false, errors.New("ABSTP_DEV_REUSABLE_TOKEN is only permitted in dev builds")
	}
	return inDocker, debug, devReusableToken, nil
}

// Load reads and validates configuration values from the given environment getter function.
func Load(getenv func(string) string, lookPath func(string) (string, error), version string) (Config, error) {
	absURL, listenAddr, externalURL, err := parseCoreSettings(getenv)
	if err != nil {
		return Config{}, err
	}

	absToken, apiKey, err := parseSecretKeys(getenv)
	if err != nil {
		return Config{}, err
	}

	ffmpegPath, err := parseFFmpegPath(getenv, lookPath)
	if err != nil {
		return Config{}, err
	}

	tokenTTL, bufferDuration, maxConns, maxStreams, err := parseLimits(getenv)
	if err != nil {
		return Config{}, err
	}

	inDocker, debug, devReusableToken, err := parseFlags(getenv, version)
	if err != nil {
		return Config{}, err
	}

	return Config{
		ABSURL:           absURL,
		ABSToken:         absToken,
		APIKey:           apiKey,
		ListenAddr:       listenAddr,
		ExternalURL:      externalURL,
		FFmpegPath:       ffmpegPath,
		TokenTTL:         tokenTTL,
		BufferDuration:   bufferDuration,
		MaxConns:         maxConns,
		MaxStreams:       maxStreams,
		InDocker:         inDocker,
		Debug:            debug,
		DevReusableToken: devReusableToken,
	}, nil
}
