package reporting

import (
	"testing"

	"streamnzb/pkg/session"
)

func TestReportPlaybackSuccessReportsGood(t *testing.T) {
	reporter := &reportingTestReporter{}
	svc := NewServiceWithOptions(Options{
		Enabled:  true,
		Reporter: reporter,
	})

	sess := &session.Session{ID: "sess-1"}
	svc.ReportPlaybackSuccess(sess)

	if reporter.good != sess {
		t.Fatalf("expected good report for session %p, got %p", sess, reporter.good)
	}
	if reporter.bad != nil {
		t.Fatalf("expected no bad report, got %#v", reporter.bad)
	}
}

func TestReportPlaybackFailureReportsBad(t *testing.T) {
	reporter := &reportingTestReporter{}
	svc := NewServiceWithOptions(Options{
		Enabled:  true,
		Reporter: reporter,
	})

	sess := &session.Session{ID: "sess-2"}
	svc.ReportPlaybackFailure(sess, "failed")

	if reporter.bad != sess {
		t.Fatalf("expected bad report for session %p, got %p", sess, reporter.bad)
	}
	if reporter.badReason != "failed" {
		t.Fatalf("expected bad reason failed, got %q", reporter.badReason)
	}
}

func TestReportPlaybackIsNoopWhenDisabled(t *testing.T) {
	reporter := &reportingTestReporter{}
	svc := NewServiceWithOptions(Options{
		Enabled:  false,
		Reporter: reporter,
	})

	svc.ReportPlaybackSuccess(&session.Session{ID: "sess-3"})
	svc.ReportPlaybackFailure(&session.Session{ID: "sess-3"}, "failed")

	if reporter.good != nil || reporter.bad != nil {
		t.Fatalf("expected no reporting while disabled, got good=%p bad=%p", reporter.good, reporter.bad)
	}
}

type reportingTestReporter struct {
	good      *session.Session
	bad       *session.Session
	badReason string
}

func (r *reportingTestReporter) ReportGood(sess *session.Session) {
	r.good = sess
}

func (r *reportingTestReporter) ReportBad(sess *session.Session, reason string) {
	r.bad = sess
	r.badReason = reason
}
