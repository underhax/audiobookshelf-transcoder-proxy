package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

type dummyFileInfo struct {
	os.FileInfo
}

type secretTestCase struct {
	envVar      string
	fileVar     string
	defaultPath string
	fileData    string
	defaultData string
	isToken     bool
}

var secretCases = []secretTestCase{
	{
		envVar:      "ABSTP_ABS_TOKEN",
		fileVar:     "ABSTP_ABS_TOKEN_FILE",
		defaultPath: "/run/secrets/abstp_abs_token",
		fileData:    "custom_file_content_alpha",
		defaultData: "default_path_content_alpha",
		isToken:     true,
	},
	{
		envVar:      "ABSTP_API_KEY",
		fileVar:     "ABSTP_API_KEY_FILE",
		defaultPath: "/run/secrets/abstp_api_key",
		fileData:    "custom_file_content_beta",
		defaultData: "default_path_content_beta",
		isToken:     false,
	},
}

func setMockReadFile(fn func(name string) ([]byte, error)) func() {
	orig := readFile
	readFile = fn
	return func() {
		readFile = orig
	}
}

func setMockStatFile(fn func(name string) (os.FileInfo, error)) func() {
	orig := statFile
	statFile = fn
	return func() {
		statFile = orig
	}
}

func TestLoad_SecretsFromFile(t *testing.T) {
	for _, sc := range secretCases {
		t.Run("from file "+sc.fileVar, func(t *testing.T) {
			filePath := "/tmp/" + sc.fileVar + ".txt"
			restoreRead := setMockReadFile(func(name string) ([]byte, error) {
				if name == filePath {
					return []byte(sc.fileData + "\n"), nil
				}
				return nil, errors.New("not found")
			})
			defer restoreRead()

			env := func(k string) string {
				if k == sc.fileVar {
					return filePath
				}
				return mockEnvValue("", "", k)
			}

			cfg, err := Load(env, mockLookPathSuccess, "dev")
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			val := cfg.APIKey
			if sc.isToken {
				val = cfg.ABSToken
			}
			if val != sc.fileData {
				t.Errorf("expected %q, got %q", sc.fileData, val)
			}
		})
	}
}

func TestLoad_SecretsFromDefaultPath(t *testing.T) {
	for _, sc := range secretCases {
		t.Run("from default path "+sc.defaultPath, func(t *testing.T) {
			restoreStat := setMockStatFile(func(name string) (os.FileInfo, error) {
				if name == sc.defaultPath {
					return dummyFileInfo{}, nil
				}
				return nil, os.ErrNotExist
			})
			defer restoreStat()

			restoreRead := setMockReadFile(func(name string) ([]byte, error) {
				if name == sc.defaultPath {
					return []byte(sc.defaultData + "\n"), nil
				}
				return nil, errors.New("not found")
			})
			defer restoreRead()

			env := func(k string) string {
				return mockEnvValue("", "", k)
			}

			cfg, err := Load(env, mockLookPathSuccess, "dev")
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			val := cfg.APIKey
			if sc.isToken {
				val = cfg.ABSToken
			}
			if val != sc.defaultData {
				t.Errorf("expected %q, got %q", sc.defaultData, val)
			}
		})
	}
}

func TestLoad_SecretsMissing(t *testing.T) {
	restoreStat := setMockStatFile(func(_ string) (os.FileInfo, error) {
		return nil, os.ErrNotExist
	})
	defer restoreStat()

	for _, sc := range secretCases {
		t.Run("missing "+sc.envVar, func(t *testing.T) {
			env := func(k string) string {
				if k == sc.envVar {
					return ""
				}
				return mockEnvValue("", "", k)
			}

			if _, err := Load(env, mockLookPathSuccess, "dev"); err == nil {
				t.Errorf("expected error for missing %s", sc.envVar)
			}
		})
	}
}

func TestLoad_SecretFileErrors(t *testing.T) {
	restoreStat := setMockStatFile(func(_ string) (os.FileInfo, error) {
		return nil, os.ErrNotExist
	})
	defer restoreStat()

	tests := []struct {
		readFile func(string) ([]byte, error)
		name     string
		sc       secretTestCase
	}{
		{
			name: "file read error",
			sc:   secretCases[0],
			readFile: func(_ string) ([]byte, error) {
				return nil, errors.New("read error")
			},
		},
		{
			name: "empty file error",
			sc:   secretCases[1],
			readFile: func(_ string) ([]byte, error) {
				return []byte("   \n\t "), nil
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			restoreRead := setMockReadFile(tt.readFile)
			defer restoreRead()

			env := func(k string) string {
				if k == tt.sc.envVar {
					return ""
				}
				if k == tt.sc.fileVar {
					return "/secrets/file.txt"
				}
				return mockEnvValue("", "", k)
			}

			if _, err := Load(env, mockLookPathSuccess, "dev"); err == nil {
				t.Error("expected error on secret failure")
			}
		})
	}
}

func TestDefaultFileOperations(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "secret.txt")
	if err := os.WriteFile(testFile, []byte("test-data"), 0o600); err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}

	data, err := defaultReadFile(testFile)
	if err != nil || string(data) != "test-data" {
		t.Errorf("unexpected read result: data=%q, err=%v", string(data), err)
	}

	info, err := defaultStatFile(testFile)
	if err != nil || info == nil {
		t.Errorf("unexpected stat result: info=%v, err=%v", info, err)
	}

	if _, err := defaultReadFile(filepath.Join(tmpDir, "missing.txt")); err == nil {
		t.Error("expected error for non-existent file read")
	}
	if _, err := defaultStatFile(filepath.Join(tmpDir, "missing.txt")); err == nil {
		t.Error("expected error for non-existent file stat")
	}
}
