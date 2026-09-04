package seek

import (
	"bytes"
	"encoding/binary"
	"math"

	"streamnzb/pkg/media/ebml"
)

var (
	ebmlSegmentID = []byte{0x18, 0x53, 0x80, 0x67}

	ebmlInfoID = []byte{0x15, 0x49, 0xA9, 0x66}

	ebmlTimestampScaleID = []byte{0x2A, 0xD7, 0xB1}

	ebmlDurationID = []byte{0x44, 0x89}
)

const defaultTimestampScale = 1000000

func durationFromMKV(data []byte) (durationSec float64, ok bool) {

	segStart := findInBytes(data, ebmlSegmentID)
	if segStart < 0 {
		return 0, false
	}
	segData := data[segStart:]

	off, payloadLen := ebml.ReadElementHeader(segData)
	if off < 0 {
		return 0, false
	}
	segPayload := segData[off:]
	// A payloadLen of -1 is the unknown-size marker a live mux writes; there is
	// nothing to truncate to then.
	if payloadLen >= 0 && int64(len(segPayload)) >= payloadLen {
		segPayload = segPayload[:payloadLen]
	}

	infoStart := findInBytes(segPayload, ebmlInfoID)
	if infoStart < 0 {
		return 0, false
	}
	infoData := segPayload[infoStart:]
	off2, infoPayloadLen := ebml.ReadElementHeader(infoData)
	if off2 < 0 {
		return 0, false
	}
	infoPayload := infoData[off2:]
	if infoPayloadLen >= 0 && int64(len(infoPayload)) > infoPayloadLen {
		infoPayload = infoPayload[:infoPayloadLen]
	}
	var timecodeScale uint64 = defaultTimestampScale
	var duration float64 = -1

	for len(infoPayload) > 0 {
		idLen, _ := ebml.ReadVint(infoPayload)
		if idLen <= 0 {
			break
		}
		idBytes := infoPayload[:idLen]
		rest := infoPayload[idLen:]
		if len(rest) < 1 {
			break
		}
		sizeLen, size := ebml.ReadVint(rest)
		if sizeLen <= 0 {
			break
		}
		payloadStart := sizeLen
		payloadEnd := payloadStart + int(size)
		if payloadEnd > len(rest) {
			payloadEnd = len(rest)
		}
		payload := rest[payloadStart:payloadEnd]
		switch {
		case bytesEqual(idBytes, ebmlTimestampScaleID):
			if len(payload) >= 1 && size <= 8 {
				timecodeScale = ebml.ReadUint(payload, int(size))
			}
		case bytesEqual(idBytes, ebmlDurationID):
			if size == 8 && len(payload) >= 8 {
				bits := binary.BigEndian.Uint64(payload)
				duration = math.Float64frombits(bits)
			}
		}
		infoPayload = rest[payloadEnd:]
	}
	if duration <= 0 || timecodeScale == 0 {
		return 0, false
	}

	return duration * float64(timecodeScale) / 1e9, true
}

func findInBytes(data, needle []byte) int {
	return bytes.Index(data, needle)
}

func bytesEqual(a, b []byte) bool {
	return bytes.Equal(a, b)
}
