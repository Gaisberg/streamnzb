package reporting

import (
	"streamnzb/pkg/session"
	"strings"
)

type playbackReporter interface {
	ReportGood(sess *session.Session)
	ReportBad(sess *session.Session, reason string)
}

type Options struct {
	Enabled  bool
	Reporter playbackReporter
}

type Service struct {
	enabled  bool
	reporter playbackReporter
}

func NewServiceWithOptions(opts Options) *Service {
	return &Service{
		enabled:  opts.Enabled,
		reporter: opts.Reporter,
	}
}

func (s *Service) ReportPlaybackSuccess(sess *session.Session) {
	if !s.enabled || s.reporter == nil || sess == nil {
		return
	}
	s.reporter.ReportGood(sess)
}

func (s *Service) ReportPlaybackFailure(sess *session.Session, reason string) {
	if !s.enabled || s.reporter == nil || sess == nil {
		return
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "failed"
	}
	s.reporter.ReportBad(sess, reason)
}
