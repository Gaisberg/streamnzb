// Package ebml reads the element structure Matroska files are built from, and
// repairs the zero-filled holes playback serves when an article is missing
// from every provider.
//
// The loader substitutes zeros for a segment no provider will serve
// (fetchpolicy.go). Inside a Cluster those zeros sit where a demuxer expects an
// element header, and 0x00 is not a well-formed vint: lenient demuxers resync
// after a scan, strict ones (ExoPlayer- and Infuse-class) error out on the
// first invalid header and the player dies. Analyze rewrites such a hole into
// Void elements a demuxer skips by size, keeping the byte count identical so
// every offset after the hole stays where the container header says it is.
package ebml

// Matroska element IDs, kept as their raw big-endian byte value the way the
// spec writes them (marker bits included), so a parsed ID compares directly.
const (
	IDEBMLHeader        = 0x1A45DFA3
	IDSegment           = 0x18538067
	IDSeekHead          = 0x114D9B74
	IDInfo              = 0x1549A966
	IDTracks            = 0x1654AE6B
	IDTrackEntry        = 0xAE
	IDVideo             = 0xE0
	IDAudio             = 0xE1
	IDContentEncodings  = 0x6D80
	IDContentEncoding   = 0x6240
	IDCluster           = 0x1F43B675
	IDTimestamp         = 0xE7
	IDPosition          = 0xA7
	IDPrevSize          = 0xAB
	IDSimpleBlock       = 0xA3
	IDBlockGroup        = 0xA0
	IDBlock             = 0xA1
	IDCues              = 0x1C53BB6B
	IDCuePoint          = 0xBB
	IDCueTrackPositions = 0xB7
	IDChapters          = 0x1043A770
	IDEditionEntry      = 0x45B9
	IDChapterAtom       = 0xB6
	IDTags              = 0x1254C367
	IDTag               = 0x7373
	IDTargets           = 0x63C0
	IDSimpleTag         = 0x67C8
	IDAttachments       = 0x1941A469
	IDAttachedFile      = 0x61A7
	IDCRC32             = 0xBF
	IDVoid              = 0xEC
)

// masterIDs are the elements whose payload is a list of child elements. An ID
// missing from the set is treated as a leaf: the repair then voids from the
// element's own end rather than from a finer boundary inside it, which is
// wrong only in how much valid data it sacrifices, never in the structure it
// emits.
var masterIDs = map[uint32]bool{
	IDSegment:           true,
	IDSeekHead:          true,
	IDInfo:              true,
	IDTracks:            true,
	IDTrackEntry:        true,
	IDVideo:             true,
	IDAudio:             true,
	IDContentEncodings:  true,
	IDContentEncoding:   true,
	IDCluster:           true,
	IDBlockGroup:        true,
	IDCues:              true,
	IDCuePoint:          true,
	IDCueTrackPositions: true,
	IDChapters:          true,
	IDEditionEntry:      true,
	IDChapterAtom:       true,
	IDTags:              true,
	IDTag:               true,
	IDTargets:           true,
	IDSimpleTag:         true,
	IDAttachments:       true,
	IDAttachedFile:      true,
}

// IsMaster reports whether an element of this ID holds child elements.
func IsMaster(id uint32) bool { return masterIDs[id] }

// maxHeaderLen is the longest element header: a 4-byte ID and an 8-byte size.
const maxHeaderLen = 12

// ReadVint parses an EBML variable-length integer at the start of data,
// returning its encoded length and the value with the marker bit stripped. A
// length of 0 means data does not begin with a well-formed vint.
func ReadVint(data []byte) (length int, value uint64) {
	if len(data) == 0 {
		return 0, 0
	}
	first := data[0]
	var numBytes int
	for i := 0; i < 8; i++ {
		if (first & (0x80 >> i)) != 0 {
			numBytes = i + 1
			break
		}
	}
	if numBytes == 0 || numBytes > len(data) {
		return 0, 0
	}
	mask := byte(0xFF >> numBytes)
	value = uint64(first & mask)
	for i := 1; i < numBytes; i++ {
		value = (value << 8) | uint64(data[i])
	}
	return numBytes, value
}

// VintIsUnknown reports whether a size vint of this length and value is the
// all-ones "unknown size" marker — a live-muxed Cluster or Segment whose end
// the file does not declare.
func VintIsUnknown(length int, value uint64) bool {
	if length <= 0 || length > 8 {
		return false
	}
	return value == uint64(1)<<(7*length)-1
}

// ReadID parses an element ID, returning its encoded length and the raw
// big-endian bytes as the ID value. IDs keep their marker bits, so the result
// is 0xA3 for SimpleBlock and 0x1F43B675 for Cluster.
func ReadID(data []byte) (length int, id uint32) {
	length, _ = ReadVint(data)
	if length <= 0 || length > 4 {
		return 0, 0
	}
	for i := 0; i < length; i++ {
		id = (id << 8) | uint32(data[i])
	}
	return length, id
}

// ReadElementHeader returns where an element's payload starts and how long it
// is declared to be, or (-1, -1) when data does not begin with a readable
// header. An unknown size reads back as -1 with a valid payload offset.
func ReadElementHeader(data []byte) (payloadOffset int, payloadLen int64) {
	idLen, _ := ReadID(data)
	if idLen <= 0 || idLen >= len(data) {
		return -1, -1
	}
	sizeLen, size := ReadVint(data[idLen:])
	if sizeLen <= 0 || idLen+sizeLen > len(data) {
		return -1, -1
	}
	if VintIsUnknown(sizeLen, size) {
		return idLen + sizeLen, -1
	}
	return idLen + sizeLen, int64(size)
}

// ReadUint reads a big-endian unsigned integer of size bytes.
func ReadUint(data []byte, size int) uint64 {
	if size > len(data) || size > 8 {
		return 0
	}
	var v uint64
	for i := 0; i < size; i++ {
		v = (v << 8) | uint64(data[i])
	}
	return v
}

// VoidHeader encodes the header of a Void element that occupies exactly space
// bytes in total, header included. It returns nil when the space cannot hold
// one: a single byte has no room for an ID plus a size, and no vint length
// short enough to leave the remainder representable exists past 8 bytes.
//
// The payload is left untouched by design — the original bytes ride through as
// Void content, so a repaired stream differs from the raw one only in these
// few header bytes and reads the same no matter which range request produced
// it.
func VoidHeader(space int64) []byte {
	if space < 2 {
		return nil
	}
	for l := 1; l <= 8; l++ {
		payload := space - 1 - int64(l)
		if payload < 0 {
			return nil
		}
		// The all-ones value means "unknown size", so the largest payload a
		// length-l vint can state is one below it.
		if payload >= int64(1)<<(7*l)-1 {
			continue
		}
		out := make([]byte, 0, 1+l)
		out = append(out, IDVoid)
		return append(out, encodeVint(uint64(payload), l)...)
	}
	return nil
}

// encodeVint writes value as a vint of exactly length bytes, marker included.
func encodeVint(value uint64, length int) []byte {
	v := value | uint64(1)<<(7*length)
	out := make([]byte, length)
	for i := length - 1; i >= 0; i-- {
		out[i] = byte(v)
		v >>= 8
	}
	return out
}
