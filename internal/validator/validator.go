// Package validator provides reusable network, domain, URL, and address validation routines for the proxy.
package validator

import (
	"errors"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// Common sentinel errors returned by validator routines.
var (
	ErrInvalidURL        = errors.New("must be a valid http or https URL")
	ErrInvalidPort       = errors.New("port must be between 1 and 65535")
	ErrInvalidListenPort = errors.New("must be between 1025 and 65535")
	ErrInvalidDomain     = errors.New("invalid domain or IP address")
	ErrEmptyHost         = errors.New("empty host")
)

func validateDomainLabel(part string) bool {
	pl := len(part)
	if pl < 1 || pl > 63 || part[0] == '-' || part[pl-1] == '-' {
		return false
	}
	for i := range len(part) {
		c := part[i]
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
			return false
		}
	}
	return true
}

func isNumericLabel(s string) bool {
	for i := range len(s) {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// ValidateDomain verifies that a domain name complies with RFC 1035 FQDN standards.
func ValidateDomain(domain string) error {
	domain = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(domain), "."))
	if domain == "localhost" {
		return nil
	}
	if len(domain) < 3 || len(domain) > 253 {
		return ErrInvalidDomain
	}
	parts := strings.Split(domain, ".")
	if len(parts) < 2 {
		return ErrInvalidDomain
	}
	for _, part := range parts {
		if !validateDomainLabel(part) {
			return ErrInvalidDomain
		}
	}
	lastPart := parts[len(parts)-1]
	if len(lastPart) < 2 || isNumericLabel(lastPart) {
		return ErrInvalidDomain
	}
	return nil
}

// ValidateServerName verifies that a string is a valid IPv4/IPv6 address or domain, with an optional 1-65535 port.
func ValidateServerName(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return ErrEmptyHost
	}
	host, portStr, err := net.SplitHostPort(name)
	if err != nil {
		host = name
	} else {
		port, err := strconv.Atoi(portStr)
		if err != nil || port < 1 || port > 65535 {
			return ErrInvalidPort
		}
	}

	cleanHost := strings.TrimPrefix(strings.TrimSuffix(host, "]"), "[")
	if net.ParseIP(cleanHost) != nil {
		return nil
	}
	return ValidateDomain(host)
}

// ValidateHTTPURL verifies that a raw string is a valid absolute HTTP or HTTPS URL with a valid host.
func ValidateHTTPURL(rawURL string) error {
	parsedURL, err := url.ParseRequestURI(rawURL)
	if err != nil || (parsedURL.Scheme != "http" && parsedURL.Scheme != "https") || parsedURL.Host == "" {
		return ErrInvalidURL
	}
	return ValidateServerName(parsedURL.Host)
}

// ValidateListenAddr verifies that an address is in host:port format with a non-root port between 1025 and 65535.
func ValidateListenAddr(addr string) error {
	if addr == "" {
		return errors.New("listen address cannot be empty")
	}
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return errors.New("must be in host:port format")
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 1025 || port > 65535 {
		return ErrInvalidListenPort
	}
	if host != "" && net.ParseIP(host) == nil {
		return errors.New("must be a valid IP address or empty")
	}
	return nil
}
