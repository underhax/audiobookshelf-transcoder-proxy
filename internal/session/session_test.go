package session

import (
	"crypto/rand"
	"errors"
	"testing"
	"time"
)

func TestStore_CreateAndGet(t *testing.T) {
	t.Parallel()

	s := NewStore(30 * time.Second)
	sess := &Session{
		ID:     "play_session_1",
		ItemID: "item_123",
		Speed:  1.5,
	}

	token, err := s.Create(sess)
	if err != nil {
		t.Fatalf("unexpected error creating session: %v", err)
	}
	if token == "" {
		t.Fatal("expected non-empty token")
	}

	retrieved, ok := s.Get("play_session_1")
	if !ok || retrieved == nil {
		t.Fatal("expected to retrieve session")
	}
	if retrieved.ItemID != "item_123" {
		t.Errorf("got %s, want %s", retrieved.ItemID, "item_123")
	}

	_, notFound := s.Get("non_existent")
	if notFound {
		t.Error("expected non_existent session to not be found")
	}
}

func TestStore_Create_Errors(t *testing.T) {
	s := NewStore(30 * time.Second)
	if _, err := s.Create(nil); err == nil {
		t.Error("expected error creating nil session")
	}

	if _, err := s.Create(&Session{}); err == nil {
		t.Error("expected error creating session with empty ID")
	}

	cleanup := SetRandRead(func(_ []byte) (int, error) {
		return 0, errors.New("entropy error")
	})
	defer cleanup()

	if _, err := s.Create(&Session{ID: "sess_err"}); err == nil {
		t.Error("expected error when randRead fails")
	}

	buf := make([]byte, 16)
	if n, err := defaultRandRead(buf); err != nil || n != len(buf) {
		t.Errorf("defaultRandRead failed: %v, n=%d", err, n)
	}

	origReader := rand.Reader
	rand.Reader = &errReader{}
	defer func() { rand.Reader = origReader }()

	if _, err := defaultRandRead(buf); err == nil {
		t.Error("expected error from defaultRandRead with broken rand.Reader")
	}
}

type errReader struct{}

func (e *errReader) Read(_ []byte) (int, error) {
	return 0, errors.New("entropy error")
}

func TestStore_ValidateAndConsumeToken_Errors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		setup      func(*Store) string
		name       string
		id         string
		token      string
		wantErrMsg string
	}{
		{
			name: "not found",
			setup: func(_ *Store) string {
				return ""
			},
			id:         "invalid_id",
			token:      "any_tok",
			wantErrMsg: "session not found",
		},
		{
			name: "token already used",
			setup: func(s *Store) string {
				sess := &Session{ID: "sess_used", TokenUsed: true}
				tok, err := s.Create(sess)
				if err != nil {
					t.Fatalf("setup create: %v", err)
				}
				sess.TokenUsed = true
				return tok
			},
			id:         "sess_used",
			token:      "any",
			wantErrMsg: "token already used",
		},
		{
			name: "token expired",
			setup: func(s *Store) string {
				sess := &Session{ID: "sess_expired"}
				tok, err := s.Create(sess)
				if err != nil {
					t.Fatalf("setup create: %v", err)
				}
				sess.TokenExpiresAt = time.Now().Add(-1 * time.Minute)
				return tok
			},
			id:         "sess_expired",
			token:      "any",
			wantErrMsg: "token expired",
		},
		{
			name: "invalid token value",
			setup: func(s *Store) string {
				sess := &Session{ID: "sess_mismatch"}
				if _, err := s.Create(sess); err != nil {
					t.Fatalf("setup create: %v", err)
				}
				return "wrong_token"
			},
			id:         "sess_mismatch",
			token:      "wrong_token_val",
			wantErrMsg: "invalid token",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := NewStore(30 * time.Second)
			tt.setup(s)

			_, err := s.ValidateAndConsumeToken(tt.id, tt.token, false)
			if err == nil || err.Error() != tt.wantErrMsg {
				t.Fatalf("expected error %q, got %v", tt.wantErrMsg, err)
			}
		})
	}
}

