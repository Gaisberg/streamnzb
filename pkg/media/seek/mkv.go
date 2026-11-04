package seek

import (
	"encoding/binary"
	"math"

	"streamnzb/pkg/media/ebml"
)

const (
	idTimestampScale = 0x2AD7B1
	idDuration       = 0x4489

	defaultTimestampScale = 1000000
)

// durationFromMKV reads the duration the segment Info declares. Info is found
// by walking elements, not by searching for its ID: the SeekHead mkvmerge and
// ffmpeg write ahead of it names Info by that same ID.
func durationFromMKV(data []byte) (durationSec float64, ok bool) {
	infoPayload := childPayload(childPayload(data, ebml.IDSegment), ebml.IDInfo)
	if infoPayload == nil {
		return 0, false
	}
	var timecodeScale uint64 = defaultTimestampScale
	var duration float64 = -1

	for len(infoPayload) > 0 {
		id, size, payload, rest, ok := nextElement(infoPayload)
		if !ok {
			break
		}
		switch id {
		case idTimestampScale:
			if size >= 1 && size <= 8 {
				timecodeScale = ebml.ReadUint(payload, int(size))
			}
		case idDuration:
			switch {
			case size == 8 && len(payload) >= 8:
				duration = math.Float64frombits(binary.BigEndian.Uint64(payload))
			case size == 4 && len(payload) >= 4:
				duration = float64(math.Float32frombits(binary.BigEndian.Uint32(payload)))
			}
		}
		infoPayload = rest
	}
	// A missing, NaN, infinite or non-positive Duration, a zero TimestampScale
	// and an overflowing product all land here; NaN fails <= 0, so it is named.
	sec := duration * float64(timecodeScale) / 1e9
	if math.IsNaN(sec) || math.IsInf(sec, 0) || sec <= 0 {
		return 0, false
	}
	return sec, true
}

// childPayload returns the payload of the first element with the given ID
// among the sibling elements data starts with, cut to what data holds, or nil
// when data runs out, or an element of unknown size hides the rest, first.
func childPayload(data []byte, id uint32) []byte {
	for len(data) > 0 {
		elID, size, payload, rest, ok := nextElement(data)
		if !ok {
			return nil
		}
		if elID == id {
			return payload
		}
		if size < 0 {
			return nil
		}
		data = rest
	}
	return nil
}

// nextElement splits off the element data starts with: its ID, its declared
// size (-1 when unknown), its payload cut to what data holds, and what follows.
func nextElement(data []byte) (id uint32, size int64, payload, rest []byte, ok bool) {
	off, size := ebml.ReadElementHeader(data)
	if off < 0 {
		return 0, 0, nil, nil, false
	}
	end := len(data)
	// Compared before converting: a declared size can overflow a 32-bit int.
	if size >= 0 && size < int64(end-off) {
		end = off + int(size)
	}
	_, id = ebml.ReadID(data)
	return id, size, data[off:end], data[end:], true
}
