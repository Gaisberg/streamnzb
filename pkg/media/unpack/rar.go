package unpack

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"streamnzb/pkg/core/logger"

	"github.com/javi11/rardecode/v2"
)

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
}

func StreamFromBlueprint(ctx context.Context, bp *ArchiveBlueprint, password string) (io.ReadSeekCloser, string, int64, error) {
	if bp.IsCompressed {
		return nil, "", 0, fmt.Errorf("compressed RAR archive (file: %s) -- STORE mode required for streaming", bp.MainFileName)
	}

	if bp.AnyEncrypted {
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
	fileMap := make(map[string]UnpackableFile, len(bp.Parts))
	for _, p := range bp.Parts {
		name := ExtractFilename(p.VolFile.Name())
		fileMap[name] = p.VolFile
	}
	firstName := ExtractFilename(bp.Parts[0].VolFile.Name())
	fsys := NewNZBFSFromMapCtx(ctx, fileMap)

	opts := []rardecode.Option{rardecode.FileSystem(fsys), rardecode.Password(password)}
	rc, err := rardecode.OpenReader(firstName, opts...)
	if err != nil {
		return nil, "", 0, fmt.Errorf("open encrypted RAR: %w", err)
	}

	mainBase := filepath.Base(bp.MainFileName)
	for {
		if err := contextErr(ctx); err != nil {
			rc.Close()
			return nil, "", 0, err
		}
		h, err := rc.Next()
		if err != nil {
			rc.Close()
			if err == io.EOF {
				return nil, "", 0, fmt.Errorf("encrypted RAR: file %q not found", bp.MainFileName)
			}
			return nil, "", 0, fmt.Errorf("encrypted RAR next: %w", err)
		}
		if h.Name == bp.MainFileName || filepath.Base(h.Name) == mainBase {
			stream := &encryptedRARStream{
				ctx:          ctx,
				rc:           rc,
				limit:        bp.TotalSize,
				firstVolName: firstName,
				fileMap:      fileMap,
				password:     password,
				mainFileName: bp.MainFileName,
				mainBase:     mainBase,
			}
			return stream, bp.MainFileName, bp.TotalSize, nil
		}

		_, _ = io.Copy(io.Discard, io.LimitReader(rc, h.UnPackedSize))
	}
}

type encryptedRARStream struct {
	ctx          context.Context
	rc           *rardecode.ReadCloser
	limit        int64
	read         int64
	firstVolName string
	fileMap      map[string]UnpackableFile
	password     string
	mainFileName string
	mainBase     string
	mu           sync.Mutex
}

func (e *encryptedRARStream) Read(p []byte) (n int, err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.read >= e.limit {
		return 0, io.EOF
	}
	max := int64(len(p))
	if max > e.limit-e.read {
		max = e.limit - e.read
	}
	n, err = e.rc.Read(p[:max])
	if n > 0 {
		e.read += int64(n)
	}
	return n, err
}

func (e *encryptedRARStream) Seek(offset int64, whence int) (int64, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	var abs int64
	switch whence {
	case io.SeekStart:
		abs = offset
	case io.SeekCurrent:
		abs = e.read + offset
	case io.SeekEnd:
		abs = e.limit + offset
	default:
		return e.read, fmt.Errorf("invalid whence %d", whence)
	}
	if abs < 0 {
		return e.read, fmt.Errorf("negative position")
	}
	if abs == e.read {
		return e.read, nil
	}

	if err := e.rc.Close(); err != nil {
		logger.Debug("encrypted RAR stream close on seek", "err", err)
	}
	fsys := NewNZBFSFromMapCtx(e.ctx, e.fileMap)
	opts := []rardecode.Option{rardecode.FileSystem(fsys), rardecode.Password(e.password)}
	rc, err := rardecode.OpenReader(e.firstVolName, opts...)
	if err != nil {
		return e.read, fmt.Errorf("reopen for seek: %w", err)
	}
	e.rc = rc

	for {
		h, err := rc.Next()
		if err != nil {
			rc.Close()
			return e.read, fmt.Errorf("seek next: %w", err)
		}
		if h.Name == e.mainFileName || filepath.Base(h.Name) == e.mainBase {
			break
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(rc, h.UnPackedSize))
	}

	if abs > 0 {
		_, err = io.CopyN(io.Discard, rc, abs)
		if err != nil && err != io.EOF {
			rc.Close()
			return e.read, err
		}
	}
	e.read = abs
	return e.read, nil
}

func (e *encryptedRARStream) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.rc == nil {
		return nil
	}
	err := e.rc.Close()
	e.rc = nil
	return err
}