func TestStore_ValidateAndConsumeToken_SuccessAndReusable(t *testing.T) {
	t.Parallel()

	s := NewStore(30 * time.Second)
	sess := &Session{ID: "sess_ok"}
	tok, err := s.Create(sess)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	res1, err := s.ValidateAndConsumeToken("sess_ok", tok, true)
	if err != nil || res1 == nil {
		t.Fatalf("expected success in reusable mode: %v", err)
	}

	res2, err := s.ValidateAndConsumeToken("sess_ok", tok, true)
	if err != nil || res2 == nil {
		t.Fatalf("expected success in reusable mode second time: %v", err)
	}

	sess2 := &Session{ID: "sess_v2"}
	tok2, err := s.Create(sess2)
	if err != nil {
		t.Fatalf("create sess_v2: %v", err)
	}
	vSess, valErr := s.ValidateToken("sess_v2", tok2, false)
	if valErr != nil || vSess == nil || vSess.TokenUsed {
		t.Fatalf("expected valid unused token, got err: %v, sess: %v", valErr, vSess)
	}
	s.MarkTokenUsed("sess_v2")
	s.MarkTokenUsed("nonexistent")
	if _, checkErr := s.ValidateToken("sess_v2", tok2, false); checkErr == nil {
		t.Error("expected error for token after MarkTokenUsed")
	}

	res3, err := s.ValidateAndConsumeToken("sess_ok", tok, false)
	if err != nil || res3 == nil || !res3.TokenUsed {
		t.Fatalf("expected success in non-reusable mode: %v", err)
	}

	if _, err := s.ValidateAndConsumeToken("sess_ok", tok, false); err == nil {
		t.Error("expected error for already used token")
	}
}

func TestStore_DeleteAndGetAll(t *testing.T) {
	t.Parallel()

	s := NewStore(30 * time.Second)
	if _, err := s.Create(&Session{ID: "s1"}); err != nil {
		t.Fatalf("create s1: %v", err)
	}
	if _, err := s.Create(&Session{ID: "s2"}); err != nil {
		t.Fatalf("create s2: %v", err)
	}

	all := s.GetAll()
	if len(all) != 2 {
		t.Fatalf("expected 2 sessions, got %d", len(all))
	}

	s.Delete("s1")
	if len(s.GetAll()) != 1 {
		t.Errorf("expected 1 session after delete, got %d", len(s.GetAll()))
	}
}

func TestStore_CustomTokenTTL(t *testing.T) {
	t.Parallel()

	customTTL := 2 * time.Minute
	s := NewStore(customTTL)
	sess := &Session{ID: "sess_custom_ttl"}

	before := time.Now()
	if _, err := s.Create(sess); err != nil {
		t.Fatalf("create session: %v", err)
	}

	expectedExpiry := before.Add(customTTL)
	diff := sess.TokenExpiresAt.Sub(expectedExpiry)
	if diff < -time.Second || diff > time.Second {
		t.Errorf("expected token expiry around %v, got %v", expectedExpiry, sess.TokenExpiresAt)
	}
}

func TestNewStore_DefaultTTL(t *testing.T) {
	t.Parallel()

	s := NewStore(0)
	if s == nil {
		t.Fatal("expected non-nil store")
	}
}

func TestSession_LastSyncPosition(t *testing.T) {
	t.Parallel()

	sess := &Session{ID: "sess_pos"}
	if sess.GetLastSyncPosition() != 0.0 {
		t.Errorf("expected 0.0 initially, got %v", sess.GetLastSyncPosition())
	}
	sess.SetLastSyncPosition(123.45)
	if sess.GetLastSyncPosition() != 123.45 {
		t.Errorf("expected 123.45, got %v", sess.GetLastSyncPosition())
	}
}

func TestGenerateToken(t *testing.T) {
	tok1, err := GenerateToken()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(tok1) != 64 {
		t.Errorf("expected 64 hex chars, got %d", len(tok1))
	}

	tok2, err := GenerateToken()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tok1 == tok2 {
		t.Error("expected unique tokens")
	}

	orig := randRead
	randRead = func(_ []byte) (int, error) {
		return 0, errors.New("entropy error")
	}
	defer func() { randRead = orig }()

	if _, err := GenerateToken(); err == nil {
		t.Error("expected error when randRead fails")
	}
}

func TestSession_AudioStartTimeAndElapsed(t *testing.T) {
	t.Parallel()

	sess := &Session{ID: "sess_audio_time"}
	if !sess.GetAudioStartTime().IsZero() {
		t.Errorf("expected zero initial audio start time, got %v", sess.GetAudioStartTime())
	}
	if got := sess.AudioElapsed(time.Now()); got != 0 {
		t.Errorf("expected 0 elapsed duration before start time set, got %v", got)
	}

	startTime := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	sess.SetAudioStartTime(startTime)

	if got := sess.GetAudioStartTime(); !got.Equal(startTime) {
		t.Errorf("got %v, want %v", got, startTime)
	}

	tests := []struct {
		now  time.Time
		name string
		want time.Duration
	}{
		{
			now:  startTime.Add(5 * time.Second),
			name: "elapsed after five seconds",
			want: 5 * time.Second,
		},
		{
			now:  startTime.Add(-5 * time.Second),
			name: "now is before start time clamps to zero",
			want: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := sess.AudioElapsed(tt.now)
			if got != tt.want {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}
