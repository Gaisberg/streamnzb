package jellyfin

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestRedactedQueryMasksCredentials(t *testing.T) {
	q := url.Values{
		"api_key":          {"secret-1"},
		"ApiKey":           {"secret-2"},
		"Tag":              {"secret-3"},
		"X-Emby-Token":     {"secret-4"},
		"ParentId":         {"abc"},
		"IncludeItemTypes": {"Movie,Series"},
	}
	got := redactedQuery(q)
	if strings.Contains(got, "secret") {
		t.Fatalf("credential leaked into the log: %s", got)
	}
	for _, want := range []string{"ParentId=abc", "IncludeItemTypes=Movie%2CSeries", "api_key=REDACTED", "Tag=REDACTED"} {
		if !strings.Contains(got, want) {
			t.Fatalf("query %q missing %q", got, want)
		}
	}
	if redactedQuery(nil) != "" {
		t.Fatalf("empty query rendered as %q", redactedQuery(nil))
	}
}

// The recorder sits in front of video streams, so it must not swallow a
// flush or hide the writer underneath from a ResponseController.
func TestStatusRecorderPassesThrough(t *testing.T) {
	inner := httptest.NewRecorder()
	rec := &statusRecorder{ResponseWriter: inner, status: http.StatusOK}
	var w http.ResponseWriter = rec
	w.WriteHeader(http.StatusPartialContent)
	_, _ = w.Write([]byte("hello"))
	_, _ = rec.ReadFrom(strings.NewReader(" world"))
	f, ok := w.(http.Flusher)
	if !ok {
		t.Fatal("recorder is not a Flusher")
	}
	f.Flush()
	if !inner.Flushed {
		t.Fatal("flush did not reach the underlying writer")
	}
	if rec.status != http.StatusPartialContent || rec.bytes != int64(len("hello world")) || inner.Body.String() != "hello world" {
		t.Fatalf("status %d bytes %d body %q", rec.status, rec.bytes, inner.Body.String())
	}
	if rec.Unwrap() != inner {
		t.Fatal("Unwrap does not return the underlying writer")
	}
}
