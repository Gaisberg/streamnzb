// Package par2 reads the file descriptions out of a PAR2 index: the original
// name and length of every file in a release. Obfuscated posts carry their
// media under hash names and the real names only here, which is how SABnzbd
// restores them before an *arr ever sees the files; it is how this server
// restores them too.
//
// Only the index is read — the small .par2 without a volume suffix — and only
// its File Description packets. Recovery data is never touched.
package par2

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
)

// FileDesc is one file the PAR2 set describes.
type FileDesc struct {
	Name   string
	Length uint64
	// MD5 is the hash of the whole file; MD5First16K of its first 16 KiB.
	MD5         [16]byte
	MD5First16K [16]byte
}

var (
	magic        = []byte("PAR2\x00PKT")
	typeFileDesc = []byte("PAR 2.0\x00FileDesc")

	ErrNotPar2 = errors.New("par2: not a PAR2 file")
)

const (
	headerLen = 8 + 8 + 16 + 16 + 16 // magic, length, packet md5, set id, type
	// maxPacket bounds one packet: a FileDesc is a few hundred bytes, and
	// nothing in an index is large. Guards against a corrupt length.
	maxPacket = 1 << 24
)

// ParseIndex returns the file descriptions in data, in packet order, with
// duplicates (repeated packets are normal in PAR2) folded.
func ParseIndex(data []byte) ([]FileDesc, error) {
	if len(data) < headerLen || !bytes.HasPrefix(data, magic) {
		// The index may start with junk; find the first packet.
		i := bytes.Index(data, magic)
		if i < 0 {
			return nil, ErrNotPar2
		}
		data = data[i:]
	}
	var out []FileDesc
	seen := map[[16]byte]bool{}
	for len(data) >= headerLen {
		if !bytes.HasPrefix(data, magic) {
			// Lost sync: look for the next magic.
			i := bytes.Index(data[1:], magic)
			if i < 0 {
				break
			}
			data = data[1+i:]
			continue
		}
		length := binary.LittleEndian.Uint64(data[8:16])
		if length < uint64(headerLen) || length > maxPacket {
			return out, fmt.Errorf("par2: packet length %d out of range", length)
		}
		if uint64(len(data)) < length {
			// A truncated final packet: keep what was parsed.
			break
		}
		packet := data[:length]
		data = data[length:]
		if !bytes.Equal(packet[48:64], typeFileDesc) {
			continue
		}
		body := packet[headerLen:]
		if len(body) < 16+16+16+8 {
			continue
		}
		var fd FileDesc
		var fileID [16]byte
		copy(fileID[:], body[0:16])
		if seen[fileID] {
			continue
		}
		seen[fileID] = true
		copy(fd.MD5[:], body[16:32])
		copy(fd.MD5First16K[:], body[32:48])
		fd.Length = binary.LittleEndian.Uint64(body[48:56])
		fd.Name = strings.TrimRight(string(body[56:]), "\x00")
		if fd.Name == "" {
			continue
		}
		out = append(out, fd)
	}
	if len(out) == 0 {
		return nil, errors.New("par2: no file descriptions")
	}
	return out, nil
}

// NamesByLength indexes descriptions by length, the key a release's files
// can be matched on without hashing. Several files of one length map to all
// their names; the caller decides what to do with the ambiguity.
func NamesByLength(descs []FileDesc) map[uint64][]string {
	out := make(map[uint64][]string, len(descs))
	for _, d := range descs {
		out[d.Length] = append(out[d.Length], d.Name)
	}
	return out
}
