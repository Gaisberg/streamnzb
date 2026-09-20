package unpack

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"

	"streamnzb/pkg/core/logger"
)

type virtualPart struct {
	VirtualStart int64
	VirtualEnd   int64
	VolFile      UnpackableFile
	VolOffset    int64
	AesKey       []byte
	AesIV        []byte
}

type VirtualStream struct {
	parts     []virtualPart
	totalSize int64
	ctx       context.Context

	mu            sync.Mutex
	offset        int64
	currentReader io.ReadCloser
	currentPart   int
	closed        bool
	// readerOffset is the virtual offset currentReader is positioned at, which
	// is only the same as offset until a Seek moves the read pointer without
	// touching the reader. ensureReader closes the gap on the next Read.
	readerOffset int64
	// prefetchedPart is the last part index handed to prefetchPart, so the
	// approach to a boundary warms the next volume exactly once instead of on
	// every Read inside the margin.
	prefetchedPart int
}

var liveVirtualStreams atomic.Int64
var errOffsetChanged = errors.New("virtual stream offset changed")

func LiveVirtualStreams() int64 {
	return liveVirtualStreams.Load()
}

func NewVirtualStream(ctx context.Context, parts []virtualPart, totalSize int64, startOffset int64) *VirtualStream {
	for i := range parts {
		if sizer, ok := parts[i].VolFile.(playbackStreamSizer); ok {
			sizer.SetPlaybackStreamBytes(totalSize)
		}
	}
	liveVirtualStreams.Add(1)
	return &VirtualStream{
		parts:          parts,
		totalSize:      totalSize,
		ctx:            ctx,
		offset:         startOffset,
		currentPart:    -1,
		prefetchedPart: -1,
	}
}

// virtualPartPrefetchMargin is how close to a part's end the read pointer gets
// before the next volume is warmed. Warming only at the crossing itself meant
// the switch always began with a cold segment map — a blocking probe at the
// exact moment the player needed the next byte. A few seconds of playback of
// margin hides that entirely.
const virtualPartPrefetchMargin = 16 << 20

func (s *VirtualStream) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}

	for {
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			return 0, io.ErrClosedPipe
		}

		if s.offset >= s.totalSize {
			s.mu.Unlock()
			return 0, io.EOF
		}

		select {
		case <-s.ctx.Done():
			s.mu.Unlock()
			return 0, s.ctx.Err()
		default:
		}

		part, partIdx := s.findPart(s.offset)
		if part == nil {
			s.mu.Unlock()
			return 0, fmt.Errorf("offset %d not mapped in %d parts", s.offset, len(s.parts))
		}

		if err := s.ensureReader(part, partIdx); err != nil {
			if errors.Is(err, errOffsetChanged) {
				s.mu.Unlock()
				continue
			}
			s.mu.Unlock()
			return 0, err
		}

		remaining := part.VirtualEnd - s.offset
		buf := p
		if int64(len(buf)) > remaining {
			buf = buf[:remaining]
		}

		expectedOffset := s.offset
		reader := s.currentReader
		s.mu.Unlock()

		n, err := reader.Read(buf)

		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			return n, io.ErrClosedPipe
		}

		if s.offset != expectedOffset || s.currentReader != reader {
			s.mu.Unlock()
			return 0, fmt.Errorf("virtual stream: concurrent seek or read detected")
		}

		s.offset += int64(n)
		s.readerOffset = s.offset

		if err == io.EOF {
			if s.currentReader == reader {
				s.closeReader()
			}

			if n == 0 && s.offset < part.VirtualEnd {
				// Exhausted this volume before the packed span ends (trailing RAR bytes).
				// Advance to the next virtual part instead of stalling on a dead reader.
				s.offset = part.VirtualEnd
			}
			if n > 0 {
				if s.offset >= part.VirtualEnd && s.offset < s.totalSize {
					s.prefetchPart(partIdx + 1)
				}
				s.mu.Unlock()
				return n, nil
			}
			if s.offset < s.totalSize {
				s.mu.Unlock()
				continue
			}
			s.mu.Unlock()
			return 0, io.EOF
		}

		if s.offset >= part.VirtualEnd && s.offset < s.totalSize {
			s.prefetchPart(partIdx + 1)
		} else if part.VirtualEnd-s.offset <= virtualPartPrefetchMargin {
			// Approaching the boundary: warm the next volume before the switch.
			s.prefetchPart(partIdx + 1)
		}

		s.mu.Unlock()
		return n, err
	}
}

