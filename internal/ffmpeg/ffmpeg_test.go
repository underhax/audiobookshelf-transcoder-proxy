package ffmpeg

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestBuildArgs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		wantSubstr []string
		notSubstr  []string
		name       string
		params     Params
	}{
		{
			name: "single track without seek or speed alteration",
			params: Params{
				FFmpegPath: "/opt/bin/ffmpeg-custom",
				InputPath:  "http://abs.example.org/audio.mp3",
				Version:    "1.2.3",
				Speed:      1.0,
				SeekOffset: 0.0,
				IsConcat:   false,
			},
			wantSubstr: []string{"-user_agent", "abstp/1.2.3", "-rw_timeout", "60000000", "-probesize", "32768", "-analyzeduration", "100000", "-i", "http://abs.example.org/audio.mp3", "-f", "adts"},
			notSubstr:  []string{"-f concat", "-ss", "atempo", "-headers"},
		},
		{
			name: "concat multi track with seek and custom speed without version",
			params: Params{
				FFmpegPath: "/usr/local/bin/ffmpeg-v2",
				InputPath:  "/tmp/concat.txt",
				Speed:      1.75,
				SeekOffset: 120.5,
				IsConcat:   true,
			},
			wantSubstr: []string{"-rw_timeout", "60000000", "-f", "concat", "-safe", "0", "-protocol_whitelist", "-ss", "120.50", "-filter:a", "atempo=1.75"},
			notSubstr:  []string{"-headers", "-user_agent"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			args := BuildArgs(&tt.params)
			for _, w := range tt.wantSubstr {
				if !slices.Contains(args, w) {
					t.Errorf("expected arg %q in %v", w, args)
				}
			}
			for _, nw := range tt.notSubstr {
				if slices.Contains(args, nw) {
					t.Errorf("did not expect arg %q in %v", nw, args)
				}
			}
		})
	}

	if nilArgs := BuildArgs(nil); nilArgs != nil {
		t.Errorf("expected nil args for nil params, got %v", nilArgs)
	}
}

func TestGenerateConcatFile_Success(t *testing.T) {
	t.Parallel()

	baseURL := "http://abs.example.net:13378/"
	tracks := []string{
		"/s/item/book1/ch'1.mp3",
		"/s/item/book1/ch2.mp3",
	}

	filePath, err := GenerateConcatFile(baseURL, tracks)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer func() {
		if removeErr := os.Remove(filePath); removeErr != nil {
			t.Errorf("remove error: %v", removeErr)
		}
	}()

	data, err := os.ReadFile(filepath.Clean(filePath))
	if err != nil {
		t.Fatalf("failed to read created file: %v", err)
	}

	content := string(data)
	if !strings.Contains(content, "file 'http://abs.example.net:13378/s/item/book1/ch'\\''1.mp3'") {
		t.Errorf("content does not contain escaped single quote: %s", content)
	}
	if !strings.Contains(content, "file 'http://abs.example.net:13378/s/item/book1/ch2.mp3'") {
		t.Errorf("content does not contain second file: %s", content)
	}
}

func TestGenerateConcatFile_Errors(t *testing.T) {
	origTemp := createTemp
	createTemp = func(_, _ string) (*os.File, error) {
		return nil, errors.New("cannot create temp file")
	}
	defer func() { createTemp = origTemp }()

	_, err := GenerateConcatFile("http://abs.example.com", []string{"/error-sample.mp3"})
	if err == nil {
		t.Fatal("expected error when createTemp fails")
	}

	closedTemp, tempErr := os.CreateTemp("", "test-closed-*.txt")
	if tempErr != nil {
		t.Fatalf("create temp: %v", tempErr)
	}
	name := closedTemp.Name()
	if closeErr := closedTemp.Close(); closeErr != nil {
		t.Fatalf("close temp: %v", closeErr)
	}
	defer func() {
		if rmErr := os.Remove(name); rmErr != nil && !errors.Is(rmErr, os.ErrNotExist) {
			t.Errorf("remove error: %v", rmErr)
		}
	}()

	createTemp = func(_, _ string) (*os.File, error) {
		return closedTemp, nil
	}

	if _, genErr := GenerateConcatFile("http://abs.example.com", []string{"/track.mp3"}); genErr == nil {
		t.Fatal("expected error writing to closed file")
	}

	tmpF, defaultErr := defaultCreateTemp("", "test-*.txt")
	if defaultErr != nil {
		t.Fatalf("default create temp failed: %v", defaultErr)
	}
	if closeErr := tmpF.Close(); closeErr != nil {
		t.Errorf("close error: %v", closeErr)
	}
	if removeErr := os.Remove(tmpF.Name()); removeErr != nil {
		t.Errorf("remove error: %v", removeErr)
	}

	if _, invalidDirErr := defaultCreateTemp("/non/existent/directory/123", "test-*.txt"); invalidDirErr == nil {
		t.Error("expected error creating temp in invalid directory")
	}
}

func TestStartProcess_Success(t *testing.T) {
	t.Parallel()

	cmd, stdout, stderr, startErr := StartProcess(context.Background(), &Params{
		FFmpegPath: "echo",
		InputPath:  "test",
		Speed:      1.0,
	})
	if startErr != nil {
		t.Fatalf("unexpected error starting process: %v", startErr)
	}
	if cmd == nil || stdout == nil || stderr == nil {
		t.Fatal("expected non-nil cmd, stdout and stderr")
	}
	if _, readErr := io.ReadAll(stdout); readErr != nil {
		t.Errorf("read stdout error: %v", readErr)
	}
	if closeErr := stdout.Close(); closeErr != nil {
		t.Errorf("close stdout error: %v", closeErr)
	}
	if waitErr := cmd.Wait(); waitErr != nil {
		t.Errorf("wait error: %v", waitErr)
	}
}

