package config

import (
	"errors"
	"testing"
	"time"
)

var defaultEnvMap = map[string]string{
	"ABSTP_ABS_URL":      "http://abs.example.com:13378",
	"ABSTP_ABS_TOKEN":    "sample_abs_token",
	"ABSTP_API_KEY":      "sample_api_key",
	"ABSTP_LISTEN_ADDR":  "127.0.0.1:8100",
	"ABSTP_EXTERNAL_URL": "http://proxy.example.org:8099",
	"ABSTP_FFMPEG_PATH":  "/usr/local/bin/ffmpeg",
}

func mockEnvValue(targetKey, targetVal, currentKey string) string {
	if currentKey == targetKey {
		return targetVal
	}
	return defaultEnvMap[currentKey]
}

func mockLookPathSuccess(file string) (string, error) {
	return "/usr/bin/" + file, nil
}

func mockLookPathFail(_ string) (string, error) {
	return "", errors.New("binary not found")
}

func TestLoad_Success(t *testing.T) {
	t.Parallel()

	validEnv := func(k string) string {
		return mockEnvValue("", "", k)
	}

	cfg, err := Load(validEnv, mockLookPathSuccess, "dev")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	checks := []struct {
		got  string
		want string
		desc string
	}{
		{got: cfg.ABSURL, want: "http://abs.example.com:13378", desc: "abs url"},
		{got: cfg.ABSToken, want: "sample_abs_token", desc: "abs token"},
		{got: cfg.APIKey, want: "sample_api_key", desc: "api key"},
		{got: cfg.ListenAddr, want: "127.0.0.1:8100", desc: "listen addr"},
		{got: cfg.ExternalURL, want: "http://proxy.example.org:8099", desc: "external url"},
		{got: cfg.FFmpegPath, want: "/usr/local/bin/ffmpeg", desc: "ffmpeg path"},
	}

	for _, tc := range checks {
		if tc.got != tc.want {
			t.Errorf("expected %s %s, got %q", tc.desc, tc.want, tc.got)
		}
	}

	envWithValidCmd := func(key string) string {
		return mockEnvValue("ABSTP_FFMPEG_PATH", "go", key)
	}
	if _, err := Load(envWithValidCmd, nil, "dev"); err != nil {
		t.Errorf("unexpected error with default lookPath: %v", err)
	}
}

func TestLoad_Defaults(t *testing.T) {
	t.Parallel()

	env := func(key string) string {
		if key == "ABSTP_LISTEN_ADDR" || key == "ABSTP_EXTERNAL_URL" || key == "ABSTP_FFMPEG_PATH" {
			return ""
		}
		return mockEnvValue("", "", key)
	}

	cfg, err := Load(env, mockLookPathSuccess, "dev")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	checks := []struct {
		got  string
		want string
		desc string
	}{
		{got: cfg.ListenAddr, want: "127.0.0.1:8099", desc: "listen addr"},
		{got: cfg.ExternalURL, want: "", desc: "external url"},
		{got: cfg.FFmpegPath, want: "ffmpeg", desc: "ffmpeg path"},
	}

	for _, tc := range checks {
		if tc.got != tc.want {
			t.Errorf("expected default %s %s, got %q", tc.desc, tc.want, tc.got)
		}
	}
	if cfg.TokenTTL != 30*time.Second {
		t.Errorf("expected default TokenTTL 30s, got %v", cfg.TokenTTL)
	}
	if cfg.BufferDuration != 10*time.Second {
		t.Errorf("expected default BufferDuration 10s, got %v", cfg.BufferDuration)
	}
	if cfg.MaxConns != 100 {
		t.Errorf("expected default MaxConns 100, got %d", cfg.MaxConns)
	}
	if cfg.MaxStreams != 5 {
		t.Errorf("expected default MaxStreams 5, got %d", cfg.MaxStreams)
	}
	if cfg.InDocker {
		t.Errorf("expected default InDocker false, got true")
	}
}

func TestLoad_CustomOptions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		verify func(cfg Config) error
		name   string
		key    string
		val    string
	}{
		{
			name: "custom token ttl",
			key:  "ABSTP_TOKEN_TTL",
			val:  "2m",
			verify: func(cfg Config) error {
				if cfg.TokenTTL != 2*time.Minute {
					return errors.New("expected 2m TokenTTL")
				}
				return nil
			},
		},
		{
			name: "custom buffer duration",
			key:  "ABSTP_BUFFER_DURATION",
			val:  "15s",
			verify: func(cfg Config) error {
				if cfg.BufferDuration != 15*time.Second {
					return errors.New("expected 15s BufferDuration")
				}
				return nil
			},
		},
		{
			name: "custom max conns",
			key:  "ABSTP_MAX_CONNS",
			val:  "250",
			verify: func(cfg Config) error {
				if cfg.MaxConns != 250 {
					return errors.New("expected 250 MaxConns")
				}
				return nil
			},
		},
		{
			name: "custom max streams",
			key:  "ABSTP_MAX_STREAMS",
			val:  "12",
			verify: func(cfg Config) error {
				if cfg.MaxStreams != 12 {
					return errors.New("expected 12 MaxStreams")
				}
				return nil
			},
		},
		{
			name: "in docker flag",
			key:  "ABSTP_IN_DOCKER",
			val:  "1",
			verify: func(cfg Config) error {
				if !cfg.InDocker {
					return errors.New("expected InDocker true")
				}
				return nil
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			env := func(k string) string {
				return mockEnvValue(tt.key, tt.val, k)
			}
			cfg, err := Load(env, mockLookPathSuccess, "dev")
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if err := tt.verify(cfg); err != nil {
				t.Error(err)
			}
		})
	}
}

func TestLoad_DebugAndReusableToken(t *testing.T) {
	t.Parallel()

	env := func(key string) string {
		switch key {
		case "ABSTP_DEBUG":
			return "true"
		case "ABSTP_DEV_REUSABLE_TOKEN":
			return "true"
		default:
			return mockEnvValue("", "", key)
		}
	}

	cfg, err := Load(env, mockLookPathSuccess, "dev")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !cfg.Debug {
		t.Errorf("expected Debug true, got false")
	}
	if !cfg.DevReusableToken {
		t.Errorf("expected DevReusableToken true, got false")
	}

	if _, errRelease := Load(env, mockLookPathSuccess, "1.0.0"); errRelease == nil {
		t.Error("expected error when DevReusableToken is used in non-dev build")
	}
}