func (s *VirtualStream) Seek(offset int64, whence int) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return 0, io.ErrClosedPipe
	}

	var target int64
	switch whence {
	case io.SeekStart:
		target = offset
	case io.SeekCurrent:
		target = s.offset + offset
	case io.SeekEnd:
		target = s.totalSize + offset
	default:
		return 0, errors.New("invalid whence")
	}

	if target < 0 || target > s.totalSize {
		return 0, errors.New("seek out of bounds")
	}

	logger.Debug("VirtualStream Seek", "offset", offset, "whence", whence, "target", target, "currentOffset", s.offset)

	// Moving the read pointer is all a seek does. The reader is left exactly as
	// it is, and ensureReader repositions or replaces it on the next Read.
	//
	// Tearing it down here instead threw away a warm-up nobody had used yet:
	// every range request arrives as prime the first byte, then ServeContent
	// seeks to EOF, back to 0, and finally to the range start, so a reader
	// opened for the prime read was closed and re-opened at the same offset
	// with its in-flight read-ahead cancelled in between. On a multi-volume RAR
	// that is the whole cost of a reconnect, paid twice over, and SegmentReader
	// already declines to cancel on a seek for the same reason.
	s.offset = target
	return target, nil
}

func (s *VirtualStream) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	logger.Debug("VirtualStream Close", "offset", s.offset)
	if s.closed {
		return nil
	}
	s.closed = true
	s.closeReader()
	liveVirtualStreams.Add(-1)
	return nil
}

func (s *VirtualStream) findPart(offset int64) (*virtualPart, int) {
	left, right := 0, len(s.parts)-1
	for left <= right {
		mid := (left + right) / 2
		p := &s.parts[mid]
		if offset >= p.VirtualStart && offset < p.VirtualEnd {
			return p, mid
		}
		if offset < p.VirtualStart {
			right = mid - 1
		} else {
			left = mid + 1
		}
	}
	return nil, -1
}

func (s *VirtualStream) ensureReader(part *virtualPart, partIdx int) error {
	if s.currentReader != nil && s.currentPart == partIdx {
		if s.readerOffset == s.offset {
			return nil
		}
		// A seek moved the pointer inside the part this reader already covers.
		// Repositioning beats re-opening: a SegmentReader that lands back in
		// its current segment keeps the decoded bytes and the read-ahead window
		// it has in flight, which a fresh reader would have to fetch again.
		if seeker, ok := s.currentReader.(io.Seeker); ok {
			volOff := part.VolOffset + (s.offset - part.VirtualStart)
			if _, err := seeker.Seek(volOff, io.SeekStart); err == nil {
				s.readerOffset = s.offset
				return nil
			}
		}
	}

	logger.Debug("VirtualStream ensureReader: entering", "partIdx", partIdx, "volName", part.VolFile.Name(), "offset", s.offset)
	s.closeReader()

	localOff := s.offset - part.VirtualStart
	volOff := part.VolOffset + localOff
	expectedOffset := s.offset

	s.mu.Unlock()
	r, openErr := openPlaybackReaderAt(part.VolFile, playbackSegmentMapCtx(s.ctx), volOff)
	s.mu.Lock()

	logger.Debug("VirtualStream ensureReader: openPlaybackReaderAt completed", "partIdx", partIdx, "err", openErr)

	if s.closed {
		if r != nil {
			r.Close()
		}
		return io.ErrClosedPipe
	}

	if s.offset != expectedOffset {
		if r != nil {
			r.Close()
		}
		return errOffsetChanged
	}

	if openErr != nil {
		return fmt.Errorf("open volume part %d at offset %d: %w", partIdx, volOff, openErr)
	}

	s.currentReader = r
	s.currentPart = partIdx
	s.readerOffset = s.offset
	return nil
}

// prefetchPart warms part partIdx on its own goroutine. It must not block:
// it is called with s.mu held from inside Read, and warming a volume can mean
// probing its segment map over the network.
func (s *VirtualStream) prefetchPart(partIdx int) {
	if partIdx < 0 || partIdx >= len(s.parts) || partIdx == s.prefetchedPart {
		return
	}
	s.prefetchedPart = partIdx
	part := &s.parts[partIdx]
	prefetcher, ok := part.VolFile.(playbackPrefetcher)
	if !ok {
		return
	}
	ctx := playbackSegmentMapCtx(s.ctx)
	volOffset := part.VolOffset
	go prefetcher.PrefetchPlaybackOffset(ctx, volOffset)
}

func (s *VirtualStream) closeReader() {
	if s.currentReader != nil {
		s.currentReader.Close()
		s.currentReader = nil
		s.currentPart = -1
	}
}

type EncryptedVirtualStream struct {
	source     *VirtualStream
	totalSize  int64 // Unpacked size
	packedSize int64 // Ciphertext size
	aesKey     []byte
	aesIV      []byte
	offset     int64
	mu         sync.Mutex
	closed     bool

	// block is built once; a fresh aes.NewCipher per Read spent key-schedule
	// CPU on every 32 KB the player pulled.
	block    cipher.Block
	blockErr error

	// nextIV is the last ciphertext block of the previous read: the CBC IV for
	// a read starting at nextIVEnd. Sequential playback always does, so the
	// steady state never seeks backwards for an IV — that backward seek reset
	// the underlying SegmentReader's read-ahead on every single Read, which
	// held encrypted releases to roughly a serial fetch.
	nextIV     [aesBlockSize]byte
	haveNextIV bool
	nextIVEnd  int64
}

const aesBlockSize = 16

