package unpack

import (
	"bytes"
	"fmt"
	"sort"
	"streamnzb/pkg/core/logger"
)

type RarVolumeIdentity struct {
	Version               int
	InnerName             string
	InnerSize             int64
	VolumeNumber          *int
	IsFirstVolume         bool
	ContinuedFromPrevious bool
	ContinuesInNext       bool
}

type IdentifiedFile struct {
	Index    int
	File     UnpackableFile
	Identity *RarVolumeIdentity
}

// IdentifyRarVolumeHeader inspects the leading bytes of a volume to derive its identity.
// Returns nil if head is not a valid RAR header or headers are encrypted.
func IdentifyRarVolumeHeader(head []byte) *RarVolumeIdentity {
	if len(head) < 7 {
		return nil
	}

	// RAR5 signature: 52 61 72 21 1A 07 01 00
	rar5Sig := []byte{0x52, 0x61, 0x72, 0x21, 0x1A, 0x07, 0x01, 0x00}
	// RAR4 signature: 52 61 72 21 1A 07 00
	rar4Sig := []byte{0x52, 0x61, 0x72, 0x21, 0x1A, 0x07, 0x00}

	var version int
	var dataStart int

	if idx := bytes.Index(head, rar5Sig); idx >= 0 {
		version = 5
		dataStart = idx + len(rar5Sig)
	} else if idx := bytes.Index(head, rar4Sig); idx >= 0 {
		version = 4
		dataStart = idx + len(rar4Sig)
	} else {
		return nil
	}

	if dataStart >= len(head) {
		return nil
	}

	buf := head[dataStart:]
	return parseIdentityBlocks(buf, version)
}

func parseIdentityBlocks(buf []byte, version int) *RarVolumeIdentity {
	pos := 0
	maxBlocks := 16

	var volNum *int
	isFirstVol := false

	for i := 0; i < maxBlocks && pos < len(buf); i++ {
		if version == 5 {
			if pos+4 > len(buf) {
				break
			}
			// Skip CRC32 (4 bytes)
			p := pos + 4
			size, n := readUvarint(buf[p:])
			if n <= 0 || size <= 0 || p+n+int(size) > len(buf) {
				break
			}
			headerStart := p + n
			p = headerStart

			htype, n1 := readUvarint(buf[p:])
			p += n1
			flags, n2 := readUvarint(buf[p:])
			p += n2

			hasExtra := (flags & 0x01) != 0
			hasData := (flags & 0x02) != 0
			dataNotFirst := (flags & 0x08) != 0
			dataNotLast := (flags & 0x10) != 0

			var extraSize uint64
			if hasExtra {
				v, n := readUvarint(buf[p:])
				extraSize = v
				p += n
			}
			var dataSize uint64
			if hasData {
				v, n := readUvarint(buf[p:])
				dataSize = v
				p += n
			}

			nextPos := headerStart + int(size) + int(dataSize)

			if htype == 4 { // B5_CRYPT
				return nil // Header encrypted
			}
			if htype == 1 { // B5_MAIN
				aflags, an := readUvarint(buf[p:])
				if (aflags & 0x0002) != 0 { // ARC5_HAS_VOLUME_NUMBER
					vol, _ := readUvarint(buf[p+an:])
					v := int(vol)
					volNum = &v
				} else {
					isFirstVol = true
				}
			} else if htype == 2 { // B5_FILE
				// Parse inner file header
				bodyEnd := headerStart + int(size) - int(extraSize)
				fileFlags, fn := readUvarint(buf[p:])
				p += fn
				isDir := (fileFlags & 0x0001) != 0
				unpSize, unN := readUvarint(buf[p:])
				p += unN
				// Skip attr/mtime/crc32/compFlags/os to name
				// Find string by scanning readable ASCII/UTF-8 path
				if !isDir {
					name := extractRar5FileName(buf[p:bodyEnd])
					if name != "" {
						return &RarVolumeIdentity{
							Version:               5,
							InnerName:             name,
							InnerSize:             int64(unpSize),
							VolumeNumber:          volNum,
							IsFirstVolume:         isFirstVol || (volNum != nil && *volNum == 0),
							ContinuedFromPrevious: dataNotFirst,
							ContinuesInNext:       dataNotLast,
						}
					}
				}
			}

			if nextPos <= pos {
				break
			}
			pos = nextPos
		} else { // RAR4
			if pos+7 > len(buf) {
				break
			}
			htype := buf[pos+2]
			flags := uint16(buf[pos+3]) | uint16(buf[pos+4])<<8
			size := int(uint16(buf[pos+5]) | uint16(buf[pos+6])<<8)
			if size < 7 || pos+size > len(buf) {
				break
			}

			hasData := (flags & 0x8000) != 0
			var dataSize uint32
			if hasData && pos+11 <= len(buf) {
				dataSize = uint32(buf[pos+7]) | uint32(buf[pos+8])<<8 | uint32(buf[pos+9])<<16 | uint32(buf[pos+10])<<24
			}
			nextPos := pos + size + int(dataSize)

			if htype == 0x73 { // Main header
				if (flags & 0x0080) != 0 {
					return nil // Main header encrypted
				}
				if (flags & 0x0100) != 0 {
					isFirstVol = true
				}
			} else if htype == 0x74 { // File header
				splitBefore := (flags & 0x0001) != 0
				splitAfter := (flags & 0x0002) != 0
				isDir := (flags & 0x00e0) == 0x00e0

				if !isDir && pos+32 <= len(buf) {
					unpSize := uint64(buf[pos+11]) | uint64(buf[pos+12])<<8 | uint64(buf[pos+13])<<16 | uint64(buf[pos+14])<<24
					nameLen := int(uint16(buf[pos+26]) | uint16(buf[pos+27])<<8)
					nameStart := pos + 32
					if flags&0x0100 != 0 { // F15_LARGE_DATA
						nameStart += 8
					}
					if nameStart+nameLen <= pos+size {
						name := string(buf[nameStart : nameStart+nameLen])
						name = cleanPath(name)
						return &RarVolumeIdentity{
							Version:               4,
							InnerName:             name,
							InnerSize:             int64(unpSize),
							VolumeNumber:          volNum,
							IsFirstVolume:         isFirstVol,
							ContinuedFromPrevious: splitBefore,
							ContinuesInNext:       splitAfter,
						}
					}
				}
			}

			if nextPos <= pos {
				break
			}
			pos = nextPos
		}
	}

	return nil
}

