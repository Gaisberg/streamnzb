package decode

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// The yEnc decoder's failures as values rather than as text.
//
// rapidyenc reports everything as a formatted string, so every decision built
// on those failures — retry this segment elsewhere, report the release bad,
// count it as evidence a scan found real damage — was made by matching that
// text, in three packages, each with its own spelling. One of them already
// carries two variants of the same message because the wording changed under
// it. The decoder is also the only part of the stack whose errors decide
// whether a release is reported bad to a community database, which is not a
// decision to hang on a substring.
//
// Classification happens here and nowhere else. Callers ask errors.Is or
// IsCorrupt.

// ErrCorrupt means the article's bytes are damaged rather than missing: the
// decoder read data that is not valid yEnc, or ran off the end without finding
// the trailer. Damage reproduces on every provider, so unlike a 430 it is
// grounds for failing the slot over and reporting the release.
var ErrCorrupt = errors.New("yenc: data corruption")

// SizeMismatchError is a decode that completed but produced a different byte
// count than the article's "=yend size=" declared.
//
// It is not automatically corruption. Posters routinely write a nominal,
// rounded size (768000) over a payload a few bytes shorter, so a small
// shortfall is the normal case and the decoded bytes are the true content.
// Callers decide using Shortfall.
type SizeMismatchError struct {
	// Expected is what "=yend size=" declared, Got what the decoder produced.
	Expected int64
	Got      int64
	// Err is the underlying decoder error, kept so the original wording
	// survives into a log line.
	Err error
}

func (e *SizeMismatchError) Error() string {
	return fmt.Sprintf("yenc: expected size %d but got %d", e.Expected, e.Got)
}

func (e *SizeMismatchError) Unwrap() error { return e.Err }

// Shortfall is how many bytes short of the declared size the decode came, and
// is negative for the rarer overshoot.
func (e *SizeMismatchError) Shortfall() int64 { return e.Expected - e.Got }

// sizeMismatchRE reads the two numbers out of rapidyenc's size error. This
// regexp, and the substrings in corruptPhrases, are the whole of what this
// package knows about how the decoder words itself — the one place to fix when
// a dependency bump changes the text.
var sizeMismatchRE = regexp.MustCompile(`expected size (\d+) but got (\d+)`)

// corruptPhrases are the decoder's ways of saying the bytes were bad. The
// trailer message appears with both quotings because rapidyenc has used both.
var corruptPhrases = []string{
	"data corruption",
	"rapidyenc",
	`without finding "=yend" trailer`,
	"without finding '=yend' trailer",
	"=yend",
	// A failed integrity check is damage by definition, whoever phrases it.
	// Listed in its own right rather than relying on the "rapidyenc" prefix
	// that currently happens to accompany it: the prefix is branding, the
	// checksum is the finding.
	"crc32 mismatch",
	"crc mismatch",
	"crc32 hash",
}

// classify turns a decoder error into one of this package's, leaving anything
// it does not recognise alone. An io error reading the article is the caller's
// problem, not the decoder's, and must not be reported as damage.
func classify(err error) error {
	if err == nil {
		return nil
	}
	if sub := sizeMismatchRE.FindStringSubmatch(err.Error()); len(sub) == 3 {
		expected, _ := strconv.ParseInt(sub[1], 10, 64)
		got, _ := strconv.ParseInt(sub[2], 10, 64)
		return &SizeMismatchError{Expected: expected, Got: got, Err: err}
	}
	if matchesCorruptPhrase(err.Error()) {
		return fmt.Errorf("%w: %s", ErrCorrupt, err.Error())
	}
	return err
}

func matchesCorruptPhrase(msg string) bool {
	lowered := strings.ToLower(msg)
	for _, phrase := range corruptPhrases {
		if strings.Contains(lowered, phrase) {
			return true
		}
	}
	return false
}

// IsCorrupt reports whether err is, or wraps, a yEnc failure meaning the data
// itself is damaged.
//
// It falls back to matching the decoder's text for errors that reached the
// caller without passing through this package's own classification — a decode
// that happens inside an archive reader surfaces through that reader, which
// may have flattened the error into a string on the way. The typed check comes
// first so the fallback disappears on its own as those paths start wrapping.
func IsCorrupt(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrCorrupt) {
		return true
	}
	for e := err; e != nil; e = errors.Unwrap(e) {
		if matchesCorruptPhrase(e.Error()) {
			return true
		}
	}
	return false
}
