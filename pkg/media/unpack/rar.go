package unpack

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"

	"streamnzb/pkg/core/logger"

	"github.com/javi11/rardecode/v2"
)

const likelyPAR2RepairRequiredMsg = "likely PAR2 repair required"

type ArchiveBlueprint struct {
	MainFileName string
	TotalSize    int64
	Parts        []VirtualPartDef
	IsCompressed bool
	AnyEncrypted bool
	Target       EpisodeTarget
}

type VirtualPartDef struct {
	VirtualStart int64
	VirtualEnd   int64
	VolFile      UnpackableFile
	VolOffset    int64
	AesKey       []byte
	AesIV        []byte
}

func StreamFromBlueprint(ctx context.Context, bp *ArchiveBlueprint, password string) (io.ReadSeekCloser, string, int64, error) {
	if bp.IsCompressed {
		return nil, "", 0, fmt.Errorf("compressed RAR archive (file: %s) -- STORE mode required for streaming", bp.MainFileName)
	}

	// Password-protected RAR5 releases sometimes list the first volume without
	// AnyEncrypted/file Encrypted flags (ListTolerant on part01 only). Route any
	// passworded release through rardecode so playback sees decrypted bytes.
	if bp.AnyEncrypted || password != "" {
		if password == "" {
			return nil, "", 0, fmt.Errorf("password-protected RAR (file: %s) -- password required from NZB head", bp.MainFileName)
		}
		return streamEncryptedRAR(ctx, bp, password)
	}

	parts := make([]virtualPart, len(bp.Parts))
	for i, p := range bp.Parts {
		parts[i] = virtualPart(p)
	}
	return NewVirtualStream(ctx, parts, bp.TotalSize, 0), bp.MainFileName, bp.TotalSize, nil
}

func streamEncryptedRAR(ctx context.Context, bp *ArchiveBlueprint, password string) (io.ReadSeekCloser, string, int64, error) {
	if err := contextErr(ctx); err != nil {
		return nil, "", 0, err
	}

	var aesKey, aesIV []byte
	var packedSize int64
	parts := make([]virtualPart, len(bp.Parts))
	for i, p := range bp.Parts {
		parts[i] = virtualPart{
			VirtualStart: p.VirtualStart,
			VirtualEnd:   p.VirtualEnd,
			VolFile:      p.VolFile,
			VolOffset:    p.VolOffset,
			AesKey:       p.AesKey,
			AesIV:        p.AesIV,
		}
		packedSize += (p.VirtualEnd - p.VirtualStart)
		if len(p.AesKey) > 0 && len(aesKey) == 0 {
			aesKey = p.AesKey
			aesIV = p.AesIV
		}
	}

	if len(aesKey) == 0 {
		// Fall back to standard unencrypted stream. The archive is either not
		// encrypted, or we don't have the correct password/keys to decrypt it.
		logger.Debug("Encrypted virtual stream fallback to plaintext", "file", bp.MainFileName)
		return NewVirtualStream(ctx, parts, bp.TotalSize, 0), bp.MainFileName, bp.TotalSize, nil
	}

	stream := NewEncryptedVirtualStream(ctx, parts, bp.TotalSize, packedSize, aesKey, aesIV, 0)
	return stream, bp.MainFileName, bp.TotalSize, nil
}


// maxFirstVolumesToScan caps how many "first" volumes we try when many files are
// treated as first volumes (e.g. non-standard names like .100, .101). Prevents
// failing slowly across hundreds of volumes; we try a few and fail fast.
const maxFirstVolumesToScan = 5

func withRARFastFailoverMode(ctx context.Context, enabled bool) context.Context {
	return WithArchiveFastFailoverMode(ctx, enabled)
}

func isRARFastFailoverModeEnabled(ctx context.Context) bool {
	return IsArchiveFastFailoverModeEnabled(ctx)
}

type firstVolumeCandidate struct {
	file  UnpackableFile
	score int
	order int
}

func selectFirstVolumesForFastScan(files []UnpackableFile, limit int) []UnpackableFile {
	if len(files) <= limit {
		out := make([]UnpackableFile, len(files))
		copy(out, files)
		return out
	}
	candidates := make([]firstVolumeCandidate, 0, len(files))
	for idx, f := range files {
		candidates = append(candidates, firstVolumeCandidate{
			file:  f,
			score: firstVolumeScore(f, idx, len(files)),
			order: idx,
		})
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].score == candidates[j].score {
			return candidates[i].order < candidates[j].order
		}
		return candidates[i].score > candidates[j].score
	})
	out := make([]UnpackableFile, 0, limit)
	for i := 0; i < len(candidates) && i < limit; i++ {
		out = append(out, candidates[i].file)
	}
	return out
}