func readUvarint(buf []byte) (uint64, int) {
	var x uint64
	var s uint
	for i, b := range buf {
		if b < 0x80 {
			if i >= 10 || i == 9 && b > 1 {
				return 0, -(i + 1) // overflow
			}
			return x | uint64(b)<<s, i + 1
		}
		x |= uint64(b&0x7f) << s
		s += 7
	}
	return 0, 0
}

func extractRar5FileName(buf []byte) string {
	// Look for standard utf-8 string in RAR5 file header body
	if len(buf) < 10 {
		return ""
	}
	// Skip attribute varint, comp flags varint, OS varint, then name len
	p := 0
	_, n1 := readUvarint(buf[p:]) // attr
	p += n1
	if p >= len(buf) {
		return ""
	}
	_, n2 := readUvarint(buf[p:]) // compFlags
	p += n2
	if p >= len(buf) {
		return ""
	}
	_, n3 := readUvarint(buf[p:]) // OS
	p += n3
	if p >= len(buf) {
		return ""
	}
	nlen, n4 := readUvarint(buf[p:])
	p += n4
	if nlen > 0 && p+int(nlen) <= len(buf) {
		return cleanPath(string(buf[p : p+int(nlen)]))
	}
	return ""
}

func cleanPath(p string) string {
	// Trim trailing NULs or replace backslashes
	idx := bytes.IndexByte([]byte(p), 0)
	if idx >= 0 {
		p = p[:idx]
	}
	res := make([]rune, 0, len(p))
	for _, r := range p {
		if r == '\\' {
			res = append(res, '/')
		} else {
			res = append(res, r)
		}
	}
	return string(res)
}

// GroupByVolumeIdentity groups unplaced obfuscated volume files by their header identity.
func GroupByVolumeIdentity(files []IdentifiedFile) [][]UnpackableFile {
	byInner := make(map[string][]IdentifiedFile)
	for _, f := range files {
		if f.Identity == nil {
			continue
		}
		key := fmt.Sprintf("%s\x00%d", f.Identity.InnerName, f.Identity.InnerSize)
		byInner[key] = append(byInner[key], f)
	}

	var result [][]UnpackableFile
	for key, group := range byInner {
		// Sort by header volume number if all carry one, else by NZB index position
		sortGroup(group)

		if !isCompleteChain(group) {
			logger.Debug("Dropping incomplete header-grouped RAR chain", "key", key, "count", len(group))
			continue
		}

		if len(group) == 1 && !group[0].Identity.IsFirstVolume {
			continue
		}

		firstCount := 0
		for _, m := range group {
			if m.Identity.IsFirstVolume {
				firstCount++
			}
		}
		if firstCount > 1 {
			continue
		}

		set := make([]UnpackableFile, len(group))
		for i, m := range group {
			set[i] = m.File
		}
		result = append(result, set)
	}

	return result
}

func sortGroup(group []IdentifiedFile) {
	hasAllVolNums := true
	for _, m := range group {
		if m.Identity.VolumeNumber == nil && !m.Identity.IsFirstVolume {
			hasAllVolNums = false
			break
		}
	}

	if hasAllVolNums {
		sort.SliceStable(group, func(i, j int) bool {
			ni := 0
			if group[i].Identity.VolumeNumber != nil {
				ni = *group[i].Identity.VolumeNumber
			}
			nj := 0
			if group[j].Identity.VolumeNumber != nil {
				nj = *group[j].Identity.VolumeNumber
			}
			return ni < nj
		})
		return
	}

	sort.SliceStable(group, func(i, j int) bool {
		return group[i].Index < group[j].Index
	})
}

func isCompleteChain(group []IdentifiedFile) bool {
	for i, m := range group {
		continuedFromPrev := m.Identity.ContinuedFromPrevious
		continuesInNext := m.Identity.ContinuesInNext

		expectedPrev := i > 0
		expectedNext := i < len(group)-1

		if continuedFromPrev != expectedPrev || continuesInNext != expectedNext {
			return false
		}
	}
	return true
}
