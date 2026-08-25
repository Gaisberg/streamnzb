package indexer

import "errors"

// ErrRateLimited marks a failure caused by an indexer refusing to serve us
// right now — a local quota that is already exhausted, a newznab 201 quota
// error, or an HTTP 429/503 from the indexer itself. It says nothing about the
// release that was being fetched, so callers must treat it as temporary:
// failing over is fine, recording a durable bad-release verdict is not.
//
// Callers check this with errors.Is rather than matching on message text,
// because the same condition arrives phrased half a dozen different ways
// depending on which layer noticed it.
var ErrRateLimited = errors.New("indexer rate limited")

// ErrAuthFailed marks an indexer refusing us for who we are rather than for
// what we asked: a rejected API key, a revoked login, an account whose
// subscription ended. Unlike ErrRateLimited it does not pass with time, so it
// is the one indexer failure allowed to park the indexer until a human or a
// probe says otherwise.
//
// Only definitive rejections may carry it. A timeout or a 5xx says nothing
// about the account and must stay unwrapped, or one bad afternoon on the
// indexer's side would retire a perfectly good API key.
var ErrAuthFailed = errors.New("indexer authentication failed")
