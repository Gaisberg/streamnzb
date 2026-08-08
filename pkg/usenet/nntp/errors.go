package nntp

import (
	"context"
	"errors"
	"io"
	"net"
	"net/textproto"
	"strings"
)

// IsArticleNotFound reports whether err indicates 430 No Such Article.
//
// NNTP responses surface as *textproto.Error carrying the status code, which
// survives fmt.Errorf %w wrapping, so the typed check is authoritative. The
// textual fallbacks exist only for errors that lost their type (e.g. after
// stringification); a bare substring match on "430" is deliberately avoided —
// message-IDs and byte counts interpolated into error text are digit-heavy and
// previously caused misclassification that fed provider cooloffs and the
// permanent-missing cache with wrong data.
func IsArticleNotFound(err error) bool {
	if err == nil {
		return false
	}
	var tpErr *textproto.Error
	if errors.As(err, &tpErr) {
		return tpErr.Code == 430
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "no such article") {
		return true
	}
	// Standalone "430" token (start-of-string or preceded by space/colon, and
	// followed by space or end) — matches "... : 430 dmca removed" without
	// matching "<seg430abc@host>".
	for idx := strings.Index(msg, "430"); idx >= 0; {
		before := idx == 0 || msg[idx-1] == ' '
		afterIdx := idx + len("430")
		after := afterIdx == len(msg) || msg[afterIdx] == ' '
		if before && after {
			return true
		}
		next := strings.Index(msg[idx+1:], "430")
		if next < 0 {
			break
		}
		idx = idx + 1 + next
	}
	return false
}

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
