package availnzb

import (
	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/release"
	"streamnzb/pkg/session"
	"strings"
	"sync"
)

// DefaultMinBytesToReportGood is the minimum bytes read during playback before reporting a release as good.
// Set to 0: the stream is already validated by unpack.ProbeMediaStream before handlePlay reaches the
// serve path, and mid-stream failures are caught by onReadError (ErrTooManyZeroFills / segment unavailable
// / data corruption) which calls ReportBad. A separate bytes threshold is therefore redundant.
const DefaultMinBytesToReportGood = 0

type ProviderHostsSource interface {
	GetProviderHosts() []string
}

type sessionProviderHostSource interface {
	ProviderHosts() []string
}

type Reporter struct {
	client               *Client
	providerSrc          ProviderHostsSource
	reported             sync.Map
	MinBytesToReportGood int64 // minimum bytes read before reporting good; 0 = no threshold
	Disabled             bool  // when true, all reporting is silently skipped
}

func NewReporter(client *Client, providerSrc ProviderHostsSource) *Reporter {
	return &Reporter{
		client:               client,
		providerSrc:          providerSrc,
		MinBytesToReportGood: DefaultMinBytesToReportGood,
	}
}

func (r *Reporter) ReportGood(sess *session.Session) {
	if r.MinBytesToReportGood > 0 && sess.BytesRead() < r.MinBytesToReportGood {
		return
	}
	if _, loaded := r.reported.LoadOrStore(sess.ID, struct{}{}); loaded {
		return
	}
	logger.Info("Reporting good/streamable release to AvailNZB", "session", sess.ID)
	r.report(sess, true)
}

func (r *Reporter) ReportBad(sess *session.Session, reason string) {
	if reason != "" {
		logger.Info("Reporting bad/unstreamable release to AvailNZB", "session", sess.ID, "reason", reason)
	}
	r.report(sess, false)
}

func (r *Reporter) ReportRAR(sess *session.Session) {
	logger.Info("Reporting RAR release to AvailNZB (compression_type)", "session", sess.ID)
	r.report(sess, true)
}

func (r *Reporter) report(sess *session.Session, available bool) {
	if r.Disabled {
		logger.Debug("AvailNZB reporting disabled by configuration, skipping report")
		return
	}
	if r.client == nil || r.client.BaseURL == "" {
		return
	}
	go func() {
		releaseURL := sess.ReleaseURL()
		if releaseURL == "" {
			return
		}
		if release.IsPrivateReleaseURL(releaseURL) {
			logger.Debug("Skipping AvailNZB report: release URL is private", "url", releaseURL)
			return
		}
		meta := ReportMeta{ReleaseName: sess.ReportReleaseName(), Size: sess.ReportSize()}
		if ids := sess.ContentIDs; ids != nil {

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
		if meta.ImdbID == "" && meta.TvdbID == "" {
			return
		}
		if meta.ReleaseName == "" {
			return
		}
		if sess.NZB != nil {
			meta.CompressionType = sess.NZB.CompressionType()
		}
		var hosts []string
		if sessWithHosts, ok := interface{}(sess).(sessionProviderHostSource); ok {
			hosts = sessWithHosts.ProviderHosts()
		}
		if len(hosts) == 0 {
			hosts = r.providerSrc.GetProviderHosts()
		}
		if len(hosts) == 0 {
			return
		}
		_ = r.client.ReportAvailability(releaseURL, strings.Join(hosts, ","), available, meta)
	}()
}
