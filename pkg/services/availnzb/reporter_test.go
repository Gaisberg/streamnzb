package availnzb

import (
	"net/http"
	"net/http/httptest"
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
