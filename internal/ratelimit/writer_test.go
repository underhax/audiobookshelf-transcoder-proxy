package ratelimit

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

type mockFlushWriter struct {
	errOnWrite error
	buf        bytes.Buffer
	flushCount atomic.Int32
}

func (m *mockFlushWriter) Write(p []byte) (int, error) {
	if m.errOnWrite != nil {
		return 0, m.errOnWrite
	}
	return m.buf.Write(p)
}

func (m *mockFlushWriter) Flush() {
	m.flushCount.Add(1)
}

var _ http.Flusher = (*mockFlushWriter)(nil)

func TestNewWriter_Defaults(t *testing.T) {
	t.Parallel()

	mw := &mockFlushWriter{}
	w := NewWriter(context.Background(), mw, -1, 0)
	if w == nil {
		t.Fatal("expected non-nil writer")
	}
	if w.BytesTransferred() != 0 {
		t.Errorf("expected 0 bytes transferred, got %d", w.BytesTransferred())
	}
}

func TestWriter_BurstOnly(t *testing.T) {
	t.Parallel()

	mw := &mockFlushWriter{}
	w := NewWriter(context.Background(), mw, 100, 1000)

	n, err := w.Write(nil)
	if err != nil || n != 0 {
		t.Errorf("expected 0, nil for empty write; got %d, %v", n, err)
	}

	data := make([]byte, 50)
	n, err = w.Write(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 50 {
		t.Errorf("expected 50 bytes, got %d", n)
	}
	if w.BytesTransferred() != 50 {
		t.Errorf("expected 50 transferred, got %d", w.BytesTransferred())
	}
	if mw.flushCount.Load() == 0 {
		t.Error("expected flush to be called")
	}
}

func TestWriter_BurstError(t *testing.T) {
	t.Parallel()

	mw := &mockFlushWriter{errOnWrite: errors.New("burst write failed")}
	w := NewWriter(context.Background(), mw, 100, 1000)

	_, err := w.Write([]byte("hello"))
	if err == nil {
		t.Fatal("expected error on burst write")
	}
}

func TestWriter_ThrottleWithSmallInterval(t *testing.T) {
	t.Parallel()

	mw := &mockFlushWriter{}
	w := NewWriter(context.Background(), mw, 10, 200)

	data := make([]byte, 30)
	start := time.Now()
	n, err := w.Write(data)
	duration := time.Since(start)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 30 {
		t.Errorf("expected 30 bytes, got %d", n)
	}
	if w.BytesTransferred() != 30 {
		t.Errorf("expected 30 transferred, got %d", w.BytesTransferred())
	}
	if duration < 50*time.Millisecond {
		t.Errorf("expected some throttling delay, got %v", duration)
	}

	n2, err2 := w.Write([]byte("more"))
	if err2 != nil {
		t.Fatalf("unexpected error on second write: %v", err2)
	}
	if n2 != 4 {
		t.Errorf("expected 4 bytes, got %d", n2)
	}
}

func TestWriter_PauseBurstReplenishment(t *testing.T) {
	t.Parallel()

	mw := &mockFlushWriter{}
	w := NewWriter(context.Background(), mw, 50, 1000)

	initial := make([]byte, 50)
	if _, err := w.Write(initial); err != nil {
		t.Fatal(err)
	}

	time.Sleep(60 * time.Millisecond)

	second := make([]byte, 50)
	start := time.Now()
	if _, err := w.Write(second); err != nil {
		t.Fatal(err)
	}
	duration := time.Since(start)

	if duration > 30*time.Millisecond {
		t.Errorf("expected burst write after pause, but took %v", duration)
	}
}

func TestWriter_ThrottleContextCancelled(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	mw := &mockFlushWriter{}
	w := NewWriter(ctx, mw, 5, 10)

	go func() {
		time.Sleep(30 * time.Millisecond)
		cancel()
	}()

	data := make([]byte, 50)
	_, err := w.Write(data)
	if err == nil {
		t.Fatal("expected context cancelled error")
	}
}

type errAfterWriter struct {
	calls atomic.Int32
}

func (e *errAfterWriter) Write(p []byte) (int, error) {
	if e.calls.Add(1) > 1 {
		return 0, errors.New("throttled write error")
	}
	return len(p), nil
}

func TestWriter_ThrottleWriteError(t *testing.T) {
	t.Parallel()

	ew := &errAfterWriter{}
	w := NewWriter(t.Context(), ew, 5, 10000)

	data := make([]byte, 25)
	_, err := w.Write(data)
	if err == nil {
		t.Fatal("expected throttled write error")
	}
}

func TestWriter_WriteContextCancelled(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	mw := &mockFlushWriter{}
	w := NewWriter(ctx, mw, 10, 1000)

	_, err := w.Write([]byte("hello"))
	if err == nil {
		t.Fatal("expected context cancelled error on write")
	}
}

func TestWriter_ThrottleFallbackChunkSize(t *testing.T) {
	t.Parallel()

	mw := &mockFlushWriter{}
	w := NewWriter(context.Background(), mw, 0, 1000)

	n, err := w.Write([]byte("test"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 4 {
		t.Errorf("expected 4 bytes written, got %d", n)
	}
}

func TestWriter_WaitDeficitZero(t *testing.T) {
	t.Parallel()

	mw := &mockFlushWriter{}
	w := NewWriter(context.Background(), mw, 10, 100)
	w.mu.Lock()
	err := w.waitDeficit(0)
	w.mu.Unlock()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
