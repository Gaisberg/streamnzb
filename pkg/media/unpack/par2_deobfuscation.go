package unpack

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"path/filepath"
	"strings"

	"streamnzb/pkg/core/logger"
)

const (
	par2PacketHeaderSize = 64
	maxPar2ReadBytes     = 64 << 20 // 64 MB guardrail
)

var (
	par2PacketMagic    = []byte("PAR2\x00PKT")
	par2FileDescPacket = []byte("PAR 2.0\x00FileDesc")
)

func directFileDisplayName(files []UnpackableFile, idx int, nameOverrides map[int]string) string {
	if nameOverrides != nil {
		if name, ok := nameOverrides[idx]; ok && strings.TrimSpace(name) != "" {
			return name
		}
	}
	if idx < 0 || idx >= len(files) || files[idx] == nil {
		return ""
	}
	return ExtractFilename(files[idx].Name())
}

// recoverDirectFilenamesFromPAR2 tries to rename obfuscated direct media entries by
// parsing the smallest PAR2 index file's FileDesc packets. Returns nil if no safe
// one-to-one mapping can be established.
func recoverDirectFilenamesFromPAR2(ctx context.Context, files []UnpackableFile) map[int]string {
	obfuscatedMediaIdx := make([]int, 0)
	for i, f := range files {
		if f == nil {
			continue
		}
		name := ExtractFilename(f.Name())
		if !IsVideoFile(name) {
			continue
		}
		if !looksObfuscatedFilename(name) {
			continue
		}
		obfuscatedMediaIdx = append(obfuscatedMediaIdx, i)
	}
	if len(obfuscatedMediaIdx) == 0 {
		return nil
	}

	par2 := smallestPAR2File(files)
	if par2 == nil {
		return nil
	}

	stream, err := par2.OpenStreamCtx(ctx)
	if err != nil {
		logger.Debug("PAR2 deobfuscation: failed to open PAR2 stream", "err", err)
		return nil
	}
	defer stream.Close()

	raw, err := io.ReadAll(io.LimitReader(stream, maxPar2ReadBytes+1))
	if err != nil {
		logger.Debug("PAR2 deobfuscation: failed to read PAR2 stream", "err", err)
		return nil
	}
	if int64(len(raw)) > maxPar2ReadBytes {
		logger.Debug("PAR2 deobfuscation: PAR2 file too large, skipping", "bytes", len(raw))
		return nil
	}

	descs := parsePAR2FileDescNames(raw)
	if len(descs) == 0 {
		return nil
	}

	mediaNames := make([]string, 0, len(descs))
	for _, name := range descs {
		base := filepath.Base(strings.TrimSpace(name))
		if base == "" || !IsVideoFile(base) {
			continue
		}
		mediaNames = append(mediaNames, base)
	}
	if len(mediaNames) == 0 {
		return nil
	}
	if len(mediaNames) != len(obfuscatedMediaIdx) {
		logger.Debug("PAR2 deobfuscation: media count mismatch",
			"par2_media", len(mediaNames),
			"obfuscated_media", len(obfuscatedMediaIdx))
		return nil
	}

	allObfuscated := true
	for _, name := range mediaNames {
		if !looksObfuscatedFilename(name) {
			allObfuscated = false
			break
		}
	}
	if allObfuscated {
		return nil
	}

	out := make(map[int]string, len(obfuscatedMediaIdx))
	// Only apply positional overrides when we can prove identity mapping:
	// obfuscated media indexes must be an exact 0..N-1 sequence.
	identityMapping := true
	for i, idx := range obfuscatedMediaIdx {
		if idx != i {
			identityMapping = false
			break
		}
	}
	if !identityMapping {
		logger.Debug("PAR2 deobfuscation: skipping unsafe positional filename override",
			"obfuscated_media", len(obfuscatedMediaIdx))
		return nil
	}
	for i, idx := range obfuscatedMediaIdx {
		out[idx] = mediaNames[i]
	}
	return out
}

func smallestPAR2File(files []UnpackableFile) UnpackableFile {
	var best UnpackableFile
	for _, f := range files {
		if f == nil {
			continue
		}
		name := strings.ToLower(ExtractFilename(f.Name()))
		if !strings.HasSuffix(name, ExtPar2) {
			continue
		}
		if best == nil || f.Size() < best.Size() {
			best = f
		}
	}
	return best
}

func parsePAR2FileDescNames(raw []byte) []string {
	if len(raw) < par2PacketHeaderSize {
		return nil
	}
	seen := make(map[[16]byte]struct{})
	names := make([]string, 0)
	for off := 0; off+par2PacketHeaderSize <= len(raw); {
		if !bytes.Equal(raw[off:off+8], par2PacketMagic) {
			off++
			continue
		}
		packetLen := binary.LittleEndian.Uint64(raw[off+8 : off+16])
		if packetLen < par2PacketHeaderSize {
			off++
			continue
		}
		end := off + int(packetLen)
		if end > len(raw) {
			break
		}
		packetType := raw[off+48 : off+64]
		if bytes.Equal(packetType, par2FileDescPacket) {
			body := raw[off+par2PacketHeaderSize : end]
			if len(body) >= 56 {
				var fileID [16]byte
				copy(fileID[:], body[0:16])
				if _, exists := seen[fileID]; !exists {
					seen[fileID] = struct{}{}
					nameRaw := bytes.TrimRight(body[56:], "\x00")
					if len(nameRaw) > 0 {
						names = append(names, string(bytes.ToValidUTF8(nameRaw, nil)))
					}
				}
			}
		}
		off = end
	}
	return names
}

func looksObfuscatedFilename(filename string) bool {
	stem := strings.TrimSpace(filename)
	if stem == "" {
		return false
	}
	if dot := strings.LastIndexByte(stem, '.'); dot > 0 {
		stem = stem[:dot]
	}
	if stem == "" {
		return false
	}
	if strings.HasPrefix(strings.ToLower(stem), "abc.xyz") {
		return true
	}
	lower := strings.ToLower(stem)
	if len(lower) == 32 && isHex(lower) {
		return true
	}
	if len(lower) >= 40 {
		hexOrDot := true
		for _, c := range lower {
			if (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || c == '.' {
				continue
			}
			hexOrDot = false
			break
		}
		if hexOrDot {
			return true
		}
	}
	if len(stem) >= 24 && !strings.ContainsAny(stem, ". -_") {
		alphaNum := true
		for _, c := range stem {
			if (c >= '0' && c <= '9') || (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') {
				continue
			}
			alphaNum = false
			break
		}
		if alphaNum {
			return true
		}
	}
	return false
}

func isHex(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') {
			continue
		}
		return false
	}
	return true
}
