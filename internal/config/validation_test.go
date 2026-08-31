package config

import (
	"testing"
)

func TestLoad_URLValidation(t *testing.T) {
	t.Parallel()

	for _, val := range []string{"", "ftp://abs.example.net", "http://"} {
		t.Run("abs_url_"+val, func(t *testing.T) {
			env := func(k string) string {
				return mockEnvValue("ABSTP_ABS_URL", val, k)
			}
			if _, err := Load(env, mockLookPathSuccess, "dev"); err == nil {
				t.Errorf("expected error for ABS URL %q", val)
			}
		})
	}

	for _, val := range []string{"://broken.example.org", "ftp://proxy.example.org"} {
		t.Run("ext_url_"+val, func(t *testing.T) {
			env := func(k string) string {
				return mockEnvValue("ABSTP_EXTERNAL_URL", val, k)
			}
			if _, err := Load(env, mockLookPathSuccess, "dev"); err == nil {
				t.Errorf("expected error for external URL %q", val)
			}
		})
	}
}

func TestLoad_ListenAddrValidation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		val   string
		valid bool
	}{
		{name: "invalid split", val: "invalid-address"},
		{name: "port too low", val: "127.0.0.1:80"},
		{name: "port not int", val: "127.0.0.1:abc"},
		{name: "invalid host", val: "not-an-ip:8099"},
		{name: "empty host is valid", val: ":8099", valid: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			env := func(k string) string {
				return mockEnvValue("ABSTP_LISTEN_ADDR", tt.val, k)
			}
			_, err := Load(env, mockLookPathSuccess, "dev")
			if tt.valid {
				if err != nil {
					t.Fatalf("expected valid config for empty host, got: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error for %s, got nil", tt.name)
			}
		})
	}
}

func TestLoad_FFmpegValidation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		lookPath func(string) (string, error)
		name     string
		val      string
	}{
		{name: "default binary missing", val: "", lookPath: mockLookPathFail},
		{name: "custom binary missing", val: "/opt/custom/ffmpeg", lookPath: mockLookPathFail},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			env := func(k string) string {
				return mockEnvValue("ABSTP_FFMPEG_PATH", tt.val, k)
			}
			if _, err := Load(env, tt.lookPath, "dev"); err == nil {
				t.Errorf("expected error for %s", tt.name)
			}
		})
	}
}

func TestLoad_DurationValidation(t *testing.T) {
	t.Parallel()

	for _, val := range []string{"invalid-ttl", "-10s", "0s"} {
		t.Run("ttl_"+val, func(t *testing.T) {
			env := func(k string) string {
				return mockEnvValue("ABSTP_TOKEN_TTL", val, k)
			}
			if _, err := Load(env, mockLookPathSuccess, "dev"); err == nil {
				t.Errorf("expected error for TTL %q", val)
			}
		})
	}

	for _, val := range []string{"invalid-buf", "4s", "-1s"} {
		t.Run("buf_"+val, func(t *testing.T) {
			env := func(k string) string {
				return mockEnvValue("ABSTP_BUFFER_DURATION", val, k)
			}
			if _, err := Load(env, mockLookPathSuccess, "dev"); err == nil {
				t.Errorf("expected error for buffer %q", val)
			}
		})
	}
}

func TestLoad_BooleanValidation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		envKey string
		val    string
	}{
		{name: "invalid in docker", envKey: "ABSTP_IN_DOCKER", val: "bad-bool-1"},
		{name: "invalid debug", envKey: "ABSTP_DEBUG", val: "bad-bool-2"},
		{name: "invalid dev reusable", envKey: "ABSTP_DEV_REUSABLE_TOKEN", val: "bad-bool-3"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			env := func(k string) string {
				return mockEnvValue(tt.envKey, tt.val, k)
			}
			if _, err := Load(env, mockLookPathSuccess, "dev"); err == nil {
				t.Errorf("expected error for %s", tt.name)
			}
		})
	}
}
