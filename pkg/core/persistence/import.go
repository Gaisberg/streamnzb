package persistence

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"streamnzb/pkg/core/logger"
)

// importCompletedKey marks the one-shot SQLite import as done, so a restart
// does not walk the old database again.
const importCompletedKey = "sqlite_import_completed_at"

// syncPointKey records how far this database has been reconciled with another
// one: per history table, the highest timestamp already accounted for. Both
// ends of a migration get the same map, which is what lets a later switch back
// copy only what the other side added since.
//
// The value is the highest timestamp actually copied rather than the wall clock
// at migration time — a clock reading would re-copy anything written during the
// same second, duplicating history on the return trip.
const syncPointKey = "database_sync_points"

// syncPoints maps a history table to the highest value of its sync column that
// the database is known to hold.
type syncPoints map[string]int64

// importColumn is one column to carry across. fallback is the SQL literal
// substituted when the source database predates the column — older StreamNZB
// releases wrote a narrower schema, and their files must still import.
type importColumn struct {
	name     string
	fallback string
}

func cols(specs ...importColumn) []importColumn { return specs }

func txt(name string) importColumn  { return importColumn{name, "''"} }
func num(name string) importColumn  { return importColumn{name, "0"} }
func blob(name string) importColumn { return importColumn{name, "NULL"} }

// importTable describes one table's migration.
//
// conflict is the ON CONFLICT target for tables with a natural key; those merge
// row by row and are safe to re-run at any time.
//
// Tables without a key are append-only history, which has nothing to
// deduplicate on. Those carry syncColumn instead: a monotonic timestamp that
// lets a re-migration copy only the rows written since the two databases were
// last in sync.
type importTable struct {
	name       string
	conflict   string
	syncColumn string
	columns    []importColumn
}

// syncColumnIndex is where syncColumn sits in columns, or -1.
func (t importTable) syncColumnIndex() int {
	for i, c := range t.columns {
		if c.name == t.syncColumn {
			return i
		}
	}
	return -1
}

var importTables = []importTable{
	{
		name:     "kv",
		conflict: "key",
		columns:  cols(txt("key"), blob("value"), num("updated_at")),
	},
	{
		name:     "library_nzbs",
		conflict: "id",
		columns: cols(
			txt("id"), txt("content_type"), txt("content_id"),
			txt("imdb_id"), txt("tmdb_id"), txt("tvdb_id"), txt("kitsu_id"),
			num("season"), num("episode"), txt("release_title"), txt("details_url"),
			txt("indexer_name"), num("size_bytes"), blob("nzb_data"),
			num("created_at"), num("last_accessed_at"), num("last_verified_at"),
			num("pinned"), importColumn{"status", "'good'"}, txt("status_reason"),
		),
	},
	{
		name:     "library_blueprints",
		conflict: "nzb_id",
		columns: cols(
			txt("nzb_id"), txt("blueprint_json"), txt("media_file_name"), num("media_file_size"),
			txt("media_caps_json"), txt("video_codec"), num("height"), num("bit_depth"),
			txt("hdr"), num("dolby_vision"), txt("audio_codec"), num("created_at"),
		),
	},
	{
		name:     "bad_releases",
		conflict: "details_url",
		columns: cols(
			txt("details_url"), txt("release_title"), txt("reason"),
			num("reported_at"), num("expires_at"),
		),
	},
	{
		name:       "nzb_attempts",
		syncColumn: "tried_at",
		columns: cols(
			num("tried_at"), txt("stream_name"), txt("provider_name"), txt("content_type"),
			txt("content_id"), txt("content_title"), txt("indexer_name"), txt("release_title"),
			txt("release_url"), num("release_size"), txt("served_file"), txt("match_type"),
			num("success"), txt("failure_reason"), txt("avail_status"), txt("avail_reason"),
			txt("slot_path"), num("preload"), num("ttff_ms"),
		),
	},
	{
		name:       "provider_metrics",
		syncColumn: "collected_at",
		columns: cols(
			num("collected_at"), txt("provider_name"), txt("host"),
			num("active_conns"), num("idle_conns"), num("max_conns"),
			num("current_speed_mbps"), num("downloaded_mb"), num("usage_percent"),
			num("article_available_count"), num("article_missing_count"),
		),
	},
	{
		name:       "indexer_metrics",
		syncColumn: "collected_at",
		columns: cols(
			num("collected_at"), txt("indexer_name"), num("api_hits_used"), num("api_hits_limit"),
			num("downloads_used"), num("downloads_limit"), num("searches_count"),
			num("unique_hits_count"), num("avg_response_ms"),
			num("avail_available_count"), num("avail_discarded_count"),
		),
	},
	{
		name:       "performance_metrics",
		syncColumn: "collected_at",
		columns: cols(
			num("collected_at"), txt("metric_type"), num("sample_count"),
			num("min_ms"), num("max_ms"), num("avg_ms"),
			num("p50_ms"), num("p95_ms"), num("p99_ms"),
		),
	},
	{
		name:       "stream_api_samples",
		syncColumn: "timestamp",
		columns: cols(
			num("timestamp"), txt("content_type"), txt("content_id"), num("total_duration_ms"),
			num("metadata_duration_ms"), num("search_duration_ms"), num("ranking_duration_ms"),
			num("avail_nzb_duration_ms"), num("candidate_count"), num("result_count"),
		),
	},
	{
		name:       "playback_ttff_samples",
		syncColumn: "timestamp",
		columns: cols(
			num("timestamp"), txt("session_id"), txt("provider_name"), num("ttff_ms"),
			num("session_resolution_ms"), num("nzb_fetch_duration_ms"),
			num("nntp_connect_duration_ms"), num("probe_duration_ms"),
			num("first_byte_duration_ms"), num("is_cache_hit"),
		),
	},
}

