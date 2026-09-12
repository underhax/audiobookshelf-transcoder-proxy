// Package ratelimit provides a rate-limited streaming writer designed for CBR audio transcoding.
package ratelimit

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync/atomic"
	"time"
)

// Rate limiting configuration defaults for CBR audio streaming (64 kbps target rate in ADTS container and 10-second initial burst).
const (
	DefaultBurstBytes     int64 = 83700
	DefaultBytesPerSecond int64 = 8370
	TickInterval                = 100 * time.Millisecond
)

// Writer wraps an io.Writer and rate-limits bytes transferred after the initial burst.
type Writer struct {
	ctx            context.Context
	w              io.Writer
	flusher        http.Flusher
	bytesTransf    atomic.Int64
	burstBytes     int64
	bytesPerSecond int64
}

// NewWriter returns a newly configured rate-limiting writer.
func NewWriter(ctx context.Context, w io.Writer, burstBytes, bytesPerSecond int64) *Writer {
	if burstBytes < 0 {
		burstBytes = DefaultBurstBytes
	}
	if bytesPerSecond <= 0 {
		bytesPerSecond = DefaultBytesPerSecond
	}

	var flusher http.Flusher
	if f, ok := w.(http.Flusher); ok {
		flusher = f
	}

	return &Writer{
		ctx:            ctx,
		w:              w,
		flusher:        flusher,
		burstBytes:     burstBytes,
		bytesPerSecond: bytesPerSecond,
	}
}

// BytesTransferred returns the total number of bytes written so far.
func (rw *Writer) BytesTransferred() int64 {
	return rw.bytesTransf.Load()
}

func (rw *Writer) writeBurst(p []byte, total int) (int, error) {
	current := rw.bytesTransf.Load()
	if current >= rw.burstBytes {
		return 0, nil
	}

	allowedBurst := int(rw.burstBytes - current)
	toWrite := min(total, allowedBurst)

	n, err := rw.w.Write(p[:toWrite])
	if n > 0 {
		rw.bytesTransf.Add(int64(n))
		if rw.flusher != nil {
			rw.flusher.Flush()
		}
	}
	if err != nil {
		return n, fmt.Errorf("write burst: %w", err)
	}

	return n, nil
}

func (rw *Writer) writeThrottled(p []byte, startWritten, total int) (int, error) {
	written := startWritten
	chunkSize := int((rw.bytesPerSecond * int64(TickInterval)) / int64(time.Second))
	if chunkSize <= 0 {
		chunkSize = 800
	}

	ticker := time.NewTicker(TickInterval)
	defer ticker.Stop()

	for written < total {
		select {
		case <-rw.ctx.Done():
			return written, fmt.Errorf("context cancelled during throttle: %w", rw.ctx.Err())
		case <-ticker.C:
			remaining := total - written
			toWrite := min(remaining, chunkSize)

			n, err := rw.w.Write(p[written : written+toWrite])
			if n > 0 {
				rw.bytesTransf.Add(int64(n))
				written += n
				if rw.flusher != nil {
					rw.flusher.Flush()
				}
			}
			if err != nil {
				return written, fmt.Errorf("write throttled: %w", err)
			}
		}
	}

	return written, nil
}

// Write streams payload bytes, allowing initial burst delivery and pacing subsequent chunks at the configured data rate.
func (rw *Writer) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}

	select {
	case <-rw.ctx.Done():
		return 0, fmt.Errorf("context cancelled before write: %w", rw.ctx.Err())
	default:
	}

	total := len(p)
	written, err := rw.writeBurst(p, total)
	if err != nil {
		return written, err
	}
	if written == total {
		return written, nil
	}

	return rw.writeThrottled(p, written, total)
}

var _ io.Writer = (*Writer)(nil)

// ErrClosed indicates an attempt to write to an aborted or disconnected client stream.
var ErrClosed = errors.New("stream closed")
