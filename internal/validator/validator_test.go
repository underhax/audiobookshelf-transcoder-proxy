package validator

import (
	"strings"
	"testing"
)

func TestValidateDomain(t *testing.T) {
	t.Parallel()

	tests := []struct {
		domain  string
		wantErr bool
	}{
		{domain: "localhost", wantErr: false},
		{domain: "example.org", wantErr: false},
		{domain: "sub.example.org", wantErr: false},
		{domain: "deep.sub.example.net.", wantErr: false},
		{domain: "", wantErr: true},
		{domain: "a.b", wantErr: true},
		{domain: "-leading.example.com", wantErr: true},
		{domain: "trailing-.example.com", wantErr: true},
		{domain: "invalid@char.example.com", wantErr: true},
		{domain: "nodot", wantErr: true},
		{domain: "allnumeric.123", wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.domain, func(t *testing.T) {
			t.Parallel()
			err := ValidateDomain(tc.domain)
			if (err != nil) != tc.wantErr {
				t.Errorf("ValidateDomain(%q) err = %v, wantErr = %v", tc.domain, err, tc.wantErr)
			}
		})
	}
}

func TestValidateServerName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		wantErr bool
	}{
		{name: "127.0.0.1", wantErr: false},
		{name: "192.168.1.50:8099", wantErr: false},
		{name: "[::1]:9099", wantErr: false},
		{name: "example.org:443", wantErr: false},
		{name: "proxy.example.net", wantErr: false},
		{name: "", wantErr: true},
		{name: "example.org:99999", wantErr: true},
		{name: "example.org:0", wantErr: true},
		{name: "example.org:port", wantErr: true},
		{name: "invalid/path", wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateServerName(tc.name)
			if (err != nil) != tc.wantErr {
				t.Errorf("ValidateServerName(%q) err = %v, wantErr = %v", tc.name, err, tc.wantErr)
			}
		})
	}
}

func TestValidateHTTPURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		rawURL  string
		wantErr bool
	}{
		{rawURL: "https://abs.example.org", wantErr: false},
		{rawURL: "http://192.168.1.50:8099/subpath", wantErr: false},
		{rawURL: "http://localhost:8099", wantErr: false},
		{rawURL: "", wantErr: true},
		{rawURL: "ftp://abs.example.org", wantErr: true},
		{rawURL: "http://", wantErr: true},
		{rawURL: "http://example.org:99999", wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.rawURL, func(t *testing.T) {
			t.Parallel()
			err := ValidateHTTPURL(tc.rawURL)
			if (err != nil) != tc.wantErr {
				t.Errorf("ValidateHTTPURL(%q) err = %v, wantErr = %v", tc.rawURL, err, tc.wantErr)
			}
		})
	}
}

func TestValidateListenAddr(t *testing.T) {
	t.Parallel()

	tests := []struct {
		addr    string
		wantErr bool
	}{
		{addr: "127.0.0.1:8099", wantErr: false},
		{addr: "0.0.0.0:8099", wantErr: false},
		{addr: ":8099", wantErr: false},
		{addr: "", wantErr: true},
		{addr: "8099", wantErr: true},
		{addr: "127.0.0.1:80", wantErr: true},
		{addr: "127.0.0.1:1024", wantErr: true},
		{addr: "127.0.0.1:99999", wantErr: true},
		{addr: "127.0.0.1:port", wantErr: true},
		{addr: "notanip:8099", wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.addr, func(t *testing.T) {
			t.Parallel()
			err := ValidateListenAddr(tc.addr)
			if (err != nil) != tc.wantErr {
				t.Errorf("ValidateListenAddr(%q) err = %v, wantErr = %v", tc.addr, err, tc.wantErr)
			}
		})
	}
}

func TestValidateRequestPath(t *testing.T) {
	t.Parallel()

	longPath := "/" + strings.Repeat("a", 257)

	tests := []struct {
		name       string
		path       string
		requestURI string
		wantErr    bool
	}{
		{name: "valid root", path: "/", requestURI: "/", wantErr: false},
		{name: "valid api path", path: "/api/proxy/books", requestURI: "/api/proxy/books", wantErr: false},
		{name: "valid stream with token", path: "/stream/sess_123.aac", requestURI: "/stream/sess_123.aac?token=abc", wantErr: false},
		{name: "path too long", path: longPath, requestURI: longPath, wantErr: true},
		{name: "double slash in uri", path: "/api/books", requestURI: "//api/books", wantErr: true},
		{name: "path traversal in uri", path: "/api/books", requestURI: "/api/../books", wantErr: true},
		{name: "invalid character space", path: "/api/ proxy", requestURI: "/api/ proxy", wantErr: true},
		{name: "invalid character percent", path: "/api/%20", requestURI: "/api/%20", wantErr: true},
		{name: "invalid character null", path: "/api/\x00", requestURI: "/api/\x00", wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateRequestPath(tc.path, tc.requestURI)
			if (err != nil) != tc.wantErr {
				t.Errorf("ValidateRequestPath(%q, %q) err = %v, wantErr = %v", tc.path, tc.requestURI, err, tc.wantErr)
			}
		})
	}
}