// copyTables copies every known table from source into target.
//
// sourceWins decides what happens where a keyed row already exists in the
// target. A first-boot import leaves it alone (the target is the database in
// use, the file is history); a backend switch overwrites it, because there the
// source is the live database and the target is whatever was left behind the
// last time that backend was used.
// copyTables copies every known table from source into target, returning the
// watermarks reached so the caller can record them.
//
// sourceWins decides what happens where a keyed row already exists in the
// target. A first-boot import leaves it alone (the target is the database in
// use, the file is history); a backend switch overwrites it, because there the
// source is the live database and the target is whatever was left behind the
// last time that backend was used.
func copyTables(target, source *connRef, sourceWins bool, since syncPoints) (int64, syncPoints, error) {
	started := time.Now()
	var total int64
	reached := syncPoints{}
	for _, t := range importTables {
		copied, high, err := copyOneTable(target, source, t, sourceWins, since)
		if err != nil {
			// One uncopyable table must not abandon the rest; the user gets the
			// data that could be carried across, and a warning for the rest.
			logger.Warn("Failed to copy table", "table", t.name, "err", err)
			continue
		}
		if copied > 0 {
			logger.Info("Copied table", "table", t.name, "rows", copied)
		}
		if t.syncColumn != "" {
			// Carry the previous watermark forward when this pass copied nothing
			// newer, so an empty round does not reset the table to "unsynced".
			if prev, ok := since[t.name]; ok && prev > high {
				high = prev
			}
			if high > 0 {
				reached[t.name] = high
			}
		}
		total += copied
	}
	logger.Info("Database copy complete", "rows", total, "took", time.Since(started).Round(time.Millisecond))
	return total, reached, nil
}

// migrateInto copies the database currently in use into the one being switched
// to, so a backend change carries the data with it in either direction.
//
// Switching back and forth is the case worth getting right. Keyed rows are
// upserted wholesale, which is always correct and cheap enough — the library is
// quota-capped. History has no key, so it is copied from where the two
// databases were last in sync. Both ends are stamped with the same watermarks
// afterwards, which is what lets the database being returned to later know how
// much of the other's history it is missing rather than skipping it entirely.
func migrateInto(target, source *connRef) error {
	since := readSyncPoints(target)
	if len(since) > 0 {
		logger.Info("Migrating data to the new database", "history", "since last sync")
	} else {
		logger.Info("Migrating data to the new database", "history", "full copy")
	}

	_, reached, err := copyTables(target, source, true, since)
	if err != nil {
		return err
	}

	if err := writeSyncPoints(target, reached); err != nil {
		return err
	}
	return writeSyncPoints(source, reached)
}

// readSyncPoints returns how far this database has been reconciled with
// another, or nil when the two have never been synced.
func readSyncPoints(c *connRef) syncPoints {
	raw, ok, err := getKV(c, syncPointKey)
	if err != nil || !ok {
		return nil
	}
	var points syncPoints
	if err := json.Unmarshal(raw, &points); err != nil {
		return nil
	}
	return points
}

func writeSyncPoints(c *connRef, points syncPoints) error {
	raw, err := json.Marshal(points)
	if err != nil {
		return err
	}
	return setKV(c, syncPointKey, raw)
}

// importFromSQLite copies an existing streamnzb.db in dataDir into the target
// database at startup. It runs once — completion is recorded in kv — and does
// nothing when there is no file to import.
func importFromSQLite(target *connRef, dataDir string) error {
	if done, ok, err := getKV(target, importCompletedKey); err == nil && ok && len(done) > 0 {
		return nil
	}

	sourcePath := filepath.Join(dataDir, dbFilename)
	if _, err := os.Stat(sourcePath); err != nil {
		// Nothing to import. Deliberately no completion marker: someone who
		// stands up Postgres first and drops their old streamnzb.db in
		// afterwards should still get it imported on the next start.
		return nil
	}

	source, closeSource, err := openSQLiteFile(sourcePath)
	if err != nil {
		return fmt.Errorf("open %s: %w", sourcePath, err)
	}
	defer closeSource()

	logger.Info("Importing existing SQLite database", "path", sourcePath)
	_, reached, err := copyTables(target, source, false, nil)
	if err != nil {
		return err
	}
	// Stamp both ends, so a later switch back to this file syncs the history
	// written since rather than skipping it.
	if err := writeSyncPoints(target, reached); err != nil {
		return err
	}
	if err := writeSyncPoints(source, reached); err != nil {
		logger.Warn("Could not stamp the imported SQLite file as synced", "err", err)
	}
	return setKV(target, importCompletedKey, []byte(fmt.Sprintf("%q", time.Now().Format(time.RFC3339))))
}