func firstVolumeScore(file UnpackableFile, index, total int) int {
	name := strings.ToLower(ExtractFilename(file.Name()))
	score := 0

	// Prefer names that are likely canonical first volumes.
	switch {
	case strings.HasSuffix(name, ExtRar) && !strings.Contains(name, ".part"):
		score += 1200
	case strings.Contains(name, ".part001.") || strings.Contains(name, ".part01."):
		score += 1100
	case strings.Contains(name, ".part1."):
		score += 1000
	case strings.HasSuffix(name, ".001"):
		score += 900
	case strings.HasSuffix(name, ".r00"):
		score += 300
	}

	if IsMiddleRarVolume(name) {
		score -= 800
	}

	// NZB-order tie-breaker: keep earlier files slightly preferred.
	if total > 0 {
		score += (total - index)
	}

	// Subject counters often indicate segment/part ordering when filenames are obfuscated.
	if part, ok := parseSubjectPartCounter(strings.ToLower(file.Name())); ok {
		switch {
		case part == 1:
			score += 200
		case part <= 3:
			score += 100
		default:
			score -= 50
		}
	}

	return score
}

func parseSubjectPartCounter(subject string) (int, bool) {
	closeIdx := strings.Index(subject, "]")
	if closeIdx <= 0 {
		return 0, false
	}
	openIdx := strings.LastIndex(subject[:closeIdx], "[")
	if openIdx < 0 || openIdx+1 >= closeIdx {
		return 0, false
	}
	token := strings.TrimSpace(subject[openIdx+1 : closeIdx])
	slashIdx := strings.Index(token, "/")
	if slashIdx <= 0 {
		return 0, false
	}
	partStr := strings.TrimSpace(token[:slashIdx])
	part := 0
	for _, r := range partStr {
		if r < '0' || r > '9' {
			return 0, false
		}
		part = part*10 + int(r-'0')
	}
	if part <= 0 {
		return 0, false
	}
	return part, true
}

func shouldRetryExhaustiveRARScan(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrTooManyZeroFills) {
		return false
	}
	return !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(likelyPAR2RepairRequiredMsg))
}

func ScanArchive(ctx context.Context, files []UnpackableFile, password string, target EpisodeTarget) (*ArchiveBlueprint, error) {
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	ctx = WithArchiveScanIOTrace(ctx)
	scanTraceReadPos.Store(0)
	rarFiles := filterRarFiles(files)
	if len(rarFiles) == 0 {
		return nil, errors.New("no RAR files found")
	}

	allFirstVols := filterFirstVolumes(rarFiles)
	firstVols := make([]UnpackableFile, len(allFirstVols))
	copy(firstVols, allFirstVols)
	tryingCount := len(firstVols)
	if tryingCount > maxFirstVolumesToScan {
		firstVols = selectFirstVolumesForFastScan(firstVols, maxFirstVolumesToScan)
		logger.Debug("Limiting RAR first-volume scan to fail fast", "trying", len(firstVols), "skipped", tryingCount-len(firstVols))
	}
	runScan := func(scanFirstVols []UnpackableFile, mode string) (*ArchiveBlueprint, error) {
		logger.Debug("Scanning RAR first volumes", "target", target, "count", len(scanFirstVols), "total", len(rarFiles), "mode", mode)
		start := time.Now()
		parts, diagnostics, err := scanVolumesParallel(ctx, scanFirstVols, password)
		if err != nil {
			return nil, err
		}

		for _, f := range scanFirstVols {
			if fc, ok := f.(interface{ IsFailed() bool }); ok && fc.IsFailed() {
				logger.Error("First volume failed too many segments, aborting scan", "file", f.Name())
				return nil, fmt.Errorf("first volume unavailable: %w", ErrTooManyZeroFills)
			}
		}

		logger.Info("RAR scan complete", "files", len(rarFiles), "duration", time.Since(start), "mode", mode)
		if diagnostics.failedScans > 0 {
			logger.Debug("RAR scan diagnostics",
				"mode", mode,
				"failed_scans", diagnostics.failedScans,
				"invalid_blocks", diagnostics.invalidBlocks,
				"unexpected_eof", diagnostics.unexpectedEOF,
				"decode_corruption", diagnostics.decodeCorruption)
		}
		if len(parts) == 0 {
			if err := diagnostics.classifyArchiveError(); err != nil {
				return nil, err
			}
		}

		for _, p := range parts {
			if p.isCompressed {
				return nil, fmt.Errorf("compressed RAR archive (file: %s) -- STORE mode required for streaming", p.name)
			}
		}

		return buildBlueprint(ctx, parts, rarFiles, password, target)
	}

	bp, err := runScan(firstVols, "fast")
	if err == nil {
		return bp, nil
	}

	// Obfuscated sets can have random volume names where alphabetical top-N may
	// miss the true first volume. Keep fail-fast behavior, but retry exhaustively
	// once before giving up.
	if isRARFastFailoverModeEnabled(ctx) {
		logger.Warn("RAR fast failover mode enabled: skipping exhaustive first-volume scan",
			"target", target,
			"fast_count", len(firstVols),
			"full_count", len(allFirstVols),
			"err", err)
		return nil, err
	}
	if len(firstVols) < len(allFirstVols) && shouldRetryExhaustiveRARScan(err) {
		logger.Warn("RAR fast first-volume scan failed, retrying exhaustive first-volume scan",
			"target", target,
			"fast_count", len(firstVols),
			"full_count", len(allFirstVols),
			"err", err)
		if bpFull, errFull := runScan(allFirstVols, "full"); errFull == nil {
			return bpFull, nil
		} else {
			return nil, errFull
		}
	}
	if len(firstVols) < len(allFirstVols) {
		logger.Warn("Skipping exhaustive RAR first-volume scan after conclusive fast-scan failure",
			"target", target,
			"fast_count", len(firstVols),
			"full_count", len(allFirstVols),
			"err", err)
	}

	return nil, err
}

