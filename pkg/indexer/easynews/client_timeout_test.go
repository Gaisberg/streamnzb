package easynews

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestNewClientConfiguresSeparateSearchAndDownloadTimeouts(t *testing.T) {
	client, err := NewClient("user", "pass", "Easynews", "", 0, 0, 0, 0, "", "", nil)
	if err != nil {
		t.Fatalf("NewClient returned error: %v", err)
	}
	if client.client == nil {
		t.Fatal("expected search client to be configured")
	}
	if client.downloadClient == nil {
		t.Fatal("expected download client to be configured")
	}
	if client.client.Timeout != 15*time.Second {
		t.Fatalf("expected default search timeout %v, got %v", 15*time.Second, client.client.Timeout)
	}
	if client.downloadClient.Timeout != 30*time.Second {
		t.Fatalf("expected default download timeout %v, got %v", 30*time.Second, client.downloadClient.Timeout)
	}
}

func TestNewClientUsesConfiguredTimeoutAndDoublesDownloadTimeout(t *testing.T) {
	client, err := NewClient("user", "pass", "Easynews", "", 0, 0, 0, 12, "", "", nil)
	if err != nil {
		t.Fatalf("NewClient returned error: %v", err)
	}
	if client.client.Timeout != 12*time.Second {
		t.Fatalf("expected configured search timeout %v, got %v", 12*time.Second, client.client.Timeout)
	}
	if client.downloadClient.Timeout != 24*time.Second {
		t.Fatalf("expected doubled download timeout %v, got %v", 24*time.Second, client.downloadClient.Timeout)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

func TestSearchInternalUsesSearchClient(t *testing.T) {
	client, err := NewClient("user", "pass", "Easynews", "", 0, 0, 0, 0, "", "", nil)
	if err != nil {
		t.Fatalf("NewClient returned error: %v", err)
	}
	client.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		body := `{"data":[],"results":0}`
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(body)),
			Header:     make(http.Header),
		}, nil
	})
	client.downloadClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		t.Fatal("searchInternal used download client")
		return nil, nil
	})

	if _, _, err := client.searchInternal(context.Background(), "test", "", "", "", "", false); err != nil {
		t.Fatalf("searchInternal returned error: %v", err)
	}
}

func TestDownloadNZBInternalUsesDownloadClient(t *testing.T) {
	client, err := NewClient("user", "pass", "Easynews", "", 0, 0, 0, 0, "", "EasynewsGrab/1.0", nil)
	if err != nil {
		t.Fatalf("NewClient returned error: %v", err)
	}
	client.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		t.Fatal("downloadNZBInternal used search client")
		return nil, nil
	})
	client.downloadClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if got := req.Header.Get("User-Agent"); got != "EasynewsGrab/1.0" {
			t.Fatalf("User-Agent = %q, want %q", got, "EasynewsGrab/1.0")
		}
		body := `<?xml version="1.0" encoding="UTF-8"?><nzb xmlns="http://www.newzbin.com/DTD/2003/nzb"></nzb>`
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(body)),
			Header:     make(http.Header),
		}, nil
	})

	payload := map[string]interface{}{
		"hash":     "hash",
		"filename": "Example",
		"ext":      "mkv",
		"title":    "Example",
	}
	if _, err := client.downloadNZBInternal(context.Background(), payload); err != nil {
		t.Fatalf("downloadNZBInternal returned error: %v", err)
	}
}
