package stremio

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"streamnzb/pkg/core/persistence"
	"streamnzb/pkg/search/diag"
)

// buildSearchDebugStream synthesizes the debug row the Advanced
// "search debug stream" toggle prepends to stream responses: a summary of the
// most recent search funnel for this content. Its URL is the top real slot
// with a marker query, so selecting it (or an autoplaying client landing on
// it) plays the best release — the row costs nothing but a redirect. Returns
// nil when no diagnostics exist for the content yet.
//
// The row is built even when the playlist came out empty: a search that was
// filtered down to nothing is exactly the case the summary needs to explain.
func (s *Server) buildSearchDebugStream(key StreamSlotKey, service, baseURL string) *Stream {
	if service == "" {
		service = DefaultServiceName
	}
	if s == nil || s.attemptRecorder == nil {
		return nil
	}
	rows, err := s.attemptRecorder.ListSearchDiagnostics(persistence.ListSearchDiagnosticsOptions{
		StreamName:  key.StreamID,
		ContentType: key.ContentType,
		ContentID:   key.ID,
		Limit:       1,
	})
	if err != nil || len(rows) == 0 {
		return nil
	}
	var snap diag.Snapshot
	if err := json.Unmarshal([]byte(rows[0].Payload), &snap); err != nil {
		return nil
	}

	return &Stream{
		Name:        service + "\nDebug",
		URL:         baseURL + "/play/" + key.SlotPath(0) + "?src=debug",
		Description: formatSearchDebugDescription(&snap),
		BehaviorHints: &BehaviorHints{
			NotWebReady: true,
			// Its own binge group so autoplay never binge-chains into the
			// debug row from a real one.
			BingeGroup: "streamnzb-debug",
		},
	}
}

// formatSearchDebugDescription renders the snapshot into the few lines a
// stream description can carry. Full detail lives on the History page; this
// is the at-a-glance shape of the search.
func formatSearchDebugDescription(snap *diag.Snapshot) string {
	lines := make([]string, 0, 6)

	rawTotal, keptTotal := 0, 0
	for _, v := range snap.Validation {
		rawTotal += v.Raw
		keptTotal += v.Kept
	}
	funnel := []string{fmt.Sprintf("⏱ %d ms", snap.TotalMS)}
	if rawTotal > 0 {
		funnel = append(funnel, fmt.Sprintf("raw %d", rawTotal), fmt.Sprintf("valid %d", keptTotal))
	}
	if snap.DedupInput > 0 {
		funnel = append(funnel, fmt.Sprintf("dedup %d", snap.DedupOutput))
	}
	if snap.BadFiltered > 0 {
		funnel = append(funnel, fmt.Sprintf("bad −%d", snap.BadFiltered))
	}
	if snap.ProfileName != "" {
		funnel = append(funnel, fmt.Sprintf("profile %d→%d", snap.ProfileInput, snap.ProfileKept))
	}
	lines = append(lines, strings.Join(funnel, " • "))

	for _, line := range summarizeIndexerCalls(snap.IndexerCalls, 6) {
		lines = append(lines, line)
	}
	if reasons := summarizeRejections(snap.Rejected, 3); reasons != "" {
		lines = append(lines, "🚫 "+reasons)
	}
	lines = append(lines, "▶ Plays top result • details in History")
	return strings.Join(lines, "\n")
}

// summarizeIndexerCalls folds per-mode calls into one line per indexer:
// "🔍 altHUB 841ms/8 ⚡" (⚡ = at least one call answered from cache). An
// indexer whose every call failed shows its first error instead of counts.
func summarizeIndexerCalls(calls []diag.IndexerCall, limit int) []string {
	type agg struct {
		name     string
		maxMS    int64
		results  int
		cached   bool
		okCalls  int
		firstErr string
	}
	byName := map[string]*agg{}
	order := []string{}
	for _, call := range calls {
		a := byName[call.Indexer]
		if a == nil {
			a = &agg{name: call.Indexer}
			byName[call.Indexer] = a
			order = append(order, call.Indexer)
		}
		if call.DurationMS > a.maxMS {
			a.maxMS = call.DurationMS
		}
		a.results += call.Results
		a.cached = a.cached || call.Cached
		if call.Error != "" {
			if a.firstErr == "" {
				a.firstErr = call.Error
			}
		} else {
			a.okCalls++
		}
	}
	sort.SliceStable(order, func(i, j int) bool {
		return byName[order[i]].maxMS > byName[order[j]].maxMS
	})
	if len(order) > limit {
		order = order[:limit]
	}
	out := make([]string, 0, len(order))
	for _, name := range order {
		a := byName[name]
		// Only an indexer whose every call failed reads as broken; one
		// successful mode means its results made it through.
		if a.okCalls == 0 && a.firstErr != "" {
			msg := a.firstErr
			if len(msg) > 40 {
				msg = msg[:40] + "…"
			}
			out = append(out, fmt.Sprintf("🔍 %s ✗ %s", a.name, msg))
			continue
		}
		line := fmt.Sprintf("🔍 %s %dms/%d", a.name, a.maxMS, a.results)
		if a.cached {
			line += " ⚡"
		}
		out = append(out, line)
	}
	return out
}

// summarizeRejections counts jhin's rejection reasons across all turned-away
// releases and renders the top few: "trash ×7, attribute:cam ×3".
func summarizeRejections(rejected []diag.RejectedRelease, limit int) string {
	if len(rejected) == 0 {
		return ""
	}
	counts := map[string]int{}
	for _, r := range rejected {
		for _, reason := range r.Reasons {
			counts[reason]++
		}
	}
	reasons := make([]string, 0, len(counts))
	for reason := range counts {
		reasons = append(reasons, reason)
	}
	sort.Slice(reasons, func(i, j int) bool {
		if counts[reasons[i]] != counts[reasons[j]] {
			return counts[reasons[i]] > counts[reasons[j]]
		}
		return reasons[i] < reasons[j]
	})
	if len(reasons) > limit {
		reasons = reasons[:limit]
	}
	parts := make([]string, 0, len(reasons))
	for _, reason := range reasons {
		parts = append(parts, fmt.Sprintf("%s ×%d", reason, counts[reason]))
	}
	return fmt.Sprintf("%d rejected: %s", len(rejected), strings.Join(parts, ", "))
}