func InspectRAR(files []UnpackableFile) (string, error) {
	if len(files) == 0 {
		return "", errors.New("no files provided")
	}

	firstVol := findFirstVolume(files)
	if firstVol == nil {
		return "", errors.New("no valid RAR volume found")
	}

	stream, err := firstVol.OpenStream()
	if err != nil {
		return "", fmt.Errorf("failed to open stream: %w", err)
	}
	defer stream.Close()

	r, err := rardecode.NewReader(stream)
	if err != nil {
		return "", fmt.Errorf("failed to open rar: %w", err)
	}

	for i := 0; i < 50; i++ {
		header, err := r.Next()
		if header != nil && !header.IsDir && IsVideoFile(header.Name) {
			return header.Name, nil
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			if strings.Contains(err.Error(), "multi-volume archive") {
				break
			}
			return "", err
		}
	}
	return "", errors.New("no video found in rar")
}

type filePart struct {
	name         string
	unpackedSize int64
	dataOffset   int64
	packedSize   int64
	volFile      UnpackableFile
	volName      string
	isMedia      bool
	isCompressed bool
	isEncrypted  bool
	aesKey       []byte
	aesIV        []byte
}

type scanDiagnostics struct {
	failedScans      int
	evidenceScans    int
	invalidBlocks    int
	unexpectedEOF    int
	decodeCorruption int
}

func (d scanDiagnostics) classifyArchiveError() error {
	if d.failedScans == 0 {
		return nil
	}
	// Require explicit decode-corruption evidence; "invalid file block"
	// alone can happen when scanning non-first volumes in obfuscated sets.
	if d.decodeCorruption == 0 {
		return nil
	}
	// Classify as likely corruption only when most failures look like
	// broken article/volume bytes rather than generic parse errors.
	evidence := d.evidenceScans
	if evidence == 0 {
		return nil
	}
	if evidence*2 < d.failedScans {
		return nil
	}
	return fmt.Errorf(
		"archive data appears corrupted or incomplete (%d/%d corrupt scans; invalid_blocks=%d unexpected_eof=%d decode_corruption=%d): %s",
		evidence,
		d.failedScans,
		d.invalidBlocks,
		d.unexpectedEOF,
		d.decodeCorruption,
		likelyPAR2RepairRequiredMsg,
	)
}

