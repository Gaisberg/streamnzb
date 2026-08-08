package unpack

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sort"

	"streamnzb/pkg/core/logger"

	"github.com/javi11/sevenzip"
)

type SevenZipBlueprint struct {
	MainFileName  string
	TotalSize     int64
	FileOffset    int64
	PackedSize    int64
	Files         []UnpackableFile
	Encrypted     bool
	AESSalt       []byte
	AESIV         []byte
	KDFIterations int
	Target        EpisodeTarget
}

func CreateSevenZipBlueprint(ctx context.Context, files []UnpackableFile, firstVolName string, password string, target EpisodeTarget) (*SevenZipBlueprint, error) {
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	archiveFiles := make([]UnpackableFile, len(files))
	copy(archiveFiles, files)
	sort.Slice(archiveFiles, func(i, j int) bool {
		return Get7zVolumeNumber(archiveFiles[i].Name()) < Get7zVolumeNumber(archiveFiles[j].Name())
	})
	parts, err := filesToParts(ctx, archiveFiles)
	if err != nil {
		return nil, err
	}
	mr := NewConcatenatedReaderAt(parts)

	// Verify 7z magic signature before passing to library
	// Signature is 7z (0x37 0x7A 0xBC 0xAF 0x27 0x1C)
	header := make([]byte, 6)
	if _, err := mr.ReadAt(header, 0); err != nil {
		return nil, fmt.Errorf("failed to read 7z header: %w", err)
	}
	if string(header[:2]) != "7z" || header[2] != 0xBC || header[3] != 0xAF || header[4] != 0x27 || header[5] != 0x1C {
		return nil, fmt.Errorf("failed to open 7z archive: invalid header (possibly missing segments or corrupted)")
	}

	var r *sevenzip.Reader
	if password != "" {
		r, err = sevenzip.NewReaderWithPassword(mr, mr.Size(), password)
	} else {
		r, err = sevenzip.NewReader(mr, mr.Size())
	}
	if err != nil {
		return nil, fmt.Errorf("failed to open 7z archive: %w", err)
	}

	fileInfos, err := r.ListFilesWithOffsets()
	if err != nil {
		return nil, fmt.Errorf("failed to list 7z files: %w", err)
	}

	bestIdx, bestSize := -1, int64(0)
	candidates := make([]namedEpisodeCandidate, 0, len(fileInfos))
	for i, fi := range fileInfos {
		if !IsVideoFile(fi.Name) || fi.Compressed || IsSampleFile(fi.Name) {
			continue
		}
		candidates = append(candidates, namedEpisodeCandidate{
			Name:  filepath.Base(fi.Name),
			Size:  int64(fi.Size),
			Index: i,
			Order: len(candidates),
		})
		if int64(fi.Size) > bestSize {
			bestIdx = i
			bestSize = int64(fi.Size)
		}
	}
	if best, ok, err := selectEpisodeCandidateOrError(candidates, target, "sevenzip_media"); err != nil {
		return nil, err
	} else if ok {
		bestIdx = best.Index
	}

	if bestIdx == -1 {
		return nil, errors.New("no uncompressed media found in 7z")
	}

	fi := fileInfos[bestIdx]
	bp := &SevenZipBlueprint{
		MainFileName:  filepath.Base(fi.Name),
		TotalSize:     int64(fi.Size),
		FileOffset:    fi.Offset,
		PackedSize:    int64(fi.PackedSize),
		Files:         archiveFiles,
		Encrypted:     fi.Encrypted,
		AESSalt:       append([]byte(nil), fi.AESSalt...),
		AESIV:         append([]byte(nil), fi.AESIV...),
		KDFIterations: fi.KDFIterations,
		Target:        target,
	}
	logger.Debug("Created 7z blueprint", "name", bp.MainFileName, "offset", bp.FileOffset, "size", bp.TotalSize, "packed", bp.PackedSize, "encrypted", bp.Encrypted)

	return bp, nil
}

func TrySevenZipNestedArchive(ctx context.Context, files []UnpackableFile, password string, target EpisodeTarget) (ReadSeekCloser, string, int64, Blueprint, error) {
	depth := NestDepthFromContext(ctx)
	if depth >= MaxNestDepth {
		return nil, "", 0, nil, fmt.Errorf("max archive nesting depth (%d) reached", MaxNestDepth)
	}

	if err := contextErr(ctx); err != nil {
		return nil, "", 0, nil, err
	}

	archiveFiles := make([]UnpackableFile, len(files))
	copy(archiveFiles, files)
	sort.Slice(archiveFiles, func(i, j int) bool {
		return Get7zVolumeNumber(archiveFiles[i].Name()) < Get7zVolumeNumber(archiveFiles[j].Name())
	})
	parts, err := filesToParts(ctx, archiveFiles)
	if err != nil {
		return nil, "", 0, nil, err
	}
	mr := NewConcatenatedReaderAt(parts)

	var r *sevenzip.Reader
	if password != "" {
		r, err = sevenzip.NewReaderWithPassword(mr, mr.Size(), password)
	} else {
		r, err = sevenzip.NewReader(mr, mr.Size())
	}
	if err != nil {
		return nil, "", 0, nil, fmt.Errorf("failed to open 7z for nested scanning: %w", err)
	}

	fileInfos, err := r.ListFilesWithOffsets()
	if err != nil {
		return nil, "", 0, nil, fmt.Errorf("failed to list 7z files: %w", err)
	}

	var nestedFiles []UnpackableFile
	for _, fi := range fileInfos {
		if fi.Compressed || !IsArchiveFile(fi.Name) {
			continue
		}
		vParts, mapErr := mapOffsetToParts(parts, fi.Offset, int64(fi.Size))
		if mapErr != nil {
			continue
		}
		nestedFiles = append(nestedFiles, NewVirtualFile(filepath.Base(fi.Name), int64(fi.Size), vParts))
	}

	if len(nestedFiles) == 0 {
		return nil, "", 0, nil, errors.New("no uncompressed nested archives found in 7z")
	}

	logger.Info("Recursively scanning 7z nested archive", "depth", depth+1, "nested_vols", len(nestedFiles))
	nextCtx := WithNestDepth(ctx, depth+1)
	return GetMediaStreamForEpisodeWithHints(nextCtx, nestedFiles, nil, password, target, StreamSelectionHints{AllowLargestDirectFallback: true})
}

