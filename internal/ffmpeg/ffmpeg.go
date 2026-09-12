// Package ffmpeg manages building concat lists and spawning the FFmpeg transcoding process.
package ffmpeg

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// Params holds the parameters required to spawn an FFmpeg transcoding pipeline.
type Params struct {
	FFmpegPath string
	InputPath  string
	Speed      float64
}

// BuildArgs constructs the slice of command-line arguments passed to FFmpeg.
func BuildArgs(params *Params) []string {
	if params == nil {
		return nil
	}

	args := []string{
		"-hide_banner",
		"-loglevel", "error",
		"-rw_timeout", "60000000",
		"-probesize", "32768",
		"-analyzeduration", "100000",
		"-f", "concat",
		"-safe", "0",
		"-protocol_whitelist", "file,http,https,tcp,tls",
		"-i", params.InputPath,
	}

	if params.Speed != 1.0 && params.Speed > 0 {
		args = append(args, "-filter:a", "atempo="+strconv.FormatFloat(params.Speed, 'f', 2, 64))
	}

	args = append(args,
		"-f", "adts",
		"-c:a", "aac",
		"-b:a", "64k",
		"-flush_packets", "1",
		"pipe:1",
	)

	return args
}

func defaultCommandContext(ctx context.Context) *exec.Cmd {
	return exec.CommandContext(ctx, "ffmpeg")
}

var commandContext = defaultCommandContext

// MaxStderrBytes limits the captured ffmpeg stderr to prevent unbounded memory growth.
const MaxStderrBytes = 4096

// StartProcess spawns the FFmpeg process with stdout piped for audio streaming and stderr captured for diagnostics.
func StartProcess(ctx context.Context, params *Params) (cmd *exec.Cmd, stdout io.ReadCloser, stderr *bytes.Buffer, err error) {
	if params == nil {
		return nil, nil, nil, errors.New("params cannot be nil")
	}

	args := BuildArgs(params)

	binName := "ffmpeg"
	if params.FFmpegPath != "" {
		binName = params.FFmpegPath
	}

	binPath, lookErr := exec.LookPath(binName)
	if lookErr != nil {
		return nil, nil, nil, fmt.Errorf("lookup binary %s: %w", binName, lookErr)
	}

	cmd = commandContext(ctx)
	cmd.Path = filepath.Clean(binPath)
	cmd.Args = append([]string{binName}, args...)
	cmd.Err = nil

	stderrBuf := bytes.NewBuffer(make([]byte, 0, MaxStderrBytes))
	cmd.Stderr = &limitedWriter{buf: stderrBuf, limit: MaxStderrBytes}

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("create stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		closeErr := stdoutPipe.Close()
		return nil, nil, nil, errors.Join(
			fmt.Errorf("start ffmpeg process: %w", err),
			closeErr,
		)
	}

	return cmd, stdoutPipe, stderrBuf, nil
}

type limitedWriter struct {
	buf   *bytes.Buffer
	limit int
}

func (lw *limitedWriter) Write(p []byte) (int, error) {
	remaining := lw.limit - lw.buf.Len()
	if remaining <= 0 {
		return len(p), nil
	}
	if len(p) > remaining {
		p = p[:remaining]
	}
	_, _ = lw.buf.Write(p)
	return len(p), nil
}

func defaultCreateTemp(dir, pattern string) (*os.File, error) {
	f, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return nil, fmt.Errorf("create temp file: %w", err)
	}
	return f, nil
}

var createTemp = defaultCreateTemp

// BuildMediaURL constructs an absolute HTTP(S) URL for an audio track, supporting both root and subpath reverse proxies.
func BuildMediaURL(baseURL, trackURL string) string {
	if strings.HasPrefix(trackURL, "http://") || strings.HasPrefix(trackURL, "https://") {
		return trackURL
	}

	cleanBase := strings.TrimRight(baseURL, "/")
	cleanTrack := trackURL
	if !strings.HasPrefix(cleanTrack, "/") {
		cleanTrack = "/" + cleanTrack
	}

	parsedBase, err := url.Parse(cleanBase)
	if err != nil || parsedBase.Path == "" || parsedBase.Path == "/" {
		return cleanBase + cleanTrack
	}

	basePath := strings.TrimRight(parsedBase.Path, "/")
	if strings.HasPrefix(cleanTrack, basePath+"/") || cleanTrack == basePath {
		origin := parsedBase.Scheme + "://" + parsedBase.Host
		return origin + cleanTrack
	}

	return cleanBase + cleanTrack
}

// GenerateConcatFile creates a temporary file compatible with FFmpeg concat demuxer listing all tracks from startIndex.
func GenerateConcatFile(baseURL string, trackURLs []string, inpoint float64) (filePath string, err error) {
	tmpFile, err := createTemp("", "abstp-concat-*.txt")
	if err != nil {
		return "", err
	}
	tmpPath := tmpFile.Name()

	var sb strings.Builder
	for i, u := range trackURLs {
		fullURL := BuildMediaURL(baseURL, u)
		escaped := strings.ReplaceAll(fullURL, "'", "'\\''")
		sb.WriteString("file '")
		sb.WriteString(escaped)
		sb.WriteString("'\n")
		if i == 0 && inpoint > 0 {
			sb.WriteString("inpoint ")
			sb.WriteString(strconv.FormatFloat(inpoint, 'f', 2, 64))
			sb.WriteString("\n")
		}
	}

	_, writeErr := tmpFile.WriteString(sb.String())
	closeErr := tmpFile.Close()
	if writeErr != nil || closeErr != nil {
		removeErr := os.Remove(tmpPath)
		return "", errors.Join(
			fmt.Errorf("write or close concat file: %w", writeErr),
			closeErr,
			removeErr,
		)
	}

	return tmpPath, nil
}