func scanVolumesParallel(ctx context.Context, files []UnpackableFile, password string) ([]filePart, scanDiagnostics, error) {
	var mu sync.Mutex
	var result []filePart
	diag := scanDiagnostics{}
	sem := make(chan struct{}, 20)
	var wg sync.WaitGroup
	var firstErr error
	var firstErrMu sync.Mutex

	setFirstErr := func(err error) {
		if err == nil {
			return
		}
		firstErrMu.Lock()
		if firstErr == nil {
			firstErr = err
		}
		firstErrMu.Unlock()
	}

	for _, f := range files {
		if err := contextErr(ctx); err != nil {
			setFirstErr(err)
			break
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(f UnpackableFile) {
			defer wg.Done()
			defer func() { <-sem }()
			defer func() {
				if r := recover(); r != nil {
					logger.Error("Panic scanning RAR", "file", f.Name(), "err", r)
				}
			}()
			if err := contextErr(ctx); err != nil {
				setFirstErr(err)
				return
			}

			cleanName := ExtractFilename(f.Name())
			fsys := NewNZBFSFromMapCtx(ctx, map[string]UnpackableFile{cleanName: f})
			// ListTolerant: this maps only the single first volume; a split file that
			// continues into sibling volumes must still be discoverable here. The full
			// per-volume layout is stitched later by aggregateRemainingVolumes.
			listOpts := []rardecode.Option{rardecode.FileSystem(fsys), rardecode.ParallelRead(false), rardecode.SkipVolumeCheck, rardecode.ListTolerant}
			if password != "" {
				listOpts = append(listOpts, rardecode.Password(password))
			}
			infos, err := rardecode.ListArchiveInfo(cleanName, listOpts...)
			if err != nil {
				if ctxErr := contextErr(ctx); ctxErr != nil {
					setFirstErr(ctxErr)
					return
				}
				logger.Debug("Scan failure", "name", cleanName, "err", err, "virtual_size", f.Size())
				logRARScanDebug(f, err)
				mu.Lock()
				diag.failedScans++
				msg := strings.ToLower(err.Error())
				scanHasEvidence := false
				if strings.Contains(msg, "invalid file block") {
					diag.invalidBlocks++
					scanHasEvidence = true
				}
				if strings.Contains(msg, "unexpected eof") {
					diag.unexpectedEOF++
					scanHasEvidence = true
				}
				if strings.Contains(msg, "data corruption detected") ||
					strings.Contains(msg, "without finding \"=yend\" trailer") ||
					strings.Contains(msg, "without finding '=yend' trailer") ||
					strings.Contains(msg, "rapidyenc") {
					diag.decodeCorruption++
					scanHasEvidence = true
				}
				if scanHasEvidence {
					diag.evidenceScans++
				}
				mu.Unlock()
			}

			for _, info := range infos {
				if err := contextErr(ctx); err != nil {
					setFirstErr(err)
					return
				}
				if info.Name == "" {
					continue
				}
				logger.Debug("Found file in archive", "vol", cleanName, "name", info.Name, "size", info.TotalUnpackedSize)

				compressed := false
				for _, p := range info.Parts {
					if p.CompressionMethod != "stored" {
						compressed = true
					}
				}

				fileEncrypted := info.AnyEncrypted
				if !fileEncrypted {
					for _, p := range info.Parts {
						if p.Encrypted || len(p.AesKey) > 0 {
							fileEncrypted = true
							break
						}
					}
				}

				for _, p := range info.Parts {
					mu.Lock()
					result = append(result, filePart{
						name:         info.Name,
						unpackedSize: info.TotalUnpackedSize,
						dataOffset:   p.DataOffset,
						packedSize:   p.PackedSize,
						volFile:      f,
						volName:      f.Name(),
						isMedia:      isMediaFile(info),
						isCompressed: compressed,
						isEncrypted:  fileEncrypted,
						aesKey:       p.AesKey,
						aesIV:        p.AesIV,
					})
					mu.Unlock()
				}
			}
		}(f)
	}
	wg.Wait()
	if firstErr != nil {
		return nil, scanDiagnostics{}, firstErr
	}
	return result, diag, nil
}

func buildBlueprint(ctx context.Context, parts []filePart, allRarFiles []UnpackableFile, password string, target EpisodeTarget) (*ArchiveBlueprint, error) {
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	bestName, err := selectMainFile(parts, target)
	if err != nil {
		logger.Debug("RAR blueprint direct media selection failed for requested episode, trying nested archive", "target", target, "err", err)
		if bp, nestedErr := tryNestedArchive(ctx, parts, password, target); nestedErr == nil {
			return bp, nil
		}
		return nil, err
	}
	logger.Debug("RAR blueprint main file decision", "target", target, "selected", bestName, "parts", len(parts), "rar_files", len(allRarFiles))

	if bestName != "" {
		var mediaTotal, archiveTotal int64
		for _, p := range parts {
			if p.isMedia {
				mediaTotal += p.packedSize
			} else if IsArchiveFile(p.name) {
				archiveTotal += p.packedSize
			}
		}
		if archiveTotal > mediaTotal*2 {
			logger.Info("Archive content outweighs direct media, trying nested archive first",
				"media", mediaTotal, "archive", archiveTotal, "sample", bestName)
			if bp, err := tryNestedArchive(ctx, parts, password, target); err == nil {
				return bp, nil
			}
		}
	}

	if bestName == "" {
		logger.Debug("RAR blueprint found no direct media match, trying nested archive", "target", target, "parts", len(parts))
		return tryNestedArchive(ctx, parts, password, target)
	}

	logger.Info("Selected main media", "target", target, "name", bestName)

	mainParts := collectParts(parts, bestName)
	sortByVolume(mainParts)

	headerSize := mainParts[0].unpackedSize
	scannedSize := totalPackedSize(mainParts)

	if scannedSize < headerSize && len(allRarFiles) > len(mainParts) {
		mainParts, err = aggregateRemainingVolumes(ctx, mainParts, allRarFiles, bestName, headerSize, password)
		if err != nil {
			return nil, err
		}
	}

	compressed := false
	anyEncrypted := false
	for _, p := range mainParts {
		if p.isCompressed {
			compressed = true
		}
		if p.isEncrypted {
			anyEncrypted = true
		}
	}
	if password != "" {
		anyEncrypted = true
	}

	bp := &ArchiveBlueprint{
		MainFileName: bestName,
		TotalSize:    headerSize,
		IsCompressed: compressed,
		AnyEncrypted: anyEncrypted,
		Target:       target,
	}

	var vOffset int64
	for i, p := range mainParts {
		bp.Parts = append(bp.Parts, VirtualPartDef{
			VirtualStart: vOffset,
			VirtualEnd:   vOffset + p.packedSize,
			VolFile:      p.volFile,
			VolOffset:    p.dataOffset,
			AesKey:       p.aesKey,
			AesIV:        p.aesIV,
		})
		if i < 3 || i >= len(mainParts)-2 {
			logger.Trace("Blueprint part", "idx", i, "vStart", vOffset, "vEnd", vOffset+p.packedSize, "volOff", p.dataOffset, "packed", p.packedSize)
		}
		vOffset += p.packedSize
	}

	logger.Trace("Blueprint total", "vOffset", vOffset, "headerSize", headerSize, "parts", len(mainParts))

	// Packed volume totals can be smaller than unpacked media size; only clamp
	// for direct VirtualStream reads. Decrypting streams must keep unpacked size.
	if vOffset < headerSize && !anyEncrypted && password == "" {
		logger.Debug("Adjusting stream size", "header", headerSize, "actual", vOffset)
		bp.TotalSize = vOffset
	}

	return bp, nil
}

func selectMainFile(parts []filePart, target EpisodeTarget) (string, error) {
	type mediaChoice struct {
		size  int64
		order int
	}
	choices := make(map[string]*mediaChoice)
	order := 0
	for _, p := range parts {
		if p.isMedia {
			choice, ok := choices[p.name]
			if !ok {
				choice = &mediaChoice{order: order}
				choices[p.name] = choice
				order++
			}
			choice.size += p.packedSize
		}
	}
	candidates := make([]namedEpisodeCandidate, 0, len(choices))
	for name, choice := range choices {
		candidates = append(candidates, namedEpisodeCandidate{Name: name, Size: choice.size, Order: choice.order})
	}
	if len(candidates) == 0 {
		logger.Debug("RAR main file selection found no media candidates", "target", target)
		return "", nil
	}
	if best, ok, err := selectEpisodeCandidateOrError(candidates, target, "rar_main_media"); err != nil {
		return "", err
	} else if ok {
		logger.Debug("RAR main file selected by episode match", "target", target, "name", best.Name, "size", best.Size, "order", best.Order, "candidates", len(candidates))
		return best.Name, nil
	}
	var best namedEpisodeCandidate
	found := false
	for _, candidate := range candidates {
		if !found || candidate.Size > best.Size || (candidate.Size == best.Size && candidate.Order < best.Order) {
			best = candidate
			found = true
		}
	}
	logger.Debug("RAR main file selection fell back to largest candidate", "target", target, "name", best.Name, "size", best.Size, "order", best.Order, "candidates", len(candidates))
	return best.Name, nil
}

func collectParts(parts []filePart, name string) []filePart {
	var result []filePart
	for _, p := range parts {
		if p.name == name {
			result = append(result, p)
		}
	}
	return result
}

func totalPackedSize(parts []filePart) int64 {
	var total int64
	for _, p := range parts {
		total += p.packedSize
	}
	return total
}

func aggregateRemainingVolumes(ctx context.Context, mainParts []filePart, allRarFiles []UnpackableFile, name string, headerSize int64, password string) ([]filePart, error) {
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	sort.Slice(allRarFiles, func(i, j int) bool {
		return GetRARVolumeNumber(allRarFiles[i].Name()) < GetRARVolumeNumber(allRarFiles[j].Name())
	})

	startVol := mainParts[0].volName
	startIdx := -1
	for i, f := range allRarFiles {
		if f.Name() == startVol {
			startIdx = i
			break
		}
	}
	if startIdx == -1 {
		return mainParts, nil
	}

	probe, err := probeContinuation(ctx, allRarFiles, startIdx, name, password)
	if err != nil {
		return nil, err
	}
	if probe.dataOffset > 0 {
		logger.Trace("Probed continuation volume", "dataOffset", probe.dataOffset, "packedSize", probe.packedSize)
	}

	return aggregateRemainingVolumesFromStart(ctx, mainParts, allRarFiles, startIdx, name, headerSize, probe)
}

func aggregateRemainingVolumesFromStart(ctx context.Context, mainParts []filePart, allRarFiles []UnpackableFile, startIdx int, name string, headerSize int64, probe continuationProbe) ([]filePart, error) {
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	first := mainParts[0]
	result := []filePart{first}

	numContVolumes := len(allRarFiles) - startIdx - 1
	if numContVolumes <= 0 {
		return result, nil
	}

	contPackedSize := probe.packedSize
	contDataOffset := probe.dataOffset

	// When the fast continuation probe is unavailable we must learn each remaining
	// volume's decoded size. Probing volumes one-by-one is the dominant startup
	// cost (each probe is a round of segment downloads), so prime them concurrently
	// up front; the sequential loop below then hits already-cached segment maps.
	if contPackedSize <= 0 {
		if err := primeContinuationSegmentMaps(ctx, allRarFiles[startIdx+1:]); err != nil {
			return nil, err
		}
	}

	var lastPartData int64
	if contPackedSize > 0 && numContVolumes > 1 {
		lastPartData = headerSize - first.packedSize - int64(numContVolumes-1)*contPackedSize
	} else if contPackedSize > 0 && numContVolumes == 1 {
		lastPartData = headerSize - first.packedSize
	}

	added := 0
	for i := startIdx + 1; i < len(allRarFiles); i++ {
		if err := contextErr(ctx); err != nil {
			return nil, err
		}
		f := allRarFiles[i]
		fileSize := int64(0)
		if contPackedSize <= 0 {
			if err := ensureSegmentMap(ctx, f); err != nil {
				return nil, err
			}
			fileSize = f.Size()
			if fileSize <= 0 {
				continue
			}
		}

		isLastVolume := i == len(allRarFiles)-1
		var dataSize int64
		if contPackedSize > 0 {
			if isLastVolume && lastPartData > 0 {
				dataSize = lastPartData
			} else if !isLastVolume {
				dataSize = contPackedSize
			} else {

				dataSize = contPackedSize
			}
		} else {

			dataSize = fileSize - contDataOffset
		}

		if dataSize <= 0 {
			continue
		}
		volDataOffset := contDataOffset

		result = append(result, filePart{
			name:         name,
			unpackedSize: headerSize,
			dataOffset:   volDataOffset,
			packedSize:   dataSize,
			volFile:      f,
			volName:      f.Name(),
			isMedia:      true,
			isCompressed: first.isCompressed,
			isEncrypted:  first.isEncrypted,
			aesKey:       first.aesKey,
			aesIV:        first.aesIV,
		})
		added++
	}
	logger.Trace("Manual volume aggregation", "added", added, "total", len(result))
	return result, nil
}

// maxContinuationProbeConcurrency bounds how many continuation volumes we probe
// at once so we speed up startup without flooding the NNTP connection pool.
// Each volume probe fans out into ~60 segment fetches, so keep this small to
// avoid saturating the pool (which shows up as "NNTP pool wait exceeded
// threshold" and stalls real playback reads).
const maxContinuationProbeConcurrency = 3

// primeContinuationSegmentMaps probes the segment maps of the given volumes
// concurrently so the subsequent sequential aggregation hits cached maps. It
// returns the first error encountered (other than per-volume sizing issues,
// which the aggregation loop already tolerates by skipping empty volumes).
func primeContinuationSegmentMaps(ctx context.Context, files []UnpackableFile) error {
	if len(files) == 0 {
		return nil
	}
	if err := contextErr(ctx); err != nil {
		return err
	}

	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		firstErr error
		sem      = make(chan struct{}, maxContinuationProbeConcurrency)
	)

	for _, f := range files {
		f := f
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				mu.Lock()
				if firstErr == nil {
					firstErr = ctx.Err()
				}
				mu.Unlock()
				return
			}
			defer func() { <-sem }()

			if err := ensureSegmentMap(ctx, f); err != nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	// Only surface context cancellation as fatal; individual volume probe errors
	// are handled (and may be retried) by the aggregation loop.
	if firstErr != nil && isContextErr(firstErr) {
		return firstErr
	}
	return nil
}

