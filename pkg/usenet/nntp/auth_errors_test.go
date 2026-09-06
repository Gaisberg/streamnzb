package nntp

import (
	"errors"
	"fmt"
	"net/textproto"
	"testing"
)

func TestIsAuthFailureOnlyForCredentialCodes(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"481 rejected", &textproto.Error{Code: 481, Msg: "Authentication failed"}, true},
		{"482 out of sequence", &textproto.Error{Code: 482, Msg: "Authentication commands issued out of sequence"}, true},
		{"wrapped 481", fmt.Errorf("authenticate: %w", &textproto.Error{Code: 481, Msg: "rejected"}), true},
		// A refused connection says nothing about the account, so it must never
		// park the provider.
		{"connection refused", errors.New("dial tcp: connection refused"), false},
		{"430 missing article", &textproto.Error{Code: 430, Msg: "No such article"}, false},
		// 502 is ambiguous between "denied" and "too many connections"; the
		// words after the code settle it. Eweka's lapsed-subscription line,
		// quotes included, is the case that was misread as a connection limit.
		{"502 eweka expired", &textproto.Error{Code: 502, Msg: `"Authentication Failed"`}, true},
		{"502 access denied", &textproto.Error{Code: 502, Msg: "Access denied"}, true},
		{"502 no permission", &textproto.Error{Code: 502, Msg: "No permission"}, true},
		{"502 too many connections", &textproto.Error{Code: 502, Msg: "Too many connections"}, false},
		// "account" appears in both kinds of message; the connection-limit
		// phrasing wins.
		{"502 connections for account", &textproto.Error{Code: 502, Msg: "Too many connections for your account"}, false},
		{"502 unexplained", &textproto.Error{Code: 502, Msg: "Service refused"}, false},
		// Digit-heavy text must not be mistaken for a status code.
		{"481 inside a message id", errors.New("fetch <seg481abc@host> failed"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsAuthFailure(tc.err); got != tc.want {
				t.Fatalf("IsAuthFailure(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestIsConnectionLimit(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"502 too many connections", &textproto.Error{Code: 502, Msg: "Too many connections"}, true},
		{"400 too many connections", &textproto.Error{Code: 400, Msg: "Too many connections for your account"}, true},
		{"502 limit exceeded", &textproto.Error{Code: 502, Msg: "Connection limit exceeded"}, true},
		{"text too many connections", errors.New("provider said too many connections"), true},
		// The code alone is not evidence: this 502 is an expired account.
		{"502 authentication failed", &textproto.Error{Code: 502, Msg: `"Authentication Failed"`}, false},
		{"502 unexplained", &textproto.Error{Code: 502, Msg: "Service refused"}, false},
		{"481", &textproto.Error{Code: 481, Msg: "Authentication failed"}, false},
		{"unrelated", errors.New("read timeout"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsConnectionLimit(tc.err); got != tc.want {
				t.Fatalf("IsConnectionLimit(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestIsLoginRefusedOnlyForUnexplained502(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"502 unexplained", &textproto.Error{Code: 502, Msg: "Service refused"}, true},
		{"502 bare", &textproto.Error{Code: 502, Msg: ""}, true},
		{"502 authentication failed", &textproto.Error{Code: 502, Msg: `"Authentication Failed"`}, false},
		{"502 too many connections", &textproto.Error{Code: 502, Msg: "Too many connections"}, false},
		{"481", &textproto.Error{Code: 481, Msg: "Authentication failed"}, false},
		{"untyped", errors.New("502 something"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsLoginRefused(tc.err); got != tc.want {
				t.Fatalf("IsLoginRefused(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}