// conflictClause renders the ON CONFLICT handling for one table. Both backends
// support the DO UPDATE form with excluded.*, so one clause serves each.
func conflictClause(t importTable, sourceWins bool) string {
	if t.conflict == "" {
		return ""
	}
	target := ` ON CONFLICT("` + t.conflict + `")`
	if !sourceWins {
		return target + " DO NOTHING"
	}
	sets := make([]string, 0, len(t.columns))
	for _, c := range t.columns {
		if c.name == t.conflict {
			continue
		}
		sets = append(sets, `"`+c.name+`" = excluded."`+c.name+`"`)
	}
	if len(sets) == 0 {
		return target + " DO NOTHING"
	}
	return target + " DO UPDATE SET " + strings.Join(sets, ", ")
}

// copyOneTable copies one table and returns the rows written plus the highest
// value of the table's sync column it saw, which becomes the new watermark.
func copyOneTable(target, source *connRef, t importTable, sourceWins bool, since syncPoints) (int64, int64, error) {
	ok, err := tableExists(source, t.name)
	if err != nil || !ok {
		return 0, 0, err
	}
	// Keyless tables are append-only history. With a watermark we can copy just
	// what is new; without one there is no way to tell what the target already
	// holds, so copying into it would double the history.
	var sinceValue int64
	applySince := false
	if t.conflict == "" {
		if value, ok := since[t.name]; ok && t.syncColumn != "" {
			sinceValue = value
			applySince = true
		} else {
			empty, err := tableIsEmpty(target, t.name)
			if err != nil {
				return 0, 0, err
			}
			if !empty {
				logger.Warn("Skipping history table: destination already has rows and no sync point to copy from",
					"table", t.name)
				return 0, 0, nil
			}
		}
	}

	available, err := tableColumns(source, t.name)
	if err != nil {
		return 0, 0, err
	}

	selectParts := make([]string, 0, len(t.columns))
	insertParts := make([]string, 0, len(t.columns))
	placeholders := make([]string, 0, len(t.columns))
	for _, c := range t.columns {
		quoted := `"` + c.name + `"`
		if _, ok := available[c.name]; ok {
			selectParts = append(selectParts, quoted)
		} else {
			selectParts = append(selectParts, c.fallback+" AS "+quoted)
		}
		insertParts = append(insertParts, quoted)
		placeholders = append(placeholders, "?")
	}

	query := `SELECT ` + strings.Join(selectParts, ", ") + ` FROM "` + t.name + `"`
	var args []any
	if applySince {
		query += ` WHERE "` + t.syncColumn + `" > ?`
		args = append(args, sinceValue)
	}
	rows, err := source.Query(query, args...)
	if err != nil {
		return 0, 0, err
	}
	defer rows.Close()

	insert := `INSERT INTO "` + t.name + `" (` + strings.Join(insertParts, ", ") + `) VALUES (` +
		strings.Join(placeholders, ", ") + `)` + conflictClause(t, sourceWins)

	tx, err := target.Begin()
	if err != nil {
		return 0, 0, err
	}
	defer func() { _ = tx.Rollback() }()

	stmt, err := tx.Prepare(insert)
	if err != nil {
		return 0, 0, err
	}
	defer stmt.Close()

	values := make([]any, len(t.columns))
	targets := make([]any, len(t.columns))
	for i := range values {
		targets[i] = &values[i]
	}
	syncIdx := t.syncColumnIndex()

	var copied, high int64
	for rows.Next() {
		if err := rows.Scan(targets...); err != nil {
			return copied, high, err
		}
		if syncIdx >= 0 {
			if ts, ok := values[syncIdx].(int64); ok && ts > high {
				high = ts
			}
		}
		res, err := stmt.Exec(values...)
		if err != nil {
			return copied, high, err
		}
		if affected, _ := res.RowsAffected(); affected > 0 {
			copied += affected
		}
	}
	if err := rows.Err(); err != nil {
		return copied, high, err
	}
	if err := tx.Commit(); err != nil {
		return copied, high, err
	}
	return copied, high, nil
}

func tableIsEmpty(c *connRef, table string) (bool, error) {
	var n int64
	if err := c.QueryRow(`SELECT COUNT(*) FROM "` + table + `"`).Scan(&n); err != nil {
		return false, err
	}
	return n == 0, nil
}
