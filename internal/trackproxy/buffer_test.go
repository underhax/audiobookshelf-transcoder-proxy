package trackproxy

import (
	"bytes"
	"errors"
	"io"
	"sync"
	"testing"
	"time"
)

func TestReadAheadBuffer_SequentialReadWrite(t *testing.T) {
	t.Parallel()

	b := newReadAheadBuffer(100)
	if b.Buffered() != 0 {
		t.Errorf("expected 0 buffered, got %d", b.Buffered())
	}

	data := []byte("hello world")
	n, err := b.Write(data)
	if err != nil || n != len(data) {
		t.Fatalf("unexpected write: n=%d, err=%v", n, err)
	}
	if b.Buffered() != len(data) {
		t.Errorf("expected %d buffered, got %d", len(data), b.Buffered())
	}

	out := make([]byte, len(data))
	nr, err := b.Read(out)
	if err != nil || nr != len(data) {
		t.Fatalf("unexpected read: nr=%d, err=%v", nr, err)
	}
	if !bytes.Equal(out, data) {
		t.Errorf("got %q, want %q", out, data)
	}
	if b.Buffered() != 0 {
		t.Errorf("expected 0 buffered after read, got %d", b.Buffered())
	}
}

func TestReadAheadBuffer_WrapAround(t *testing.T) {
	t.Parallel()

	b := newReadAheadBuffer(10)
	first := []byte("1234567")
	if _, err := b.Write(first); err != nil {
		t.Fatal(err)
	}

	discard := make([]byte, 5)
	if _, err := b.Read(discard); err != nil {
		t.Fatal(err)
	}

	second := []byte("abcdef")
	if _, err := b.Write(second); err != nil {
		t.Fatal(err)
	}

	readAll := make([]byte, 8)
	nr, err := b.Read(readAll)
	if err != nil {
		t.Fatal(err)
	}
	if nr != 8 {
		t.Errorf("expected 8 bytes read, got %d", nr)
	}
	expected := []byte("67abcdef")
	if !bytes.Equal(readAll[:nr], expected) {
		t.Errorf("got %q, want %q", readAll[:nr], expected)
	}
}

func TestReadAheadBuffer_BlockingFullAndEmpty(t *testing.T) {
	t.Parallel()

	b := newReadAheadBuffer(10)
	var wg sync.WaitGroup

	payload := []byte("0123456789extra")
	readCh := make(chan []byte, 1)

	wg.Go(func() {
		var received bytes.Buffer
		buf := make([]byte, 8)
		for received.Len() < len(payload) {
			n, err := b.Read(buf)
			if err != nil {
				t.Errorf("read error: %v", err)
				return
			}
			received.Write(buf[:n])
		}
		readCh <- received.Bytes()
	})

	time.Sleep(20 * time.Millisecond)

	wg.Go(func() {
		if _, err := b.Write(payload); err != nil {
			t.Errorf("write error: %v", err)
		}
	})

	select {
	case got := <-readCh:
		if !bytes.Equal(got, payload) {
			t.Errorf("got %q, want %q", got, payload)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for read")
	}

	b.CloseWithError(nil)
	wg.Wait()
}

func TestReadAheadBuffer_CloseWithEOF(t *testing.T) {
	t.Parallel()

	b := newReadAheadBuffer(50)
	if _, err := b.Write([]byte("remaining")); err != nil {
		t.Fatal(err)
	}
	b.CloseWithError(nil)

	out := make([]byte, 20)
	nr, err := b.Read(out)
	if err != nil || nr != 9 {
		t.Fatalf("expected 9 bytes, got nr=%d, err=%v", nr, err)
	}

	nr2, err2 := b.Read(out)
	if !errors.Is(err2, io.EOF) || nr2 != 0 {
		t.Fatalf("expected io.EOF on empty closed buffer, got nr=%d, err=%v", nr2, err2)
	}
}

func TestReadAheadBuffer_CloseWithError(t *testing.T) {
	t.Parallel()

	b := newReadAheadBuffer(50)
	expectedErr := errors.New("upstream failed")
	b.CloseWithError(expectedErr)

	buf := make([]byte, 10)
	_, err := b.Read(buf)
	if !errors.Is(err, expectedErr) {
		t.Fatalf("got %v, want %v", err, expectedErr)
	}

	_, writeErr := b.Write([]byte("data"))
	if !errors.Is(writeErr, expectedErr) {
		t.Fatalf("got writeErr %v, want %v", writeErr, expectedErr)
	}
}

func TestReadAheadBuffer_ConcurrentReadWrite(t *testing.T) {
	t.Parallel()

	b := newReadAheadBuffer(1024)
	totalBytes := 65536
	src := make([]byte, totalBytes)
	for i := range src {
		src[i] = byte(i % 256)
	}

	var wg sync.WaitGroup

	wg.Go(func() {
		chunkSize := 256
		for i := 0; i < totalBytes; i += chunkSize {
			end := min(i+chunkSize, totalBytes)
			if _, err := b.Write(src[i:end]); err != nil {
				t.Errorf("write error: %v", err)
				return
			}
		}
		b.CloseWithError(nil)
	})

	var dst bytes.Buffer
	wg.Go(func() {
		buf := make([]byte, 128)
		for {
			nr, err := b.Read(buf)
			if nr > 0 {
				dst.Write(buf[:nr])
			}
			if err != nil {
				if !errors.Is(err, io.EOF) {
					t.Errorf("read error: %v", err)
				}
				return
			}
		}
	})

	wg.Wait()
	if !bytes.Equal(src, dst.Bytes()) {
		t.Fatal("transferred data mismatch")
	}
}

func TestReadAheadBuffer_DefaultCapacity(t *testing.T) {
	t.Parallel()

	b := newReadAheadBuffer(0)
	if len(b.buf) != DefaultPrebufferBytes {
		t.Fatalf("expected default buffer capacity %d, got %d", DefaultPrebufferBytes, len(b.buf))
	}
}

func TestReadAheadBuffer_WriteClosed(t *testing.T) {
	t.Parallel()

	b := newReadAheadBuffer(50)
	b.CloseWithError(nil)
	n, err := b.Write([]byte("test"))
	if n != 0 || !errors.Is(err, errBufferClosed) {
		t.Fatalf("expected 0, errBufferClosed; got %d, %v", n, err)
	}
}
