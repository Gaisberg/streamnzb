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

// LeavesConnectionInSync reports whether a command that failed with err left
// its connection ready for the next command.
//
// A status-line refusal (430, 480, 502 …) is a complete exchange: the server
// said no and is waiting for whatever comes next, so the connection can go
// back to the pool. Anything else — a timeout mid-reply, a closed socket, a
// parse failure — leaves unread bytes or no socket at all, and pooling such a
// connection hands the next command a reply to a question it never asked.
// Callers deciding between release and discard ask this.
func LeavesConnectionInSync(err error) bool {
	if err == nil {
		return true
	}
	var tpErr *textproto.Error
	return errors.As(err, &tpErr)
}

// statusCode extracts the NNTP status code from err, if it still carries one.
func statusCode(err error) (int, bool) {
	var tpErr *textproto.Error
	if errors.As(err, &tpErr) {
		return tpErr.Code, true
	}
	return 0, false
}

func containsAny(msg string, phrases ...string) bool {
	for _, phrase := range phrases {
		if strings.Contains(msg, phrase) {
			return true
		}
	}
	return false
}

// mentionsConnectionLimit reports whether the server's own words say the
// account is out of connections, whatever code carried them.
func mentionsConnectionLimit(msg string) bool {
	return containsAny(strings.ToLower(msg),
		"too many connections", "connection limit", "connections exceeded",
		"maximum connections", "max connections", "maximum number of connections")
}

// mentionsCredentials reports whether the server's own words are about the
// account rather than the connection count.
func mentionsCredentials(msg string) bool {
	return containsAny(strings.ToLower(msg),
		"auth", "denied", "password", "login", "credential", "permission",
		"unauthorized", "expired", "suspended", "disabled", "inactive", "account")
}

// IsAuthFailure reports whether err is the server rejecting our credentials.
//
// 481 is "authentication failed/rejected" and 482 "authentication commands
// issued out of sequence"; both come back from the AUTHINFO exchange and both
// mean this account cannot log in as configured. Callers must only ask this
// about an error from Authenticate — the same codes elsewhere in a session
// would say something else entirely, and a verdict on the account is exactly
// the kind of thing that must not be inferred loosely.
//
// 502 is the code authentication failures wore before RFC 4643, and plenty of
// providers still answer it — Eweka says `502 "Authentication Failed"` for a
// lapsed subscription. The same code also means "too many connections" on
// other servers, so for 502 the verdict rests on the words after the code: it
// is a credential failure only when the text talks about the account and not
// about connections. A 502 that says neither is IsLoginRefused, not this.
func IsAuthFailure(err error) bool {
	code, ok := statusCode(err)
	if !ok {
		return false
	}
	switch code {
	case 481, 482:
		return true
	case 502:
		return !IsConnectionLimit(err) && mentionsCredentials(err.Error())
	}
	return false
}

// IsConnectionLimit reports whether err is the provider refusing another
// connection because the account is already at its limit.
//
// This is not credential death — the account works, we simply asked for more
// connections than the plan allows — so it surfaces as a degraded state that
// names the fix rather than parking the provider. The code alone cannot make
// that call (502 doubles as an authentication failure, and some servers use
// 400), so only the server's text does.
func IsConnectionLimit(err error) bool {
	if err == nil {
		return false
	}
	return mentionsConnectionLimit(err.Error())
}

// IsLoginRefused reports whether err is a 502 the server gave no usable
// explanation for: it named neither the connection count nor the account.
// Such a refusal cannot block — parking a provider on a guess is exactly what
// the fail-open rule forbids — but it is worth surfacing with the server's
// own words so the user can read what we could not.
func IsLoginRefused(err error) bool {
	code, ok := statusCode(err)
	return ok && code == 502 && !IsConnectionLimit(err) && !IsAuthFailure(err)
}
