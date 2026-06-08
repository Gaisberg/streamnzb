package decode

import (
	"bufio"
	"bytes"
	"fmt"
	"hash/crc32"
	"math/rand"
	"net/textproto"
	"testing"

	"github.com/javi11/rapidyenc"
)

// manualYencArticle hand-encodes data as a single-part yEnc article with proper
// line wrapping at `lineLen` columns (the rapidyenc encoder in this version does
// not wrap, so we cannot use it to produce realistic multi-line bodies with
// line-leading characters). Output uses CRLF and includes a correct pcrc32.
func manualYencArticle(data []byte, lineLen int) []byte {
	crit := func(c byte) bool { return c == 0x00 || c == 0x0A || c == 0x0D || c == 0x3D }
	var body bytes.Buffer
	col := 0
	emit := func(c byte) {
		if col >= lineLen {
			body.WriteString("\r\n")
			col = 0
		}
		body.WriteByte(c)
		col++
	}
	for _, b := range data {
		e := byte((int(b) + 42) & 0xff)
		if crit(e) {
			// An escape pair must not be split across a line.
			if col+2 > lineLen {
				body.WriteString("\r\n")
				col = 0
			}
			body.WriteByte('=')
			col++
			body.WriteByte(byte((int(e) + 64) & 0xff))
			col++
		} else {
			emit(e)
		}
	}

	var art bytes.Buffer
	fmt.Fprintf(&art, "=ybegin part=1 total=1 line=%d size=%d name=test.bin\r\n", lineLen, len(data))
	fmt.Fprintf(&art, "=ypart begin=1 end=%d\r\n", len(data))
	art.Write(body.Bytes())
	fmt.Fprintf(&art, "\r\n=yend size=%d part=1 pcrc32=%08x\r\n", len(data), crc32.ChecksumIEEE(data))
	return art.Bytes()
}

// stuffAndTerminate applies NNTP dot-stuffing per CRLF line and appends the
// dot terminator; optionally rewrites CRLF to bare LF.
func stuffAndTerminate(article []byte, bareLF bool) []byte {
	var out bytes.Buffer
	for _, line := range bytes.SplitAfter(article, []byte("\r\n")) {
		if len(line) > 0 && line[0] == '.' {
			out.WriteByte('.')
		}
		out.Write(line)
	}
	out.WriteString(".\r\n")
	res := out.Bytes()
	if bareLF {
		res = bytes.ReplaceAll(res, []byte("\r\n"), []byte("\n"))
	}
	return res
}

// buildWire encodes data to yEnc, applies NNTP dot-stuffing, appends the dot
// terminator, and optionally rewrites CRLF to bare LF. This mirrors what a
// (compliant) Usenet server puts on the wire for an article body.
func buildWire(t *testing.T, data []byte, bareLF bool) []byte {
	return buildWireOpt(t, data, bareLF, true)
}

func buildWireOpt(t *testing.T, data []byte, bareLF, stuff bool) []byte {
	t.Helper()
	var crlf bytes.Buffer
	enc, err := rapidyenc.NewEncoder(&crlf, rapidyenc.Meta{
		FileName: "test.bin", FileSize: int64(len(data)) * 2,
		PartNumber: 1, TotalParts: 2, Offset: 0, PartSize: int64(len(data)),
	})
	if err != nil {
		t.Fatalf("NewEncoder: %v", err)
	}
	if _, err := enc.Write(data); err != nil {
		t.Fatalf("encode: %v", err)
	}
	if err := enc.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Dot-stuff each CRLF-terminated line, then add the terminator line.
	var stuffed bytes.Buffer
	for _, line := range bytes.SplitAfter(crlf.Bytes(), []byte("\r\n")) {
		if stuff && len(line) > 0 && line[0] == '.' {
			stuffed.WriteByte('.')
		}
		stuffed.Write(line)
	}
	stuffed.WriteString(".\r\n")

	out := stuffed.Bytes()
	if bareLF {
		out = bytes.ReplaceAll(out, []byte("\r\n"), []byte("\n"))
	}
	return out
}

// fragmentReader returns at most `max` bytes per Read, simulating the small,
// arbitrarily-sized chunks a network/textproto reader delivers (which exercises
// the decoder's cross-Read state machine and our crlfReader rewind logic).
type fragmentReader struct {
	r   *bytes.Reader
	max int
}

func (f *fragmentReader) Read(p []byte) (int, error) {
	if len(p) > f.max {
		p = p[:f.max]
	}
	return f.r.Read(p)
}

