package scrobble

import (
	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/core/persistence"
)

// perStreamMigrationKey marks that ResetLegacyLinks has run, so a linked
// account is not wiped again on the next start.
const perStreamMigrationKey = "scrobble_accounts_per_stream"

// ResetLegacyLinks drops every stored watch-tracking link, once.
//
// Accounts used to be linked once for the whole server; they are linked per
// stream now, and there is no correct way to carry the old link across.
// Handing it to a stream picked by some rule would file one person's viewing
// into whichever stream sorted first — the exact mix-up per-stream accounts
// exist to prevent — and leaving the old key in place lets a stale token
// resurface under a shape nothing writes any more. So every link is dropped
// and each person links their own: one re-link, unambiguous, against a silent
// misattribution nobody would think to check for.
//
// keys are the state-store keys to clear; each package names its own so the
// key strings stay private to the code that writes them.
func ResetLegacyLinks(dataDir string, keys ...string) bool {
	manager, err := persistence.GetManager(dataDir)
	if err != nil {
		return false
	}
	var done bool
	if found, _ := manager.Get(perStreamMigrationKey, &done); found && done {
		return false
	}
	for _, key := range keys {
		// Written rather than deleted: the state store's contract is
		// get/set, and an empty value reads back as "nothing linked".
		_ = manager.Set(key, map[string]any{})
	}
	if err := manager.Set(perStreamMigrationKey, true); err != nil {
		// Without the marker this would run again next start, so a failure
		// here is worth a warning rather than silence.
		logger.Warn("Failed to record the per-stream account migration", "err", err)
	}
	logger.Info("Watch-tracking accounts are now linked per stream; existing links were cleared. " +
		"Link Simkl or MDBList again from each stream that needs one (Streams → edit).")
	return true
}
