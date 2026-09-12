// Package session manages in-memory playback session state and secure stream tokens.
package session

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"

	"github.com/underhax/audiobookshelf-transcoder-proxy/internal/absclient"
)

// Session represents the runtime state and concurrency-safe tracking metadata for an active audio stream.
type Session struct {
	Cancel             context.CancelFunc
	Cmd                *exec.Cmd
	TokenExpiresAt     time.Time
	ID                 string
	ItemID             string
	EpisodeID          string
	Token              string
	Title              string
	Author             string
	Narrator           string
	EpisodeTitle       string
	CoverURL           string
	MediaType          string
	AudioTracks        []absclient.AudioTrack
	lastSyncBits       atomic.Uint64
	audioStartNano     atomic.Int64
	BytesSent          atomic.Int64
	CurrentTime        float64
	Duration           float64
	Speed              float64
	SeekOffset         float64
	StartingTrackIndex int
	TokenUsed          bool
}

// GetLastSyncPosition returns the latest playback timestamp synchronized with the upstream Audiobookshelf server.
func (s *Session) GetLastSyncPosition() float64 {
	return math.Float64frombits(s.lastSyncBits.Load())
}

// SetLastSyncPosition atomically records the latest synchronized playback timestamp to prevent data races during background sync.
func (s *Session) SetLastSyncPosition(pos float64) {
	s.lastSyncBits.Store(math.Float64bits(pos))
}

// SetAudioStartTime atomically records the timestamp when the first audio payload byte was streamed to the client.
func (s *Session) SetAudioStartTime(t time.Time) {
	s.audioStartNano.Store(t.UnixNano())
}

// GetAudioStartTime atomically returns the timestamp when the first audio payload byte was streamed, or a zero time.Time if streaming has not started.
func (s *Session) GetAudioStartTime() time.Time {
	nano := s.audioStartNano.Load()
	if nano == 0 {
		return time.Time{}
	}
	return time.Unix(0, nano)
}

// AudioElapsed returns the elapsed duration since audio streaming began, or 0 if streaming has not started.
func (s *Session) AudioElapsed(now time.Time) time.Duration {
	start := s.GetAudioStartTime()
	if start.IsZero() {
		return 0
	}
	return max(0, now.Sub(start))
}

var defaultTokenTTL = 120 * time.Second

// Store manages in-memory active audio streams and validates single-use streaming tokens.
type Store struct {
	sessions map[string]*Session
	tokenTTL time.Duration
	mu       sync.RWMutex
}

// NewStore initializes a thread-safe session store with custom token expiration policies.
func NewStore(tokenTTL time.Duration) *Store {
	if tokenTTL <= 0 {
		tokenTTL = defaultTokenTTL
	}
	return &Store{
		sessions: make(map[string]*Session),
		tokenTTL: tokenTTL,
	}
}

func defaultRandRead(b []byte) (int, error) {
	n, err := io.ReadFull(rand.Reader, b)
	if err != nil {
		return n, fmt.Errorf("crypto rand read: %w", err)
	}
	return n, nil
}

var (
	randReadMu sync.RWMutex
	randRead   = defaultRandRead
)

func callRandRead(b []byte) (int, error) {
	randReadMu.RLock()
	fn := randRead
	randReadMu.RUnlock()
	return fn(b)
}

// SetRandRead overrides the entropy source for testing.
func SetRandRead(fn func([]byte) (int, error)) func() {
	randReadMu.Lock()
	orig := randRead
	randRead = fn
	randReadMu.Unlock()
	return func() {
		randReadMu.Lock()
		randRead = orig
		randReadMu.Unlock()
	}
}

// GenerateToken creates a cryptographically secure 256-bit random hexadecimal token to authenticate stream sessions.
func GenerateToken() (string, error) {
	b := make([]byte, 32)
	if _, err := callRandRead(b); err != nil {
		return "", fmt.Errorf("read random bytes: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// Create generates a secure single-use stream token, saves the session, and returns the token.
func (s *Store) Create(sess *Session) (string, error) {
	if sess == nil || sess.ID == "" {
		return "", errors.New("cannot create nil or empty session")
	}

	token, err := GenerateToken()
	if err != nil {
		return "", err
	}

	sess.Token = token
	sess.TokenExpiresAt = time.Now().Add(s.tokenTTL)
	sess.TokenUsed = false

	s.mu.Lock()
	s.sessions[sess.ID] = sess
	s.mu.Unlock()

	return token, nil
}

// Get retrieves a session by its ID.
func (s *Store) Get(id string) (*Session, bool) {
	s.mu.RLock()
	sess, ok := s.sessions[id]
	s.mu.RUnlock()
	return sess, ok
}

// ValidateToken verifies that the given stream token is valid, unexpired, and unused.
func (s *Store) ValidateToken(id, token string, reusable bool) (*Session, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	sess, ok := s.sessions[id]
	if !ok {
		return nil, errors.New("session not found")
	}

	if !reusable && sess.TokenUsed {
		return nil, errors.New("token already used")
	}

	if time.Now().After(sess.TokenExpiresAt) {
		return nil, errors.New("token expired")
	}

	if sess.Token != token {
		return nil, errors.New("invalid token")
	}

	return sess, nil
}

// MarkTokenUsed marks the session stream token as consumed so no new stream connections can be initiated.
func (s *Store) MarkTokenUsed(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if sess, ok := s.sessions[id]; ok {
		sess.TokenUsed = true
	}
}

// ValidateAndConsumeToken validates the stream token and marks it as used immediately.
func (s *Store) ValidateAndConsumeToken(id, token string, reusable bool) (*Session, error) {
	sess, err := s.ValidateToken(id, token, reusable)
	if err != nil {
		return nil, err
	}
	if !reusable {
		s.MarkTokenUsed(id)
	}
	return sess, nil
}

// Delete removes a session by ID from the store.
func (s *Store) Delete(id string) {
	s.mu.Lock()
	delete(s.sessions, id)
	s.mu.Unlock()
}

// GetAll returns a slice of all currently tracked sessions.
func (s *Store) GetAll() []*Session {
	s.mu.RLock()
	defer s.mu.RUnlock()

	res := make([]*Session, 0, len(s.sessions))
	for _, sess := range s.sessions {
		res = append(res, sess)
	}
	return res
}