// decodeWire feeds the wire bytes through textproto.DotReader (the production
// NNTP body reader) and then through DecodeToBytes, exactly as the fetch path does.
func decodeWire(t *testing.T, wire []byte) (*Frame, error) {
	t.Helper()
	r := textproto.NewReader(bufio.NewReader(bytes.NewReader(wire)))
	return DecodeToBytes(r.DotReader())
}

// encodeBareLFArticle yEnc-encodes data (with CRLF, as rapidyenc emits) and then
// rewrites every CRLF to a bare LF, simulating a poster/provider that delivers
// articles with bare-LF line endings (which is what the failing release uses).
func encodeBareLFArticle(t *testing.T, data []byte, part, total int64, offset int64) []byte {
	t.Helper()
	var crlf bytes.Buffer
	enc, err := rapidyenc.NewEncoder(&crlf, rapidyenc.Meta{
		FileName:   "test.bin",
		FileSize:   int64(len(data)) * total,
		PartNumber: part,
		TotalParts: total,
		Offset:     offset,
		PartSize:   int64(len(data)),
	})
	if err != nil {
		t.Fatalf("NewEncoder: %v", err)
	}
	if _, err := enc.Write(data); err != nil {
		t.Fatalf("encode write: %v", err)
	}
	if err := enc.Close(); err != nil {
		t.Fatalf("encode close: %v", err)
	}
	// Articles are terminated by a "\r\n.\r\n" line in NNTP; rapidyenc stops at
	// "=yend". Convert to bare LF to reproduce the wire format under test.
	return bytes.ReplaceAll(crlf.Bytes(), []byte("\r\n"), []byte("\n"))
}

func TestDecodeBareLFArticleNoByteLoss(t *testing.T) {
	sizes := []int{768000, 100000, 50000}
	for _, n := range sizes {
		rng := rand.New(rand.NewSource(int64(n)))
		data := make([]byte, n)
		rng.Read(data)

		body := encodeBareLFArticle(t, data, 1, 2, 0)

		frame, err := DecodeToBytes(bytes.NewReader(body))
		if err != nil {
			t.Fatalf("size=%d decode error: %v", n, err)
		}
		if len(frame.Data) != n {
			t.Errorf("size=%d: decoded %d bytes, want %d (delta %d)", n, len(frame.Data), n, len(frame.Data)-n)
		}
		if !bytes.Equal(frame.Data, data) {
			// Find first divergence for diagnostics.
			first := -1
			for i := 0; i < len(data) && i < len(frame.Data); i++ {
				if data[i] != frame.Data[i] {
					first = i
					break
				}
			}
			t.Errorf("size=%d: decoded data differs from original (first diff at %d)", n, first)
		}
	}
}

// TestDecodeBareLFManyCriticalBytes stresses the path with data that yEnc must
// escape frequently (NUL, LF, CR, '='), increasing the chance of an escape
// landing next to a line boundary where CRLF normalization could corrupt it.
func TestDecodeBareLFManyCriticalBytes(t *testing.T) {
	n := 768000
	data := make([]byte, n)
	// yEnc escapes a byte b when (b+42)&0xff is one of 0x00,0x0A,0x0D,0x3D.
	// Pick source bytes that hit those so escapes are dense.
	crit := []byte{byte((0x00 - 42) & 0xff), byte((0x0A - 42) & 0xff), byte((0x0D - 42) & 0xff), byte((0x3D - 42) & 0xff), 0xD6, 0xE0, 0xE3, 0x13}
	for i := range data {
		data[i] = crit[i%len(crit)]
	}

	body := encodeBareLFArticle(t, data, 1, 2, 0)
	frame, err := DecodeToBytes(bytes.NewReader(body))
	if err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if len(frame.Data) != n {
		t.Errorf("decoded %d bytes, want %d (delta %d)", len(frame.Data), n, len(frame.Data)-n)
	}
	if !bytes.Equal(frame.Data, data) {
		t.Errorf("decoded data differs from original")
	}
}

// TestDecodeWireDotReaderBareLF reproduces the full production fetch+decode path:
// server wire bytes -> textproto.DotReader -> DecodeToBytes. It compares CRLF vs
// bare-LF wire to isolate whether bare-LF line endings cause byte loss via the
// dot-unstuffing layer.
func TestDecodeWireDotReaderBareLF(t *testing.T) {
	for _, bareLF := range []bool{false, true} {
		// Force many lines to start with '.' (data byte 0x04 -> encoded '.').
		n := 200000
		rng := rand.New(rand.NewSource(99))
		data := make([]byte, n)
		rng.Read(data)
		for i := 0; i < n; i += 100 {
			data[i] = 0x04
		}

		wire := buildWire(t, data, bareLF)
		frame, err := decodeWire(t, wire)
		if err != nil {
			t.Fatalf("bareLF=%v decode error: %v", bareLF, err)
		}
		if len(frame.Data) != n {
			t.Errorf("bareLF=%v: decoded %d bytes, want %d (delta %d)", bareLF, len(frame.Data), n, len(frame.Data)-n)
		} else if !bytes.Equal(frame.Data, data) {
			t.Errorf("bareLF=%v: decoded data differs from original", bareLF)
		}
	}
}