func TestStartProcess_Errors(t *testing.T) {
	if _, _, _, nilErr := StartProcess(context.Background(), nil); nilErr == nil {
		t.Fatal("expected error for nil params")
	}

	if _, _, _, invalidCmdErr := StartProcess(context.Background(), &Params{
		FFmpegPath: "nonexistent_binary_xyz_123",
	}); invalidCmdErr == nil {
		t.Fatal("expected error looking up non-existent binary")
	}

	t.Setenv("PATH", t.TempDir())
	if _, _, _, defErr := StartProcess(context.Background(), &Params{
		FFmpegPath: "ffmpeg",
	}); defErr == nil {
		t.Fatal("expected error starting ffmpeg when not in PATH")
	}
}

func TestStartProcess_StdoutPipeError(t *testing.T) {
	origCmd := commandContext
	commandContext = func(ctx context.Context) *exec.Cmd {
		cmd := exec.CommandContext(ctx, "true")
		cmd.Stdout = io.Discard
		return cmd
	}
	defer func() { commandContext = origCmd }()

	if _, _, _, pipeErr := StartProcess(context.Background(), &Params{
		FFmpegPath: "true",
	}); pipeErr == nil {
		t.Fatal("expected error creating stdout pipe when Stdout is already set")
	}

	defCmd := defaultCommandContext(context.Background())
	if defCmd == nil {
		t.Fatal("expected non-nil cmd from defaultCommandContext")
	}
}

func TestStartProcess_StartError(t *testing.T) {
	origCmd := commandContext
	commandContext = func(ctx context.Context) *exec.Cmd {
		cmd := exec.CommandContext(ctx, "false")
		cmd.Dir = "/non/existent/directory/xyz/123"
		return cmd
	}
	defer func() { commandContext = origCmd }()

	if _, _, _, err := StartProcess(context.Background(), &Params{FFmpegPath: "false"}); err == nil {
		t.Fatal("expected error starting command with invalid dir")
	}
}

func TestLimitedWriter(t *testing.T) {
	t.Parallel()

	buf := &bytes.Buffer{}
	lw := &limitedWriter{buf: buf, limit: 10}

	n, err := lw.Write([]byte("hello"))
	if err != nil || n != 5 || buf.String() != "hello" {
		t.Fatalf("unexpected write result: n=%d err=%v buf=%s", n, err, buf.String())
	}

	n, err = lw.Write([]byte("world-extra"))
	if err != nil || n != 5 || buf.String() != "helloworld" {
		t.Fatalf("unexpected write result exceeding limit: n=%d err=%v buf=%s", n, err, buf.String())
	}

	n, err = lw.Write([]byte("more"))
	if err != nil || n != 4 || buf.String() != "helloworld" {
		t.Fatalf("unexpected write result when full: n=%d err=%v buf=%s", n, err, buf.String())
	}
}

func TestBuildMediaURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		baseURL  string
		trackURL string
		want     string
	}{
		{
			name:     "already absolute http URL",
			baseURL:  "http://abs.example.org",
			trackURL: "http://other.example.org/track.mp3",
			want:     "http://other.example.org/track.mp3",
		},
		{
			name:     "already absolute https URL",
			baseURL:  "https://abs.example.org",
			trackURL: "https://secure.example.org/track.mp3",
			want:     "https://secure.example.org/track.mp3",
		},
		{
			name:     "root base URL with leading slash track",
			baseURL:  "http://abs.example.org",
			trackURL: "/s/item/book-1/track.mp3",
			want:     "http://abs.example.org/s/item/book-1/track.mp3",
		},
		{
			name:     "root base URL with trailing slash and no leading slash track",
			baseURL:  "http://abs.example.org/",
			trackURL: "s/item/book-1/track.mp3",
			want:     "http://abs.example.org/s/item/book-1/track.mp3",
		},
		{
			name:     "subpath base URL with relative track",
			baseURL:  "https://abs.example.org/audiobookshelf",
			trackURL: "/s/item/book-1/track.mp3",
			want:     "https://abs.example.org/audiobookshelf/s/item/book-1/track.mp3",
		},
		{
			name:     "subpath base URL with track that already contains subpath prefix",
			baseURL:  "https://abs.example.org/audiobookshelf/",
			trackURL: "/audiobookshelf/s/item/book-1/track.mp3",
			want:     "https://abs.example.org/audiobookshelf/s/item/book-1/track.mp3",
		},
		{
			name:     "subpath base URL where track is exactly subpath",
			baseURL:  "https://abs.example.com/audiobookshelf",
			trackURL: "/audiobookshelf",
			want:     "https://abs.example.com/audiobookshelf",
		},
		{
			name:     "invalid base URL falls back cleanly",
			baseURL:  "http://[::1]:invalidport",
			trackURL: "/fallback.mp3",
			want:     "http://[::1]:invalidport/fallback.mp3",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := BuildMediaURL(tt.baseURL, tt.trackURL)
			if got != tt.want {
				t.Errorf("BuildMediaURL(%q, %q) = %q, want %q", tt.baseURL, tt.trackURL, got, tt.want)
			}
		})
	}
}
