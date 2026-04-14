package availnzb

import (
	"testing"
	"time"

	"streamnzb/pkg/session"
)

func TestQualifiesGoodByBytes(t *testing.T) {
	sess := &session.Session{}
	sess.AddBytesRead(64 << 20)

	if !QualifiesGood(sess, 2*time.Second, 64<<20, 20*time.Second) {
		t.Fatal("expected playback to qualify by bytes")
	}
}

func TestQualifiesGoodByDuration(t *testing.T) {
	sess := &session.Session{}
	sess.AddBytesRead(8 << 20)

	if !QualifiesGood(sess, 20*time.Second, 64<<20, 20*time.Second) {
		t.Fatal("expected playback to qualify by duration")
	}
}

func TestQualifiesGoodRejectsShortSmallPlayback(t *testing.T) {
	sess := &session.Session{}
	sess.AddBytesRead(8 << 20)

	if QualifiesGood(sess, 5*time.Second, 64<<20, 20*time.Second) {
		t.Fatal("expected short small playback not to qualify")
	}
}