// TestDecodeWireMissingDotStuffing reproduces a non-compliant server that does
// NOT dot-stuff: yEnc lines that genuinely begin with '.' (data byte 0x04) are
// delivered with a single dot, and textproto.DotReader strips it as if it were
// stuffing, silently dropping one real byte per such line. This both loses bytes
// (matching the observed shortfall direction) and is content-dependent.
func TestDecodeWireMissingDotStuffing(t *testing.T) {
	n := 200000
	rng := rand.New(rand.NewSource(123))
	data := make([]byte, n)
	rng.Read(data)
	for i := 0; i < n; i += 137 {
		data[i] = 0x04 // encodes to '.'
	}

	wire := buildWireOpt(t, data, true /*bareLF*/, false /*stuff*/)
	frame, err := decodeWire(t, wire)
	if err != nil {
		t.Logf("decode error (informative): %v", err)
	}
	t.Logf("missing-stuffing: decoded %d bytes, want %d (delta %d)", len(frame.Data), n, len(frame.Data)-n)
	if len(frame.Data) == n {
		t.Logf("no loss observed for this fixture")
	}
}

// TestDecodeLineLeadingDotByteExact is the regression test for the double
// dot-unstuffing bug: a yEnc data byte 0x04 encodes to '.', and when it lands at
// the start of a line, rapidyenc (which expects raw, still-stuffed NNTP input)
// would unstuff it after textproto.DotReader already did, dropping a real byte.
// Using all-0x00 data (no escapes, exactly 128 cols/line) we deterministically
// place a 0x04 at column 0 of line 2.
func TestDecodeLineLeadingDotByteExact(t *testing.T) {
	const lineLen = 128
	// 0x04 encodes to '.'. Place it at the start of several wrapped lines so the
	// double dot-unstuffing bug would drop those bytes.
	data := make([]byte, 5*lineLen)
	for i := lineLen; i < len(data); i += lineLen {
		data[i] = 0x04
	}
	// Also exercise a line that starts with two data dots (0x04 0x04).
	data[lineLen] = 0x04
	data[lineLen+1] = 0x04

	for _, bareLF := range []bool{false, true} {
		wire := stuffAndTerminate(manualYencArticle(data, lineLen), bareLF)
		frame, err := decodeWire(t, wire)
		if err != nil {
			t.Fatalf("bareLF=%v decode error: %v", bareLF, err)
		}
		if len(frame.Data) != len(data) {
			t.Fatalf("bareLF=%v: decoded %d bytes, want %d", bareLF, len(frame.Data), len(data))
		}
		if !bytes.Equal(frame.Data, data) {
			t.Fatalf("bareLF=%v: decoded bytes differ from original (line-leading dot dropped?)", bareLF)
		}
	}
}

// TestDecodeWireFragmentedReads reproduces network-style fragmented delivery:
// the wire bytes are read in small chunks of varying size through the full
// DotReader -> normalizeCRLF -> rapidyenc path.
func TestDecodeWireFragmentedReads(t *testing.T) {
	n := 200000
	rng := rand.New(rand.NewSource(7))
	data := make([]byte, n)
	rng.Read(data)
	for i := 0; i < n; i += 97 {
		data[i] = 0x04 // encodes to '.' -> may land at line start
	}

	for _, bareLF := range []bool{false, true} {
		wire := buildWire(t, data, bareLF)
		for _, chunk := range []int{1, 2, 3, 7, 13, 64, 127, 128, 129, 1000} {
			r := textproto.NewReader(bufio.NewReader(&fragmentReader{r: bytes.NewReader(wire), max: chunk}))
			frame, err := DecodeToBytes(r.DotReader())
			if err != nil {
				t.Fatalf("bareLF=%v chunk=%d decode error: %v", bareLF, chunk, err)
			}
			if len(frame.Data) != n {
				t.Errorf("bareLF=%v chunk=%d: decoded %d bytes, want %d (delta %d)", bareLF, chunk, len(frame.Data), n, len(frame.Data)-n)
			} else if !bytes.Equal(frame.Data, data) {
				t.Errorf("bareLF=%v chunk=%d: decoded data differs", bareLF, chunk)
			}
		}
	}
}
