package nntp

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"
)

// IsBenignDisconnect reports whether err is the expected, non-actionable result
// of us intentionally tearing down an in-flight connection or operation, rather
// than a genuine failure worth surfacing.
//
// These arise routinely and by design: when a parallel STAT/fetch across
// providers finds a hit it cancels its siblings (closing their sockets
// mid-read), and when a playback/probe stream is closed or seeks it cancels any
// in-flight segment downloads. Both produce "context canceled" or
// "use of closed network connection" errors that are noise, not signal.
//
// A real article-missing (430), timeout, or protocol error is NOT benign and
// returns false so it still gets logged.
func IsBenignDisconnect(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, net.ErrClosed) || errors.Is(err, io.ErrClosedPipe) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "use of closed network connection") ||
		strings.Contains(msg, "context canceled")
}