func NewEncryptedVirtualStream(
	ctx context.Context,
	parts []virtualPart,
	totalSize int64,
	packedSize int64,
	aesKey []byte,
	aesIV []byte,
	startOffset int64,
) *EncryptedVirtualStream {
	source := NewVirtualStream(ctx, parts, packedSize, 0)
	s := &EncryptedVirtualStream{
		source:     source,
		totalSize:  totalSize,
		packedSize: packedSize,
		aesKey:     aesKey,
		aesIV:      aesIV,
		offset:     startOffset,
	}
	s.block, s.blockErr = aes.NewCipher(aesKey)
	return s
}

func (s *EncryptedVirtualStream) Read(p []byte) (int, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return 0, io.ErrClosedPipe
	}
	if s.offset >= s.totalSize {
		s.mu.Unlock()
		return 0, io.EOF
	}

	n := len(p)
	if n == 0 {
		s.mu.Unlock()
		return 0, nil
	}

	// Clamp the requested size to the remaining unpacked bytes
	if s.offset+int64(n) > s.totalSize {
		n = int(s.totalSize - s.offset)
	}
	if n <= 0 {
		s.mu.Unlock()
		return 0, io.EOF
	}

	alignedStart := s.offset - (s.offset % aesBlockSize)
	alignedEnd := s.offset + int64(n)
	// Align alignedEnd to the block size
	if alignedEnd%aesBlockSize != 0 {
		alignedEnd = (alignedEnd/aesBlockSize + 1) * aesBlockSize
	}
	if alignedEnd > s.packedSize {
		alignedEnd = s.packedSize
	}

	block, blockErr := s.block, s.blockErr
	var iv []byte
	switch {
	case alignedStart == 0:
		iv = append(iv, s.aesIV...)
	case s.haveNextIV && s.nextIVEnd == alignedStart:
		iv = append(iv, s.nextIV[:]...)
	}

	expectedOffset := s.offset
	s.mu.Unlock()

	if blockErr != nil {
		return 0, fmt.Errorf("new aes cipher: %w", blockErr)
	}

	// 1. Obtain the IV (no lock held) unless the previous read already left it
	// behind: the IV for offset X is simply the ciphertext block before X.
	if iv == nil {
		iv = make([]byte, aesBlockSize)
		if _, err := s.source.Seek(alignedStart-aesBlockSize, io.SeekStart); err != nil {
			return 0, fmt.Errorf("seek to IV offset %d: %w", alignedStart-aesBlockSize, err)
		}
		if _, err := io.ReadFull(s.source, iv); err != nil {
			return 0, fmt.Errorf("read IV at offset %d: %w", alignedStart-aesBlockSize, err)
		}
	}

	// 2. Read the ciphertext (no lock held)
	cipherLen := alignedEnd - alignedStart
	if cipherLen <= 0 {
		return 0, io.EOF
	}
	ciphertext := make([]byte, cipherLen)
	if _, err := s.source.Seek(alignedStart, io.SeekStart); err != nil {
		return 0, fmt.Errorf("seek to ciphertext offset %d: %w", alignedStart, err)
	}
	if _, err := io.ReadFull(s.source, ciphertext); err != nil {
		return 0, fmt.Errorf("read ciphertext at offset %d: %w", alignedStart, err)
	}

	// 3. Decrypt ciphertext (no lock held), saving the final ciphertext block
	// first — CryptBlocks decrypts in place, and that block is the next read's IV.
	decryptLen := (cipherLen / aesBlockSize) * aesBlockSize
	var lastCipherBlock [aesBlockSize]byte
	if decryptLen > 0 {
		copy(lastCipherBlock[:], ciphertext[decryptLen-aesBlockSize:decryptLen])
		mode := cipher.NewCBCDecrypter(block, iv)
		mode.CryptBlocks(ciphertext[:decryptLen], ciphertext[:decryptLen])
	}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return 0, io.ErrClosedPipe
	}
	if s.offset != expectedOffset {
		s.mu.Unlock()
		return 0, fmt.Errorf("encrypted virtual stream: concurrent seek or read detected")
	}

	if decryptLen > 0 {
		s.nextIV = lastCipherBlock
		s.nextIVEnd = alignedStart + decryptLen
		s.haveNextIV = true
	}

	// 4. Copy to user buffer
	startInBuf := s.offset - alignedStart
	copied := copy(p[:n], ciphertext[startInBuf:])
	s.offset += int64(copied)

	s.mu.Unlock()
	return copied, nil
}

func (s *EncryptedVirtualStream) Seek(offset int64, whence int) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return 0, io.ErrClosedPipe
	}

	var target int64
	switch whence {
	case io.SeekStart:
		target = offset
	case io.SeekCurrent:
		target = s.offset + offset
	case io.SeekEnd:
		target = s.totalSize + offset
	default:
		return 0, errors.New("invalid whence")
	}

	if target < 0 || target > s.totalSize {
		return 0, errors.New("seek out of bounds")
	}

	s.offset = target
	return target, nil
}

func (s *EncryptedVirtualStream) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return nil
	}
	s.closed = true
	return s.source.Close()
}