type continuationProbe struct {
	dataOffset int64
	packedSize int64
}

func probeContinuation(ctx context.Context, allRarFiles []UnpackableFile, startIdx int, targetName string, password string) (continuationProbe, error) {
	if err := contextErr(ctx); err != nil {
		return continuationProbe{}, err
	}
	if startIdx+1 >= len(allRarFiles) {
		return continuationProbe{}, nil
	}

	firstFile := allRarFiles[startIdx]
	secondFile := allRarFiles[startIdx+1]
	firstName := ExtractFilename(firstFile.Name())
	secondName := ExtractFilename(secondFile.Name())

	fsys := NewNZBFSFromMapCtx(ctx, map[string]UnpackableFile{
		firstName:  firstFile,
		secondName: secondFile,
	})
	// ListTolerant lets the split file resolve across the two mounted volumes and
	// return both parts even though the archive continues into volumes not mounted
	// here; we only need Parts[1] (the per-continuation-volume packed size).
	listOpts := []rardecode.Option{rardecode.FileSystem(fsys), rardecode.ParallelRead(false), rardecode.SkipVolumeCheck, rardecode.ListTolerant}
	if password != "" {
		listOpts = append(listOpts, rardecode.Password(password))
	}
	infos, err := rardecode.ListArchiveInfo(firstName, listOpts...)
	if err != nil {
		if ctxErr := contextErr(ctx); ctxErr != nil {
			return continuationProbe{}, ctxErr
		}
		logger.Debug("Continuation probe failed, falling back to zero offset", "err", err)
		return continuationProbe{}, nil
	}

	// Gather every part of the target file across all returned entries, in the
	// order rardecode discovered them (volume order). Depending on how the split
	// continuation block in the second volume is flagged, rardecode may either
	// merge both volumes into a single entry with 2 parts, or emit two separate
	// single-part entries for the same name. Both layouts mean the same thing:
	// the second part overall is the per-continuation-volume data block we want.
	lowerTarget := strings.ToLower(targetName)
	var targetParts []rardecode.FilePartInfo
	for _, info := range infos {
		if err := contextErr(ctx); err != nil {
			return continuationProbe{}, err
		}
		if strings.ToLower(info.Name) != lowerTarget {
			continue
		}
		targetParts = append(targetParts, info.Parts...)
	}

	logger.Debug("Continuation probe result",
		"target", targetName,
		"infos", len(infos),
		"target_parts", len(targetParts),
		"first_volume", firstName,
		"second_volume", secondName)

	if len(targetParts) >= 2 {
		probe := normalizeContinuationProbe(targetParts)
		logger.Debug("Continuation probe metadata",
			"target", targetName,
			"dataOffset", probe.dataOffset,
			"packedSize", probe.packedSize,
			"first_part_packed", targetParts[0].PackedSize,
			"second_part_packed", targetParts[1].PackedSize)
		return probe, nil
	}
	return continuationProbe{}, nil
}

