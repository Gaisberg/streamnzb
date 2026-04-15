package unpack

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"

	"streamnzb/pkg/core/logger"

	"github.com/javi11/sevenzip"
)

type SevenZipBlueprint struct {
	MainFileName string
	TotalSize    int64
	FileOffset   int64
	Files        []UnpackableFile
	Encrypted    bool
	Target       EpisodeTarget
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
		MainFileName: filepath.Base(fi.Name),
		TotalSize:    int64(fi.Size),
		FileOffset:   fi.Offset,
		Files:        archiveFiles,
		Encrypted:    fi.Encrypted,
		Target:       target,
	}
	logger.Debug("Created 7z blueprint", "name", bp.MainFileName, "offset", bp.FileOffset, "size", bp.TotalSize, "encrypted", bp.Encrypted)

	return bp, nil
}

var ErrEncrypted7zStreaming = errors.New("encrypted 7z cannot be streamed in reasonable time; try another release")

func Open7zStreamFromBlueprint(ctx context.Context, bp *SevenZipBlueprint, password string) (ReadSeekCloser, string, int64, error) {
	if bp == nil || len(bp.Files) == 0 {
		return nil, "", 0, errors.New("invalid 7z blueprint or empty files")
	}

	if bp.Encrypted {
		return nil, "", 0, ErrEncrypted7zStreaming
	}

	parts, err := filesToParts(ctx, bp.Files)
	if err != nil {
		return nil, "", 0, err
	}
	streamParts, err := mapOffsetToParts(parts, bp.FileOffset, bp.TotalSize)
	if err != nil {
		return nil, "", 0, err
	}

	vs := NewVirtualStream(ctx, streamParts, bp.TotalSize, 0)
	return vs, bp.MainFileName, bp.TotalSize, nil
}

func filter7zFiles(files []UnpackableFile) []UnpackableFile {
	var result []UnpackableFile
	for _, f := range files {
		lower := strings.ToLower(ExtractFilename(f.Name()))
		if strings.HasSuffix(lower, ".7z") || strings.Contains(lower, ".7z.") {
			result = append(result, f)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		return Get7zVolumeNumber(result[i].Name()) < Get7zVolumeNumber(result[j].Name())
	})
	return result
}

func filesToParts(ctx context.Context, files []UnpackableFile) ([]Part, error) {
	parts := make([]Part, len(files))
	if len(files) == 0 {
		return parts, nil
	}

	firstSize, err := resolved7zVolumeSize(ctx, files[0])
	if err != nil {
		return nil, err
	}
	lastSize := firstSize
	if len(files) > 1 {
		lastSize, err = resolved7zVolumeSize(ctx, files[len(files)-1])
		if err != nil {
			return nil, err
		}
	}

	for i, f := range files {
		size := firstSize
		if i == len(files)-1 {
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
