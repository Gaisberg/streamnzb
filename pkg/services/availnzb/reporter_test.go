package availnzb

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"streamnzb/pkg/release"
	"streamnzb/pkg/session"
)

func TestQualifiesGoodByBytes(t *testing.T) {
	sess := &session.Session{}
	sess.AddBytesRead(64 << 20)

	if !QualifiesGood(sess, 2*time.Second, 64<<20, 20*time.Second) {
		t.Fatal("expected playback to qualify by bytes")
	}
}

func TestQualifiesGoodByDuration(t *testing.T) {
	sess := &session.Session{}
	sess.AddBytesRead(8 << 20)

	if !QualifiesGood(sess, 20*time.Second, 64<<20, 20*time.Second) {
		t.Fatal("expected playback to qualify by duration")
	}
}

func TestQualifiesGoodRejectsShortSmallPlayback(t *testing.T) {
	sess := &session.Session{}
	sess.AddBytesRead(8 << 20)

	if QualifiesGood(sess, 5*time.Second, 64<<20, 20*time.Second) {
		t.Fatal("expected short small playback not to qualify")
	}
}

func TestReportGoodReturnsSentOnlyAfterSuccessfulDelivery(t *testing.T) {
	reportCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reportCalls++
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()

	client := NewClient(server.URL, "test-key")
	reporter := NewReporter(client, nil)
	reporter.MinBytesToReportGood = 1
	reporter.MinDurationToReportGood = 0

	sess := &session.Session{
		ID:      "sess-good-sent",
		Release: &release.Release{Title: "Example.Release.2026", DetailsURL: "https://example.invalid/details/123", Size: 1234},
		ContentIDs: &session.AvailReportMeta{
			ImdbID: "tt1234567",
		},
	}
	sess.AddBytesRead(2)
	sess.RecordServedProviderHost("news.example.net")

	outcome := reporter.ReportGood(sess, time.Second)
	if outcome.Status != "sent" {
		t.Fatalf("expected sent outcome, got %+v", outcome)
	}
	if reportCalls != 1 {
		t.Fatalf("expected one report call, got %d", reportCalls)
	}
}

func TestReportGoodReturnsSkippedWhenDeliveryFails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	client := NewClient(server.URL, "test-key")
	reporter := NewReporter(client, nil)
	reporter.MinBytesToReportGood = 1
	reporter.MinDurationToReportGood = 0

	sess := &session.Session{
		ID:      "sess-good-failed",
		Release: &release.Release{Title: "Example.Release.2026", DetailsURL: "https://example.invalid/details/123", Size: 1234},
		ContentIDs: &session.AvailReportMeta{
			ImdbID: "tt1234567",
		},
	}
	sess.AddBytesRead(2)
	sess.RecordServedProviderHost("news.example.net")

	outcome := reporter.ReportGood(sess, time.Second)
	if outcome.Status != "skipped" {
		t.Fatalf("expected skipped outcome, got %+v", outcome)
	}
	if outcome.Reason != "AvailNZB report could not be delivered." {
		t.Fatalf("unexpected skip reason: %q", outcome.Reason)
	}
}

func TestReportGoodAllowsRetryAfterSkippedAttempt(t *testing.T) {
	reportCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reportCalls++
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()

	client := NewClient(server.URL, "test-key")
	reporter := NewReporter(client, nil)
	reporter.MinBytesToReportGood = 1
	reporter.MinDurationToReportGood = 0

	sess := &session.Session{
		ID:      "sess-good-retry",
		Release: &release.Release{Title: "Example.Release.2026", DetailsURL: "https://example.invalid/details/123", Size: 1234},
		ContentIDs: &session.AvailReportMeta{
			ImdbID: "tt1234567",
		},
	}
	sess.AddBytesRead(2)

	first := reporter.ReportGood(sess, time.Second)
	if first.Status != "skipped" {
		t.Fatalf("expected first outcome skipped, got %+v", first)
	}

	sess.RecordServedProviderHost("news.example.net")

	second := reporter.ReportGood(sess, time.Second)
	if second.Status != "sent" {
		t.Fatalf("expected second outcome sent, got %+v", second)
	}
	if reportCalls != 1 {
		t.Fatalf("expected one successful report call after retry, got %d", reportCalls)
	}
}

func TestReportBadFallsBackToAttemptedProviderHosts(t *testing.T) {
	var (
		mu          sync.Mutex
		reportCalls int
		gotProvider string
		decodeErr   error
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		reportCalls++
		mu.Unlock()
		var body ReportRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			mu.Lock()
			decodeErr = err
			mu.Unlock()
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		mu.Lock()
		gotProvider = body.ProviderURL
		mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()

	client := NewClient(server.URL, "test-key")
	reporter := NewReporter(client, nil)

	sess := &session.Session{
		ID:      "sess-bad-fallback",
		Release: &release.Release{Title: "Example.Release.2026", DetailsURL: "https://example.invalid/details/123", Size: 1234},
		ContentIDs: &session.AvailReportMeta{
			ImdbID: "tt1234567",
		},
	}
	sess.RecordAttemptedProviderHost("news-a.example.net")
	sess.RecordAttemptedProviderHost("news-b.example.net")

	outcome := reporter.ReportBad(sess, "EOF")
	if outcome.Status != "sent" {
		t.Fatalf("expected sent outcome, got %+v", outcome)
	}
	mu.Lock()
	defer mu.Unlock()
	if decodeErr != nil {
		t.Fatalf("decode report body: %v", decodeErr)
	}
	if reportCalls != 1 {
		t.Fatalf("expected one report call, got %d", reportCalls)
	}
	if gotProvider != "news-a.example.net,news-b.example.net" {
		t.Fatalf("provider = %q, want %q", gotProvider, "news-a.example.net,news-b.example.net")
	}
}