func Open7zStreamFromBlueprint(ctx context.Context, bp *SevenZipBlueprint, password string) (ReadSeekCloser, string, int64, error) {
	if bp == nil || len(bp.Files) == 0 {
		return nil, "", 0, errors.New("invalid 7z blueprint or empty files")
	}

	parts, err := filesToParts(ctx, bp.Files)
	if err != nil {
		return nil, "", 0, err
	}

	if bp.Encrypted {
		packedSize := packed7zSize(bp)
		streamParts, err := mapOffsetToParts(parts, bp.FileOffset, packedSize)
		if err != nil {
			return nil, "", 0, err
		}
		encStream := NewVirtualStream(ctx, streamParts, packedSize, 0)
		decStream, err := newAES7zStream(encStream, password, bp)
		if err != nil {
			_ = encStream.Close()
			return nil, "", 0, err
		}
		return decStream, bp.MainFileName, bp.TotalSize, nil
	}

	streamParts, err := mapOffsetToParts(parts, bp.FileOffset, bp.TotalSize)
	if err != nil {
		return nil, "", 0, err
	}

	vs := NewVirtualStream(ctx, streamParts, bp.TotalSize, 0)
	return vs, bp.MainFileName, bp.TotalSize, nil
}

func filesToParts(ctx context.Context, files []UnpackableFile) ([]Part, error) {
	parts := make([]Part, len(files))
	if len(files) == 0 {
		return parts, nil
	}

	return filesToPartsFast(ctx, files)
}

func filesToPartsFast(ctx context.Context, files []UnpackableFile) ([]Part, error) {
	parts := make([]Part, len(files))

	firstSize, err := resolved7zVolumeSize(ctx, files[0])
	if err != nil {
		return nil, err
	}
	if len(files) == 1 {
		parts[0] = Part{Reader: files[0], Offset: 0, Size: firstSize}
		return parts, nil
	}

	lastSize, err := resolved7zVolumeSize(ctx, files[len(files)-1])
	if err != nil {
		return nil, err
	}

	middleSize := firstSize
	if len(files) > 2 {
		middleSize, err = resolved7zVolumeSize(ctx, files[1])
		if err != nil {
			return nil, err
		}
	}

	for i, f := range files {
		size := middleSize
		switch i {
		case 0:
			size = firstSize
		case len(files) - 1:
			size = lastSize
		}
		parts[i] = Part{Reader: f, Offset: 0, Size: size}
	}
	return parts, nil
}

func resolved7zVolumeSize(ctx context.Context, f UnpackableFile) (int64, error) {
	if err := ensureSegmentMap(ctx, f); err != nil {
		return 0, err
	}
	return f.Size(), nil
}

func mapOffsetToParts(volumes []Part, startOffset, size int64) ([]virtualPart, error) {
	var vParts []virtualPart
	remaining := size
	volOff := startOffset
	var virtualPos int64

	for _, vol := range volumes {
		if remaining <= 0 {
			break
		}
		if volOff >= vol.Size {
			volOff -= vol.Size
			continue
		}

		available := vol.Size - volOff
		take := remaining
		if take > available {
			take = available
		}

		uf, ok := vol.Reader.(UnpackableFile)
		if !ok {
			return nil, fmt.Errorf("volume reader does not implement UnpackableFile")
		}

		vParts = append(vParts, virtualPart{
			VirtualStart: virtualPos,
			VirtualEnd:   virtualPos + take,
			VolFile:      uf,
			VolOffset:    volOff,
		})

		virtualPos += take
		remaining -= take
		volOff = 0
	}

	if remaining > 0 {
		return nil, fmt.Errorf("could not map full file range (missing %d bytes)", remaining)
	}
	return vParts, nil
}

type Part struct {
	Reader io.ReaderAt
	Offset int64
	Size   int64
}

type ConcatenatedReaderAt struct {
	parts []Part
	total int64
}

func NewConcatenatedReaderAt(parts []Part) *ConcatenatedReaderAt {
	var total int64
	for _, p := range parts {
		total += p.Size
	}
	return &ConcatenatedReaderAt{parts: parts, total: total}
}

func (c *ConcatenatedReaderAt) ReadAt(p []byte, off int64) (int, error) {
	if off >= c.total {
		return 0, io.EOF
	}

	partIdx := 0
	partOff := off
	for i, part := range c.parts {
		if partOff < part.Size {
			partIdx = i
			break
		}
		partOff -= part.Size
	}

	totalRead := 0
	for partIdx < len(c.parts) && totalRead < len(p) {
		part := c.parts[partIdx]
		available := part.Size - partOff
		toRead := int64(len(p) - totalRead)
		if toRead > available {
			toRead = available
		}

		n, err := part.Reader.ReadAt(p[totalRead:totalRead+int(toRead)], part.Offset+partOff)
		totalRead += n
		if err != nil && err != io.EOF {
			return totalRead, err
		}

		partIdx++
		partOff = 0
		if totalRead == len(p) {
			break
		}
	}

	if totalRead < len(p) {
		return totalRead, io.EOF
	}
	return totalRead, nil
}

func (c *ConcatenatedReaderAt) Size() int64 { return c.total }