func normalizeContinuationProbe(parts []rardecode.FilePartInfo) continuationProbe {
	if len(parts) < 2 {
		return continuationProbe{}
	}
	probe := continuationProbe{
		dataOffset: parts[1].DataOffset,
		packedSize: parts[1].PackedSize,
	}
	// RAR5 split continuations can expose a valid dataOffset while PackedSize is
	// left zero on the second list entry; reuse the first part's packed span.
	if probe.packedSize <= 0 && parts[0].PackedSize > 0 {
		probe.packedSize = parts[0].PackedSize
	}
	return probe
}

func tryNestedArchive(ctx context.Context, parts []filePart, password string, target EpisodeTarget) (*ArchiveBlueprint, error) {
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	if len(parts) == 0 {
		return nil, errors.New("empty archive")
	}

	type archiveSet struct {
		totalSize int64
		parts     []filePart
		order     int
	}
	sets := make(map[string]*archiveSet)
	order := 0

	for _, p := range parts {
		if !IsArchiveFile(p.name) {
			continue
		}
		setName := archiveSetName(p.name)
		s, ok := sets[setName]
		if !ok {
			s = &archiveSet{order: order}
			sets[setName] = s
			order++
		}
		s.totalSize += p.packedSize
		s.parts = append(s.parts, p)
	}

	if len(sets) == 0 {
		return nil, errors.New("no video or nested archive found")
	}

	candidates := make([]namedEpisodeCandidate, 0, len(sets))
	for name, set := range sets {
		candidates = append(candidates, namedEpisodeCandidate{Name: name, Size: set.totalSize, Order: set.order})
	}
	bestSet := ""
	maxSize := int64(0)
	if best, ok, err := selectEpisodeCandidateOrError(candidates, target, "nested_archive_sets"); err != nil {
		return nil, err
	} else if ok {
		bestSet = best.Name
		maxSize = best.Size
		logger.Debug("Nested archive set selected by episode match", "target", target, "set", bestSet, "size", maxSize, "candidates", len(candidates))
	} else {
		for _, candidate := range candidates {
			if candidate.Size > maxSize {
				maxSize = candidate.Size
				bestSet = candidate.Name
			}
		}
		logger.Debug("Nested archive set selection fell back to largest candidate", "target", target, "set", bestSet, "size", maxSize, "candidates", len(candidates))
	}

	nestedParts := sets[bestSet].parts
	logger.Info("Detected nested archive", "set", bestSet, "size", maxSize, "volumes", len(nestedParts))
	for _, p := range nestedParts {
		logger.Trace("Nested archive part", "name", p.name, "volName", p.volName, "packed", p.packedSize, "unpacked", p.unpackedSize)
	}

	innerFiles := make(map[string][]filePart)
	for _, p := range nestedParts {
		innerFiles[p.name] = append(innerFiles[p.name], p)
	}

	var nestedFiles []UnpackableFile
	for name, fps := range innerFiles {
		sortByVolume(fps)

		compressed := false
		var vfParts []virtualPart
		var vOffset int64
		for _, p := range fps {
			if p.isCompressed {
				compressed = true
			}
			vfParts = append(vfParts, virtualPart{
				VirtualStart: vOffset,
				VirtualEnd:   vOffset + p.packedSize,
				VolFile:      p.volFile,
				VolOffset:    p.dataOffset,
			})
			vOffset += p.packedSize
		}

		if compressed {
			return nil, fmt.Errorf("nested archive %s is compressed", name)
		}

		totalSize := fps[0].unpackedSize
		if totalSize == 0 {
			totalSize = vOffset
		}
		nestedFiles = append(nestedFiles, NewVirtualFile(name, totalSize, vfParts))
	}

	for _, nf := range nestedFiles {
		logger.Debug("Nested VirtualFile", "name", nf.Name(), "size", nf.Size(), "extracted", ExtractFilename(nf.Name()))
	}
	logger.Info("Recursively scanning nested archive", "set", bestSet, "volumes", len(nestedFiles))
	return ScanArchive(ctx, nestedFiles, password, target)
}

