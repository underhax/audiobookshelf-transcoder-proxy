// Package ratelimit provides a rate-limited streaming writer designed for CBR audio transcoding.
package ratelimit

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// Rate limiting configuration defaults for CBR audio streaming (64 kbps target rate in ADTS container and 10-second initial burst).
const (
	DefaultBurstBytes     int64 = 83700
	DefaultBytesPerSecond int64 = 8370
	TickInterval                = 100 * time.Millisecond
)

// Writer wraps an io.Writer and rate-limits bytes transferred using a token bucket.
type Writer struct {
	lastRefill     time.Time
	ctx            context.Context
	w              io.Writer
	flusher        http.Flusher
	bytesTransf    atomic.Int64
	capacity       float64
	bytesPerSecond int64
	tokens         float64
	mu             sync.Mutex
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

	bucketCapacity := float64(burstBytes)
	minCap := max(1.0, float64(bytesPerSecond)/10.0)
	if bucketCapacity < minCap {
		bucketCapacity = minCap
	}

	return &Writer{
		ctx:            ctx,
		w:              w,
		flusher:        flusher,
		capacity:       bucketCapacity,
		bytesPerSecond: bytesPerSecond,
		tokens:         float64(burstBytes),
		lastRefill:     time.Now(),
	}
}

// BytesTransferred returns the total number of bytes written so far.
func (rw *Writer) BytesTransferred() int64 {
	return rw.bytesTransf.Load()
}

func (rw *Writer) refillTokens(now time.Time) {
	elapsed := now.Sub(rw.lastRefill).Seconds()
	if elapsed > 0 {
		rw.tokens = min(rw.capacity, rw.tokens+(elapsed*float64(rw.bytesPerSecond)))
		rw.lastRefill = now
	}
}

func (rw *Writer) waitDeficit(deficit float64) error {
	sleepDuration := time.Duration((deficit / float64(rw.bytesPerSecond)) * float64(time.Second))
	if sleepDuration <= 0 {
		sleepDuration = time.Millisecond
	}

	rw.mu.Unlock()
	defer rw.mu.Lock()

	timer := time.NewTimer(sleepDuration)
	defer timer.Stop()

	select {
	case <-rw.ctx.Done():
		return fmt.Errorf("context cancelled during throttle: %w", rw.ctx.Err())
	case <-timer.C:
		return nil
	}
}

// Write streams payload bytes, allowing burst delivery up to configured capacity and pacing subsequent writes.
func (rw *Writer) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}

	rw.mu.Lock()
	defer rw.mu.Unlock()

	total := len(p)
	written := 0

	for written < total {
		select {
		case <-rw.ctx.Done():
			return written, fmt.Errorf("context cancelled before write: %w", rw.ctx.Err())
		default:
		}

		rw.refillTokens(time.Now())

		if rw.tokens >= 1.0 {
			toWrite := min(total-written, int(rw.tokens))
			n, err := rw.w.Write(p[written : written+toWrite])
			if n > 0 {
				rw.bytesTransf.Add(int64(n))
				rw.tokens -= float64(n)
				written += n
				if rw.flusher != nil {
					rw.flusher.Flush()
				}
			}
			if err != nil {
				return written, fmt.Errorf("write throttled: %w", err)
			}
			continue
		}

		targetChunk := min(float64(total-written), max(1.0, float64(rw.bytesPerSecond)/10.0))
		deficit := max(1.0, targetChunk-rw.tokens)

		if err := rw.waitDeficit(deficit); err != nil {
			return written, err
		}
	}

	return written, nil
}

var _ io.Writer = (*Writer)(nil)

// ErrClosed indicates an attempt to write to an aborted or disconnected client stream.
var ErrClosed = errors.New("stream closed")
