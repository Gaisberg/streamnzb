package decode

import (
	"bytes"
	"errors"
	"io"
	"regexp"
	"strconv"

	"github.com/javi11/rapidyenc"
)

var sizeMismatchRE = regexp.MustCompile(`expected size (\d+) but got (\d+)`)

type Frame struct {
	Data     []byte
	FileName string
}

const maxDecodeSizeTolerance = 256

// DecodeToBytes decodes one yEnc article body.
//
// r must yield the article in canonical NNTP wire form: CRLF line endings, dot
// stuffing still in place, no terminator. That is exactly what
// nntp.Client.Body returns, and it is what rapidyenc expects — it does its own
// dot-unstuffing and needs CRLF to find line boundaries, the "=y" control and
// the "=yend" trailer. Un-stuffing before this point drops one real byte per
// line that legitimately begins with '.' (yEnc encodes data byte 0x04 to '.').
func DecodeToBytes(r io.Reader) (*Frame, error) {
	dec := rapidyenc.NewDecoder(r)
	buf := new(bytes.Buffer)
	_, err := io.Copy(buf, dec)
	if err == nil || errors.Is(err, io.EOF) {
		// Clone to an exactly-sized slice so the over-allocated bytes.Buffer backing
		// array (up to 2× the actual data) can be GC'd immediately. Without this the
		// segment cache budget tracks len() but the heap retains cap(), causing the
		// real memory usage to far exceed the configured cache limit.
		return &Frame{Data: cloneExact(buf.Bytes()), FileName: dec.Meta.FileName}, nil
	}
	if sub := sizeMismatchRE.FindStringSubmatch(err.Error()); len(sub) == 3 {
		expected, _ := strconv.ParseInt(sub[1], 10, 64)
		got, _ := strconv.ParseInt(sub[2], 10, 64)
		shortfall := expected - got
		if shortfall > 0 && shortfall <= maxDecodeSizeTolerance && int64(buf.Len()) == got {
			// Keep the actually-decoded bytes. The =yend "size" is frequently a
			// nominal/rounded value the poster wrote (e.g. 768000) while the real
			// payload is a few bytes smaller; the decoded bytes are the true file
			// content. Padding up to the declared size would splice phantom bytes
			// at every segment boundary and corrupt the concatenated archive.
			return &Frame{Data: cloneExact(buf.Bytes()), FileName: dec.Meta.FileName}, nil
		}
	}
	return nil, err
}

// cloneExact returns a copy of b with len == cap, so the original (potentially
// over-allocated) backing array can be released to the GC.
func cloneExact(b []byte) []byte {
	if len(b) == 0 {
		return b
	}
	out := make([]byte, len(b))
	copy(out, b)
	return out
}
