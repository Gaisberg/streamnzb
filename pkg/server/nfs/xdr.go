package nfs

import (
	"encoding/binary"
	"errors"
)

// XDR (RFC 4506) is the wire encoding under ONC RPC, MOUNT and NFS alike:
// everything is big-endian and padded to four bytes. These two types are the
// whole of what the protocols here need — integers, booleans, variable- and
// fixed-length opaques and strings — written without reflection so the layout
// of every reply is explicit at the call site.

var errXDRShort = errors.New("xdr: message truncated")

// encoder appends XDR-encoded values to a byte slice.
type encoder struct {
	buf []byte
}

func (e *encoder) bytes() []byte { return e.buf }

func (e *encoder) uint32(v uint32) {
	e.buf = binary.BigEndian.AppendUint32(e.buf, v)
}

func (e *encoder) uint64(v uint64) {
	e.buf = binary.BigEndian.AppendUint64(e.buf, v)
}

func (e *encoder) bool(v bool) {
	if v {
		e.uint32(1)
	} else {
		e.uint32(0)
	}
}

// opaque writes a variable-length opaque: its length, the bytes, then zero
// padding up to a four-byte boundary.
func (e *encoder) opaque(b []byte) {
	e.uint32(uint32(len(b)))
	e.fixedOpaque(b)
}

// fixedOpaque writes bytes whose length the reader already knows, padded.
func (e *encoder) fixedOpaque(b []byte) {
	e.buf = append(e.buf, b...)
	if pad := (4 - len(b)%4) % 4; pad > 0 {
		e.buf = append(e.buf, make([]byte, pad)...)
	}
}

func (e *encoder) string(s string) {
	e.opaque([]byte(s))
}

// decoder reads XDR-encoded values off a byte slice. The first short read
// sticks: every later read returns a zero value, and err() reports it, so a
// handler can decode a whole argument block and check once.
type decoder struct {
	b   []byte
	off int
	bad bool
}

func newDecoder(b []byte) *decoder { return &decoder{b: b} }

func (d *decoder) err() error {
	if d.bad {
		return errXDRShort
	}
	return nil
}

// remaining returns the bytes not yet consumed.
func (d *decoder) remaining() []byte {
	if d.bad || d.off > len(d.b) {
		return nil
	}
	return d.b[d.off:]
}

func (d *decoder) take(n int) []byte {
	if d.bad || n < 0 || d.off+n > len(d.b) {
		d.bad = true
		return nil
	}
	out := d.b[d.off : d.off+n]
	d.off += n
	return out
}

func (d *decoder) uint32() uint32 {
	b := d.take(4)
	if b == nil {
		return 0
	}
	return binary.BigEndian.Uint32(b)
}

func (d *decoder) uint64() uint64 {
	b := d.take(8)
	if b == nil {
		return 0
	}
	return binary.BigEndian.Uint64(b)
}

func (d *decoder) bool() bool {
	return d.uint32() != 0
}

// opaque reads a variable-length opaque and its padding. maxLen bounds what a
// peer can make the server allocate; a longer field is a protocol error.
func (d *decoder) opaque(maxLen int) []byte {
	n := int(d.uint32())
	if d.bad || n > maxLen {
		d.bad = true
		return nil
	}
	return d.fixedOpaque(n)
}

func (d *decoder) fixedOpaque(n int) []byte {
	b := d.take(n)
	if b == nil {
		return nil
	}
	if pad := (4 - n%4) % 4; pad > 0 {
		d.take(pad)
	}
	return b
}

func (d *decoder) string(maxLen int) string {
	return string(d.opaque(maxLen))
}
