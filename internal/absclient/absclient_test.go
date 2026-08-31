package absclient

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

type roundTripFunc func(req *http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type errReaderCloser struct {
	errOnRead  bool
	errOnClose bool
}

func (e *errReaderCloser) Read(_ []byte) (int, error) {
	if e.errOnRead {
		return 0, errors.New("read failed")
	}
	return 0, io.EOF
}

func (e *errReaderCloser) Close() error {
	if e.errOnClose {
		return errors.New("close failed")
	}
	return nil
}

func newMockHTTPClient(fn roundTripFunc) *http.Client {
	return &http.Client{
		Transport: fn,
	}
}

func TestNewClient_Defaults(t *testing.T) {
	t.Parallel()

	c := New("http://abs.example.org:13378/", "my-token", "1.0.0", nil)
	if c == nil {
		t.Fatal("expected non-nil client")
	}
}

func TestDetectBaseURL(t *testing.T) {
	t.Parallel()

	cSub := New("http://sub.abs.example.org/audiobookshelf", "tok", "1.0.0", nil)
	if got := cSub.DetectBaseURL(context.Background()); got != "http://sub.abs.example.org/audiobookshelf" {
		t.Errorf("expected unchanged subpath URL, got %s", got)
	}

	cRoot := New("http://root.abs.example.net", "tok", "1.0.0", newMockHTTPClient(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path == "/ping" {
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       &errReaderCloser{errOnClose: true},
			}, nil
		}
		return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader(""))}, nil
	}))
	if got := cRoot.DetectBaseURL(context.Background()); got != "http://root.abs.example.net" {
		t.Errorf("expected root URL, got %s", got)
	}

	cAuto := New("http://auto.abs.example.com", "tok", "1.0.0", newMockHTTPClient(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path == "/audiobookshelf/ping" {
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       &errReaderCloser{errOnClose: true},
			}, nil
		}
		return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader(""))}, nil
	}))
	if got := cAuto.DetectBaseURL(context.Background()); got != "http://auto.abs.example.com/audiobookshelf" {
		t.Errorf("expected detected subpath URL, got %s", got)
	}

	cFail := New("http://fail.abs.example.org", "tok", "1.0.0", newMockHTTPClient(func(_ *http.Request) (*http.Response, error) {
		return nil, errors.New("network error")
	}))
	if got := cFail.DetectBaseURL(context.Background()); got != "http://fail.abs.example.org" {
		t.Errorf("expected fallback URL, got %s", got)
	}

	cAcc := New("http://acc.abs.example.net", "tok", "1.0.0", nil)
	if cAcc.BaseURL() != "http://acc.abs.example.net" {
		t.Errorf("expected BaseURL %s, got %s", "http://acc.abs.example.net", cAcc.BaseURL())
	}
	cAcc.SetBaseURL("http://acc.abs.example.net/custom/")
	if cAcc.BaseURL() != "http://acc.abs.example.net/custom" {
		t.Errorf("expected trimmed BaseURL, got %s", cAcc.BaseURL())
	}

	cBad := New("http://[::1]:namedport", "tok", "1.0.0", nil)
	_ = cBad.DetectBaseURL(context.Background())
}
