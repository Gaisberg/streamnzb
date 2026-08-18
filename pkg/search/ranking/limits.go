package ranking

import (
	"fmt"
	"time"

	jhinparser "github.com/dreulavelle/jhin/parser"

	"streamnzb/pkg/core/config"
	"streamnzb/pkg/release"
)

// applyLimits rejects results whose release falls outside the profile's NZB
// attribute limits for this content kind. Rejections are appended to jhin's
// own, so diagnostics, history and the debug stream report them the same way.
func (p *Profile) applyLimits(kind string, results []Result) {
	if p == nil {
		return
	}
	limits := config.ResolveLimits(p.limits, kind)
	if !limits.Enabled() && !p.blockPassworded {
		return
	}
	episodic := kind == KindSeries || kind == KindAnimeShow
	for i := range results {
		r := &results[i]
		if r.Candidate.Release == nil {
			continue
		}
		reasons := limitRejections(limits, p.blockPassworded, episodic, r.Candidate.Release, r.Torrent.Data)
		if len(reasons) == 0 {
			continue
		}
		r.Torrent.Fetch = false
		r.Torrent.Rejections = append(r.Torrent.Rejections, reasons...)
	}
}

// limitRejections returns why one release falls outside the resolved limits,
// or nothing when it passes. Bounds fail open: a release that does not carry
// the attribute a bound needs (no date, no grabs, unknown episode count) is
// not judged by that bound.
func limitRejections(limits config.LimitsConfig, blockPassworded, episodic bool, rel *release.Release, parsed *jhinparser.Result) []string {
	var reasons []string
	if blockPassworded && rel.Password {
		reasons = append(reasons, "password protected")
	}
	if size, ok := effectiveSize(rel.Size, episodic, parsed); ok {
		if limits.MaxSizeGB > 0 && size > gbToBytes(limits.MaxSizeGB) {
			reasons = append(reasons, fmt.Sprintf("size %s above max %s", formatGB(size), formatGB(gbToBytes(limits.MaxSizeGB))))
		}
		if limits.MinSizeGB > 0 && size < gbToBytes(limits.MinSizeGB) {
			reasons = append(reasons, fmt.Sprintf("size %s below min %s", formatGB(size), formatGB(gbToBytes(limits.MinSizeGB))))
		}
	}
	if limits.MaxAgeDays > 0 {
		if at, ok := rel.PublishedAt(); ok {
			if age := time.Since(at); age > time.Duration(limits.MaxAgeDays)*24*time.Hour {
				reasons = append(reasons, fmt.Sprintf("age %dd above max %dd", int(age.Hours()/24), limits.MaxAgeDays))
			}
		}
	}
	if limits.MinGrabs > 0 && rel.Grabs > 0 && rel.Grabs < limits.MinGrabs {
		reasons = append(reasons, fmt.Sprintf("grabs %d below min %d", rel.Grabs, limits.MinGrabs))
	}
	return reasons
}

// effectiveSize is the size the bounds judge: the full size for films and
// single episodes, the per-episode share for a multi-episode release, and
// nothing (ok=false) for a season or show pack whose episode count the title
// does not reveal — a 40 GB pack of unknown length cannot be compared against
// a per-episode cap.
func effectiveSize(size int64, episodic bool, parsed *jhinparser.Result) (int64, bool) {
	if size <= 0 {
		return 0, false
	}
	if !episodic || parsed == nil {
		return size, true
	}
	if n := len(parsed.Episodes); n > 1 {
		return size / int64(n), true
	}
	if len(parsed.Episodes) == 0 && (parsed.Complete || len(parsed.Seasons) > 0) {
		return 0, false
	}
	return size, true
}

func gbToBytes(gb float64) int64 {
	return int64(gb * 1e9)
}

// formatGB renders a byte count in the decimal gigabytes the limits are
// configured in, so a rejection reads in the same unit the user typed.
func formatGB(size int64) string {
	s := fmt.Sprintf("%.2f", float64(size)/1e9)
	// Trim trailing zeros so configured whole numbers read back verbatim
	// ("30 GB", not "30.00 GB").
	s = trimTrailingZeros(s)
	return s + " GB"
}

func trimTrailingZeros(s string) string {
	for len(s) > 0 && s[len(s)-1] == '0' {
		s = s[:len(s)-1]
	}
	if len(s) > 0 && s[len(s)-1] == '.' {
		s = s[:len(s)-1]
	}
	return s
}
