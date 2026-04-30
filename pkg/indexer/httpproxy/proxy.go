package httpproxy

import (
	"net"
	"net/http"
	"net/url"
	"strings"
)

// IsLoopbackHost reports whether host is localhost or a loopback IP (for Easynews addon downloads).
func IsLoopbackHost(host string) bool {
	host = strings.Trim(strings.ToLower(strings.TrimSpace(host)), "[]")
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// IndexerProxy returns http.Transport.Proxy: per-indexer fixed URL if set and valid (http/https), otherwise http.ProxyFromEnvironment.
func IndexerProxy(fixedProxyURL string) func(*http.Request) (*url.URL, error) {
	fixed := strings.TrimSpace(fixedProxyURL)
	if fixed == "" {
		return http.ProxyFromEnvironment
	}
	u, err := url.Parse(fixed)
	if err != nil || u.Host == "" {
		return http.ProxyFromEnvironment
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return http.ProxyFromEnvironment
	}
	canon := *u
	return func(*http.Request) (*url.URL, error) {
		return &canon, nil
	}
}

// WithEasynewsDownloadNoProxy skips HTTP(S) proxy for loopback download targets (local addon).
func WithEasynewsDownloadNoProxy(inner func(*http.Request) (*url.URL, error)) func(*http.Request) (*url.URL, error) {
	return func(req *http.Request) (*url.URL, error) {
		if req != nil && req.URL != nil && IsLoopbackHost(req.URL.Hostname()) {
			return nil, nil
		}
		if inner == nil {
			return nil, nil
		}
		return inner(req)
	}
}
