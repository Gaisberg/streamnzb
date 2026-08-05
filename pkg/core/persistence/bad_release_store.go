package persistence

import (
	"database/sql"
	"strings"
	"sync"
	"time"
)

// BadReleaseStore persists definitive bad-release verdicts (article holes,
// corrupt data) across restarts, keyed by details URL. Without it, a purged
// release comes straight back from the indexer after a restart and gets played
// again — the same broken release can be served to the user multiple times a day.
//
// Entries carry a TTL so a release is eventually retried (articles can be
// reposted, and a rare false positive must not blacklist a release forever).
type BadReleaseStore struct {
	db  *sql.DB // shared read pool
	wdb *sql.DB // single-connection write handle
	mu  sync.RWMutex
}

func NewBadReleaseStore(db, wdb *sql.DB) *BadReleaseStore {
	return &BadReleaseStore{db: db, wdb: wdb}
}

// MarkBad records (or refreshes) a bad verdict for detailsURL with the given TTL.
func (bs *BadReleaseStore) MarkBad(detailsURL, releaseTitle, reason string, ttl time.Duration) {
	detailsURL = strings.TrimSpace(detailsURL)
	if bs == nil || bs.db == nil || detailsURL == "" || ttl <= 0 {
		return
	}
	bs.mu.Lock()
	defer bs.mu.Unlock()
	now := time.Now()
	_, _ = bs.wdb.Exec(`
		INSERT INTO bad_releases (details_url, release_title, reason, reported_at, expires_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(details_url) DO UPDATE SET
			release_title = excluded.release_title,
			reason = excluded.reason,
			reported_at = excluded.reported_at,
			expires_at = excluded.expires_at
	`, detailsURL, releaseTitle, reason, now.Unix(), now.Add(ttl).Unix())
}

// BadSet returns which of the given details URLs currently have an unexpired bad
// verdict. Used to filter a candidate list in one query at playlist-build time.
func (bs *BadReleaseStore) BadSet(detailsURLs []string) map[string]struct{} {
	if bs == nil || bs.db == nil || len(detailsURLs) == 0 {
		return nil
	}
	bs.mu.RLock()
	defer bs.mu.RUnlock()

	placeholders := make([]string, 0, len(detailsURLs))
	args := make([]any, 0, len(detailsURLs)+1)
	for _, u := range detailsURLs {
		u = strings.TrimSpace(u)
		if u == "" {
			continue
		}
		placeholders = append(placeholders, "?")
		args = append(args, u)
	}
	if len(placeholders) == 0 {
		return nil
	}
	args = append(args, time.Now().Unix())

	rows, err := bs.db.Query(
		`SELECT details_url FROM bad_releases WHERE details_url IN (`+strings.Join(placeholders, ",")+`) AND expires_at > ?`,
		args...,
	)
	if err != nil {
		return nil
	}
	defer rows.Close()

	bad := make(map[string]struct{})
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err == nil {
			bad[u] = struct{}{}
		}
	}
	return bad
}

// Forget removes a verdict (e.g. when a release later verifies or plays good).
func (bs *BadReleaseStore) Forget(detailsURL string) {
	detailsURL = strings.TrimSpace(detailsURL)
	if bs == nil || bs.db == nil || detailsURL == "" {
		return
	}
	bs.mu.Lock()
	defer bs.mu.Unlock()
	_, _ = bs.wdb.Exec(`DELETE FROM bad_releases WHERE details_url = ?`, detailsURL)
}

// PurgeExpired drops verdicts past their TTL; safe to call opportunistically.
func (bs *BadReleaseStore) PurgeExpired() {
	if bs == nil || bs.db == nil {
		return
	}
	bs.mu.Lock()
	defer bs.mu.Unlock()
	_, _ = bs.wdb.Exec(`DELETE FROM bad_releases WHERE expires_at <= ?`, time.Now().Unix())
}
