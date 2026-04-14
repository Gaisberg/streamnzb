package stremio

import (
	"testing"

	"streamnzb/pkg/media/unpack"
	"streamnzb/pkg/session"
)

func TestRecordAttemptParamsSuccessUsesServedProviders(t *testing.T) {
	server := &Server{}
	sess := &session.Session{
		ID:           "stream:global:series:tt2261227:2:2:2",
		StreamName:   "Stream04",
		ContentType:  "series",
		ContentID:    "tt2261227:2:2",
		ContentTitle: "Altered Carbon",
	}
	sess.SetSelectedPlaybackFile("Altered.Carbon.S02E03.1080p.mkv")
	sess.RecordUsedProviderHost("news-a.example.net")
	sess.RecordUsedProviderHost("news-b.example.net")
	sess.RecordServedProviderHost("news-b.example.net")

	params := server.recordAttemptParamsForOutcome(sess, true)
	if got := params.ServedFile; got != "Altered.Carbon.S02E03.1080p.mkv" {
		t.Fatalf("ServedFile = %q, want %q", got, "Altered.Carbon.S02E03.1080p.mkv")
	}
	if got := params.StreamName; got != "Stream04" {
		t.Fatalf("StreamName = %q, want %q", got, "Stream04")
	}
	if got := params.ContentTitle; got != "Altered Carbon" {
		t.Fatalf("ContentTitle = %q, want %q", got, "Altered Carbon")
	}
	if got := params.ProviderName; got != "news-b.example.net" {
		t.Fatalf("ProviderName = %q, want %q", got, "news-b.example.net")
	}
}

func TestRecordAttemptParamsFailureUsesUsedProviders(t *testing.T) {
	server := &Server{}
	sess := &session.Session{}
	sess.RecordUsedProviderHost("news-a.example.net")
	sess.RecordUsedProviderHost("news-b.example.net")
	sess.RecordServedProviderHost("news-b.example.net")

	params := server.recordAttemptParamsForOutcome(sess, false)
	if got := params.ProviderName; got != "news-a.example.net, news-b.example.net" {
		t.Fatalf("ProviderName = %q, want %q", got, "news-a.example.net, news-b.example.net")
	}
}

func TestCacheReturnedPlaybackBlueprintReplacesStaleBlueprint(t *testing.T) {
	sess := &session.Session{}
	stale := &unpack.DirectBlueprint{FileName: "Show.S01E04.mkv", FileIndex: 1, Target: unpack.EpisodeTarget{Season: 1, Episode: 4}}
	fresh := &unpack.DirectBlueprint{FileName: "Show.S01E01.mkv", FileIndex: 0, Target: unpack.EpisodeTarget{Season: 1, Episode: 1}}
	sess.SetBlueprint(stale)

	cacheReturnedPlaybackBlueprint(sess, fresh)

	if got := sess.Blueprint; got != fresh {
		t.Fatalf("expected session blueprint to be replaced, got %#v", got)
	}
}

func TestNormalizeAttemptReasonEOF(t *testing.T) {
	got := normalizeAttemptReason("EOF")
	want := "No playable media stream could be opened from this release (EOF)."
	if got != want {
		t.Fatalf("normalizeAttemptReason(EOF) = %q, want %q", got, want)
	}
}