// maxFirstVolumesToScan caps how many "first" volumes we try when many files are
// treated as first volumes (e.g. non-standard names like .100, .101). Prevents
// failing slowly across hundreds of volumes; we try a few and fail fast.
const maxFirstVolumesToScan = 5

func ScanArchive(ctx context.Context, files []UnpackableFile, password string, target EpisodeTarget) (*ArchiveBlueprint, error) {
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	rarFiles := filterRarFiles(files)
	if len(rarFiles) == 0 {
		return nil, errors.New("no RAR files found")
	}

	firstVols := filterFirstVolumes(rarFiles)
	tryingCount := len(firstVols)
	if tryingCount > maxFirstVolumesToScan {
		sort.Slice(firstVols, func(i, j int) bool {
			return ExtractFilename(firstVols[i].Name()) < ExtractFilename(firstVols[j].Name())
		})
		firstVols = firstVols[:maxFirstVolumesToScan]
		logger.Debug("Limiting RAR first-volume scan to fail fast", "trying", len(firstVols), "skipped", tryingCount-len(firstVols))
	}
	logger.Debug("Scanning RAR first volumes", "target", target, "count", len(firstVols), "total", len(rarFiles))

	start := time.Now()
	parts, err := scanVolumesParallel(ctx, firstVols, password)
	if err != nil {
		return nil, err
	}

	for _, f := range firstVols {
		if fc, ok := f.(interface{ IsFailed() bool }); ok && fc.IsFailed() {
			logger.Error("First volume failed too many segments, aborting scan", "file", f.Name())
			return nil, fmt.Errorf("first volume unavailable: %w", ErrTooManyZeroFills)
		}
	}

	logger.Info("RAR scan complete", "files", len(rarFiles), "duration", time.Since(start))

	for _, p := range parts {
		if p.isCompressed {
			return nil, fmt.Errorf("compressed RAR archive (file: %s) -- STORE mode required for streaming", p.name)
		}
	}

	bp, err := buildBlueprint(ctx, parts, rarFiles, password, target)
	if err != nil {
		return nil, err
	}
	return bp, nil
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
}

func scanVolumesParallel(ctx context.Context, files []UnpackableFile, password string) ([]filePart, error) {
	var mu sync.Mutex
	var result []filePart
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
			listOpts := []rardecode.Option{rardecode.FileSystem(fsys), rardecode.ParallelRead(true), rardecode.SkipVolumeCheck}
			if password != "" {
				listOpts = append(listOpts, rardecode.Password(password))
			}
			infos, err := rardecode.ListArchiveInfo(cleanName, listOpts...)
			if err != nil {
				if ctxErr := contextErr(ctx); ctxErr != nil {
					setFirstErr(ctxErr)
					return
				}
				logger.Debug("Scan failure", "name", cleanName, "err", err)
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
						isEncrypted:  info.AnyEncrypted,
					})
					mu.Unlock()
				}
			}
		}(f)
	}
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	return result, nil
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
		})
		if i < 3 || i >= len(mainParts)-2 {
			logger.Trace("Blueprint part", "idx", i, "vStart", vOffset, "vEnd", vOffset+p.packedSize, "volOff", p.dataOffset, "packed", p.packedSize)
		}
		vOffset += p.packedSize
	}

	logger.Trace("Blueprint total", "vOffset", vOffset, "headerSize", headerSize, "parts", len(mainParts))

	if vOffset < headerSize {
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
		result = append(result, filePart{
			name:         name,
			unpackedSize: headerSize,
			dataOffset:   contDataOffset,
			packedSize:   dataSize,
			volFile:      f,
			volName:      f.Name(),
			isMedia:      true,
		})
		added++
	}
	logger.Trace("Manual volume aggregation", "added", added, "total", len(result))
	return result, nil
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
	listOpts := []rardecode.Option{rardecode.FileSystem(fsys), rardecode.ParallelRead(true)}
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

	lowerTarget := strings.ToLower(targetName)
	for _, info := range infos {
		if err := contextErr(ctx); err != nil {
			return continuationProbe{}, err
		}
		if strings.ToLower(info.Name) != lowerTarget {
			continue
		}
		if len(info.Parts) >= 2 {
			return continuationProbe{
				dataOffset: info.Parts[1].DataOffset,
				packedSize: info.Parts[1].PackedSize,
			}, nil
		}
	}
	return continuationProbe{}, nil
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
