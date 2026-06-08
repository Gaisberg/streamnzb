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

// yencInputReader prepares a textproto.DotReader article body for rapidyenc.
//
// It does two things:
//  1. Converts bare-LF line endings to CRLF, which rapidyenc requires to detect
//     line boundaries, the "=y" control, and the "=yend" trailer.
//  2. Rewrites a '.' that begins a line into the yEnc escape "=n". A literal '.'
//     and the escape "=n" both decode to byte 0x04, but rapidyenc performs its
//     own NNTP dot-unstuffing on a line-leading '.' (it expects the raw,
//     still-stuffed article). Our input has ALREADY been dot-unstuffed by
//     textproto.DotReader, so a line-leading '.' here is genuine data; without
//     this rewrite rapidyenc would strip it and silently drop one byte per such
//     line, corrupting the decoded stream.
type yencInputReader struct {
	r    io.Reader
	in   [8192]byte
	out  []byte
	last byte
	err  error
}

func (y *yencInputReader) Read(p []byte) (int, error) {
	for len(y.out) == 0 {
		if y.err != nil {
			return 0, y.err
		}
		n, err := y.r.Read(y.in[:])
		if n > 0 {
			y.out = y.transform(y.in[:n])
		}
		if err != nil {
			y.err = err
			if len(y.out) == 0 {
				return 0, err
			}
		}
	}
	n := copy(p, y.out)
	y.out = y.out[n:]
	return n, nil
}

func (y *yencInputReader) transform(src []byte) []byte {
	dst := make([]byte, 0, len(src)+len(src)/16+8)
	for _, b := range src {
		switch {
		case b == '\n' && y.last != '\r':
			dst = append(dst, '\r', '\n')
			y.last = '\n'
		case b == '.' && y.last == '\n':
			// Line-leading data dot -> escaped form so rapidyenc won't unstuff it.
			dst = append(dst, '=', 'n')
			y.last = 'n'
		default:
			dst = append(dst, b)
			y.last = b
		}
	}
	return dst
}

func normalizeCRLF(r io.Reader) io.Reader { return &yencInputReader{r: r} }

type Frame struct {
	Data     []byte
	FileName string
}

const maxDecodeSizeTolerance = 256

func DecodeToBytes(r io.Reader) (*Frame, error) {
	dec := rapidyenc.NewDecoder(normalizeCRLF(r))
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
