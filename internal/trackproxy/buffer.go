package trackproxy

import (
	"errors"
	"io"
	"sync"
)

// DefaultPrebufferBytes defines the default 10 MB in-memory buffer capacity.
const DefaultPrebufferBytes = 10 * 1024 * 1024

var errBufferClosed = errors.New("read-ahead buffer closed")

type readAheadBuffer struct {
	err    error
	cond   *sync.Cond
	buf    []byte
	r      int
	w      int
	size   int
	mu     sync.Mutex
	closed bool
}

func newReadAheadBuffer(capacity int) *readAheadBuffer {
	if capacity <= 0 {
		capacity = DefaultPrebufferBytes
	}
	b := &readAheadBuffer{
		buf: make([]byte, capacity),
	}
	b.cond = sync.NewCond(&b.mu)
	return b
}

func (b *readAheadBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	totalWritten := 0
	for len(p) > 0 {
		for b.size == len(b.buf) && !b.closed {
			b.cond.Wait()
		}
		if b.closed {
			if b.err != nil {
				return totalWritten, b.err
			}
			return totalWritten, errBufferClosed
		}

		space := len(b.buf) - b.size
		toWrite := min(len(p), space)

		firstChunk := min(toWrite, len(b.buf)-b.w)
		copy(b.buf[b.w:], p[:firstChunk])
		secondChunk := toWrite - firstChunk
		if secondChunk > 0 {
			copy(b.buf[:secondChunk], p[firstChunk:toWrite])
		}

		b.w = (b.w + toWrite) % len(b.buf)
		b.size += toWrite
		totalWritten += toWrite
		p = p[toWrite:]

		b.cond.Broadcast()
	}
	return totalWritten, nil
}

func (b *readAheadBuffer) Read(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	for b.size == 0 && !b.closed {
		b.cond.Wait()
	}

	if b.size == 0 && b.closed {
		if b.err != nil {
			return 0, b.err
		}
		return 0, io.EOF
	}

	toRead := min(len(p), b.size)
	firstChunk := min(toRead, len(b.buf)-b.r)
	copy(p[:firstChunk], b.buf[b.r:])
	secondChunk := toRead - firstChunk
	if secondChunk > 0 {
		copy(p[firstChunk:toRead], b.buf[:secondChunk])
	}

	b.r = (b.r + toRead) % len(b.buf)
	b.size -= toRead

	b.cond.Broadcast()
	return toRead, nil
}

func (b *readAheadBuffer) CloseWithError(err error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.closed {
		return
	}
	b.closed = true
	b.err = err
	b.cond.Broadcast()
}

func (b *readAheadBuffer) Buffered() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.size
}
