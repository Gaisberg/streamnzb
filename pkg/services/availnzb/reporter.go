package availnzb

import (
	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/release"
	"streamnzb/pkg/session"
	"strings"
	"sync"
	"time"
)

// DefaultMinBytesToReportGood is the minimum bytes read during playback before reporting a release as good.
const DefaultMinBytesToReportGood int64 = 64 << 20

// DefaultMinDurationToReportGood is the minimum real serve time before reporting a release as good.
const DefaultMinDurationToReportGood = 20 * time.Second

type ProviderHostsSource interface {
	GetProviderHosts() []string
}

type ReportOutcome struct {
	Status string
	Reason string
}

func SentOutcome(available bool) ReportOutcome {
	if available {
		return ReportOutcome{Status: "sent", Reason: "Reported as good to AvailNZB."}
	}
	return ReportOutcome{Status: "sent", Reason: "Reported as bad to AvailNZB."}
}

func SkippedOutcome(reason string) ReportOutcome {
	return ReportOutcome{Status: "skipped", Reason: reason}
}

type Reporter struct {
	client                  *Client
	providerSrc             ProviderHostsSource
	reported                sync.Map
	MinBytesToReportGood    int64
	MinDurationToReportGood time.Duration
	Disabled                bool // when true, all reporting is silently skipped
}

func NewReporter(client *Client, providerSrc ProviderHostsSource) *Reporter {
	return &Reporter{
		client:                  client,
		providerSrc:             providerSrc,
		MinBytesToReportGood:    DefaultMinBytesToReportGood,
		MinDurationToReportGood: DefaultMinDurationToReportGood,
	}
}

func QualifiesGood(sess *session.Session, serveDuration time.Duration, minBytes int64, minDuration time.Duration) bool {
	if sess == nil {
		return false
	}
	bytesOk := minBytes <= 0 || sess.BytesRead() >= minBytes
	durationOk := minDuration <= 0 || serveDuration >= minDuration
	return bytesOk || durationOk
}

func (r *Reporter) ReportGood(sess *session.Session, serveDuration time.Duration) ReportOutcome {
	if !QualifiesGood(sess, serveDuration, r.MinBytesToReportGood, r.MinDurationToReportGood) {
		return SkippedOutcome("Playback ended before the good threshold was reached.")
	}
	if _, loaded := r.reported.Load(sess.ID); loaded {
		return SkippedOutcome("This session was already reported to AvailNZB.")
	}
	logger.Info("Reporting good/streamable release to AvailNZB", "session", sess.ID)
	outcome := r.report(sess, true, true)
	if outcome.Status == "sent" {
		r.reported.Store(sess.ID, struct{}{})
	}
	return outcome
}

func (r *Reporter) ReportBad(sess *session.Session, reason string) ReportOutcome {
	if reason != "" {
		logger.Info("Reporting bad/unstreamable release to AvailNZB", "session", sess.ID, "reason", reason)
	}
	return r.report(sess, false, false)
}

func (r *Reporter) ReportRAR(sess *session.Session) ReportOutcome {
	logger.Info("Reporting RAR release to AvailNZB (compression_type)", "session", sess.ID)
	return r.report(sess, true, false)
}

func (r *Reporter) report(sess *session.Session, available bool, servedOnly bool) ReportOutcome {
	if r.Disabled {
		logger.Debug("AvailNZB reporting disabled by configuration, skipping report")
		return SkippedOutcome("AvailNZB reporting is disabled.")
	}
	if r.client == nil || r.client.BaseURL == "" {
		return SkippedOutcome("AvailNZB is not configured.")
	}
	releaseURL := sess.ReleaseURL()
	if releaseURL == "" || release.IsPrivateReleaseURL(releaseURL) {
		logger.Debug("Skipping AvailNZB report: no public details URL", "url", releaseURL)
		return SkippedOutcome("No public details URL is available for this release.")
	}
	meta := ReportMeta{ReleaseName: sess.ReportReleaseName(), Size: sess.ReportSize()}
	if ids := sess.ContentIDs; ids != nil {
		meta.TmdbID = ids.TmdbID

		if ids.TvdbID != "" && (ids.Season > 0 || ids.Episode > 0) {
			meta.TvdbID = ids.TvdbID
			meta.Season = ids.Season
			meta.Episode = ids.Episode
		} else if ids.ImdbID != "" {
			meta.ImdbID = ids.ImdbID
		} else if ids.TvdbID != "" {
			meta.TvdbID = ids.TvdbID
			meta.Season = ids.Season
			meta.Episode = ids.Episode
		}
	}
	if meta.ImdbID == "" && meta.TmdbID == "" && meta.TvdbID == "" {
		return SkippedOutcome("Required metadata for AvailNZB is missing.")
	}
	if meta.ReleaseName == "" {
		return SkippedOutcome("Release name is missing for AvailNZB.")
	}
	if sess.NZB != nil {
		meta.CompressionType = sess.NZB.CompressionType()
	}
	hosts := sess.UsedProviderHosts()
	if servedOnly {
		hosts = sess.ServedProviderHosts()
	}
	if len(hosts) == 0 {
		if servedOnly {
			logger.Debug("Skipping AvailNZB report without served provider hosts", "session", sess.ID)
			return SkippedOutcome("No serving provider could be confirmed for this attempt.")
		}
		return SkippedOutcome("No provider could be confirmed for this attempt.")
	}
	if err := r.client.ReportAvailability(releaseURL, strings.Join(hosts, ","), available, meta); err != nil {
		logger.Warn("AvailNZB report delivery failed", "session", sess.ID, "err", err)
		return SkippedOutcome("AvailNZB report could not be delivered.")
	}
	return SentOutcome(available)
}
