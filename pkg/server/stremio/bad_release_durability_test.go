package stremio

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/core/persistence"
	"streamnzb/pkg/indexer"
	"streamnzb/pkg/media/unpack"
	"streamnzb/pkg/playback"
	"streamnzb/pkg/release"
	"streamnzb/pkg/session"
)

func newBadReleaseTestServer(t *testing.T) (*Server, *persistence.StateManager) {
	t.Helper()
	logger.Init("ERROR")
	tempDir, err := os.MkdirTemp("", "badrelease_test")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(tempDir) })

	// GetManager is a process-wide singleton, so this must not be closed here:
	// doing so pulls the database out from under every later test in the package.
	mgr, err := persistence.GetManager(tempDir)
	if err != nil {
		t.Fatalf("get manager: %v", err)
	}

	manager := session.NewManager(nil, time.Minute)
	t.Cleanup(manager.Shutdown)

	return &Server{attemptRecorder: mgr, sessionManager: manager}, mgr
}

func badReleaseRecorded(t *testing.T, mgr *persistence.StateManager, detailsURL string) bool {
	t.Helper()
	_, recorded := mgr.BadReleaseStore().BadSet([]string{detailsURL})[detailsURL]
	return recorded
}

// The bug from the field logs: a 5s startup budget expiring wrote a 336h ban
// using the very reason string that says the failure proves nothing. One slow
// evening emptied the user's playlists for two weeks.
func TestInconclusiveFailureLeavesNoPersistentVerdict(t *testing.T) {
	server, mgr := newBadReleaseTestServer(t)

	const detailsURL = "https://indexer.example/details/inconclusive"
	sess := &session.Session{ID: "stream:default:series:tt1:1:1:0"}
	sess.SetRelease(&release.Release{Title: "Some.Release-GRP", DetailsURL: detailsURL})

	server.purgeFailedRelease(sess, "startup timeout", false)

	if badReleaseRecorded(t, mgr, detailsURL) {
		t.Fatal("an inconclusive failure recorded a persistent bad-release verdict")
	}
}

func TestConclusiveFailureStillRecordsPersistentVerdict(t *testing.T) {
	server, mgr := newBadReleaseTestServer(t)

	const detailsURL = "https://indexer.example/details/conclusive"
	sess := &session.Session{ID: "stream:default:series:tt1:1:1:0"}
	sess.SetRelease(&release.Release{Title: "Dead.Release-GRP", DetailsURL: detailsURL})

	server.purgeFailedRelease(sess, "first segment not found (430)", true)

	if !badReleaseRecorded(t, mgr, detailsURL) {
		t.Fatal("a proven-bad release was not persisted, so it will be re-offered forever")
	}
}

func TestConclusiveBadRelease(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"missing article", ErrFirstSegmentUnavailable, true},
		{"corrupt article", errors.New("rapidyenc: data corruption"), true},
		{"startup timeout", ErrPlaybackStartupTimeout, false},
		// Exactly the error shape from the field logs: the phase deadline fired
		// while the STAT sampling was mid-flight, flattening a 430-looking cause
		// into the message. The sentinel, not the text, decides.
		{"startup timeout over a stalled stat", playback.StartupTimeoutErr(5*time.Second, "probe",
			errors.New("archive volume segment unavailable: first segment not found (430)")), false},
		{"deadline exceeded", context.DeadlineExceeded, false},
		{"canceled", context.Canceled, false},
		{"fast probe heuristic", unpack.ErrArchiveFastProbe, false},
		// Both from the field logs: an indexer throttling our grab, and our own
		// matcher failing to find the episode inside an archive that holds it.
		// Neither says anything about the release, yet both used to ban it.
		{"indexer throttled the grab", fmt.Errorf("failed to lazy download NZB: %w",
			fmt.Errorf("Treasuremaps NZB download returned status 429: %w", indexer.ErrRateLimited)), false},
		{"episode matcher found no file", fmt.Errorf("%w: season=1 episode=1 scope=rar_main_media candidates=1",
			unpack.ErrEpisodeTargetNotFound), false},
		{"nil", nil, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := conclusiveBadRelease(tc.err); got != tc.want {
				t.Fatalf("conclusiveBadRelease(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}