func filterRarFiles(files []UnpackableFile) []UnpackableFile {
	var result []UnpackableFile
	for _, f := range files {
		name := ExtractFilename(f.Name())
		lower := strings.ToLower(name)
		if strings.HasSuffix(lower, ExtPar2) {
			logger.Trace("filterRarFiles: skip par2", "name", name)
			continue
		}

		if strings.Contains(lower, ".7z.") || strings.HasSuffix(lower, ".7z") {
			logger.Trace("filterRarFiles: skip 7z", "name", name)
			continue
		}
		if strings.HasSuffix(lower, ExtRar) || strings.Contains(lower, ".part") || IsRarPart(lower) || IsSplitArchivePart(lower) {
			result = append(result, f)
		} else {
			logger.Trace("filterRarFiles: skip non-rar", "name", name)
		}
	}
	return result
}

func filterFirstVolumes(files []UnpackableFile) []UnpackableFile {
	var result []UnpackableFile
	for _, f := range files {
		name := strings.ToLower(ExtractFilename(f.Name()))
		if strings.HasSuffix(name, ExtRar) && !strings.Contains(name, ".part") && !strings.Contains(name, ".r0") {
			logger.Trace("filterFirstVolumes: accept .rar first vol", "name", name)
			result = append(result, f)
			continue
		}
		if IsMiddleRarVolume(name) {
			logger.Trace("filterFirstVolumes: skip middle vol", "name", name)
			continue
		}
		logger.Trace("filterFirstVolumes: accept fallthrough", "name", name)
		result = append(result, f)
	}
	return result
}

