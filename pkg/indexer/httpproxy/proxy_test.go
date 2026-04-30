package httpproxy

import (
	"net/http"
	"net/url"
	"testing"
)

func TestIsLoopbackHost(t *testing.T) {
	for _, host := range []string{"127.0.0.1", "localhost", "::1", "[::1]"} {
		if !IsLoopbackHost(host) {
			t.Fatalf("IsLoopbackHost(%q) = false, want true", host)
		}
	}
	if IsLoopbackHost("example.com") {
		t.Fatal("IsLoopbackHost(example.com) = true, want false")
	}
}

func TestIndexerProxy_fixedURL(t *testing.T) {
	fn := IndexerProxy("http://proxy:8888")
	u, err := fn(&http.Request{URL: &url.URL{Scheme: "https", Host: "api.example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	if u == nil || u.String() != "http://proxy:8888" {
		t.Fatalf("got %v", u)
	}
}

func TestIndexerProxy_environmentFallback(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://env-proxy:3128")
	t.Setenv("HTTPS_PROXY", "http://env-proxy:3128")
	fn := IndexerProxy("")
	u, err := fn(&http.Request{URL: &url.URL{Scheme: "https", Host: "api.example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	if u == nil || u.Host != "env-proxy:3128" {
		t.Fatalf("got %v", u)
	}
}

func TestWithEasynewsDownloadNoProxy_loopback(t *testing.T) {
	inner := IndexerProxy("http://proxy:8888")
	fn := WithEasynewsDownloadNoProxy(inner)
	u, err := fn(&http.Request{URL: &url.URL{Scheme: "http", Host: "127.0.0.1:7000", Path: "/nzb"}})
	if err != nil || u != nil {
		t.Fatalf("want nil proxy for loopback, got %v err=%v", u, err)
	}
}

func TestWithEasynewsDownloadNoProxy_remoteUsesInner(t *testing.T) {
	inner := IndexerProxy("http://proxy:8888")
	fn := WithEasynewsDownloadNoProxy(inner)
	u, err := fn(&http.Request{URL: &url.URL{Scheme: "https", Host: "members.easynews.com"}})
	if err != nil {
		t.Fatal(err)
	}
	if u == nil || u.Host != "proxy:8888" {
		t.Fatalf("got %v", u)
	}
}
