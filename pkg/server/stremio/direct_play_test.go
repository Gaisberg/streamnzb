package stremio

import (
	"context"
	"strings"
	"testing"
	"time"

	"streamnzb/pkg/auth"
	"streamnzb/pkg/session"
)

const testDirectPlayNZB = `<?xml version="1.0" encoding="UTF-8"?>
<nzb>
  <file poster="poster" date="1" subject="Movie.2026.1080p.mkv">
    <groups><group>alt.binaries.test</group></groups>
    <segments><segment bytes="100" number="1">&lt;id-1@test&gt;</segment></segments>
  </file>
</nzb>`

func TestCreateDirectPlaySessionFromNZBData(t *testing.T) {
	mgr := session.NewManager(nil, time.Hour)
	t.Cleanup(mgr.Shutdown)

	s := &Server{
		sessionManager: mgr,
		baseURL:        "https://streamnzb.example",
	}
	stream := &auth.Stream{Username: "device1", Token: "token123"}

	sessionID, err := s.CreateDirectPlaySessionFromNZBData(context.Background(), "upload.nzb", []byte(testDirectPlayNZB), stream)
	if err != nil {
		t.Fatalf("CreateDirectPlaySessionFromNZBData returned error: %v", err)
	}
	if !strings.HasPrefix(sessionID, "direct-") {
		t.Fatalf("session id %q does not have direct prefix", sessionID)
	}

	playURL := s.DirectPlayURLForSession(sessionID, stream)
	if want := "/token123/play/" + sessionID; !strings.Contains(playURL, want) {
		t.Fatalf("play URL = %q, want to contain %q", playURL, want)
	}
	if got := s.DirectPlayPathForSession(sessionID, stream); got != "/token123/play/"+sessionID {
		t.Fatalf("play path = %q", got)
	}

	sess, err := mgr.GetSession(sessionID)
	if err != nil {
		t.Fatalf("session not found: %v", err)
	}
	if sess.ContentType != "direct" {
		t.Fatalf("content type = %q, want direct", sess.ContentType)
	}
	if sess.ContentTitle != "upload.nzb" {
		t.Fatalf("content title = %q, want upload.nzb", sess.ContentTitle)
	}
}

func TestCreateDirectPlaySessionFromURLRejectsInvalidURL(t *testing.T) {
	s := &Server{}
	if _, err := s.CreateDirectPlaySessionFromURL(context.Background(), "file:///tmp/a.nzb", nil); err == nil {
		t.Fatal("expected invalid URL to be rejected")
	}
}