func findFirstVolume(files []UnpackableFile) UnpackableFile {

	for _, f := range files {
		lower := strings.ToLower(f.Name())
		if strings.HasSuffix(lower, ".par2") || strings.HasSuffix(lower, ".nzb") || strings.HasSuffix(lower, ".nfo") {
			continue
		}
		if (strings.HasSuffix(lower, ".rar") && !strings.Contains(lower, ".part")) ||
			strings.Contains(lower, ".part01.") || strings.Contains(lower, ".part1.") ||
			strings.HasSuffix(lower, ".r00") || strings.HasSuffix(lower, ".001") {
			return f
		}
	}

	for _, f := range files {
		lower := strings.ToLower(f.Name())
		if strings.HasSuffix(lower, ".par2") || strings.HasSuffix(lower, ".nzb") || strings.HasSuffix(lower, ".nfo") {
			continue
		}
		if strings.HasSuffix(lower, ".rar") {
			return f
		}
	}
	return nil
}

func isMediaFile(info rardecode.ArchiveFileInfo) bool {
	name := info.Name
	lower := strings.ToLower(name)
	// .iso is not playable; do not treat as media so we don't select it as main file.
	if strings.HasSuffix(lower, ExtIso) {
		return false
	}
	// Do not select sample files (e.g. Sample/sample-foo.m2ts) as main media.
	if IsSampleFile(name) {
		return false
	}
	isVideo := IsVideoFile(name)
	isLarge := info.TotalUnpackedSize > 50*1024*1024
	isArchive := strings.HasSuffix(lower, ExtRar) || strings.HasSuffix(lower, ExtZip) ||
		strings.HasSuffix(lower, Ext7z) || strings.HasSuffix(lower, ExtPar2) || IsRarPart(lower)
	return isVideo || (isLarge && !isArchive)
}

func archiveSetName(name string) string {
	lower := strings.ToLower(name)
	if idx := strings.LastIndex(lower, ".part"); idx != -1 {
		return name[:idx]
	}
	if idx := strings.LastIndex(lower, ".r"); idx != -1 && idx > len(lower)-5 {
		return name[:idx]
	}
	if strings.HasSuffix(lower, ExtRar) {
		return strings.TrimSuffix(strings.TrimSuffix(name, ExtRar), ".RAR")
	}
	return name
}

func sortByVolume(parts []filePart) {
	sort.Slice(parts, func(i, j int) bool {
		return GetRARVolumeNumber(parts[i].volName) < GetRARVolumeNumber(parts[j].volName)
	})
}
