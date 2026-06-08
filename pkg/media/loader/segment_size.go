package loader

import (
	"context"
	"errors"
	"sort"
	"sync"

	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/media/unpack"
)

const (
	segmentSizeSlop              = 256
	maxSegmentProbeConcurrency     = 16
)

func sumNZBSegmentBytes(segments []*Segment) int64 {
	var sum int64
	for _, seg := range segments {
		sum += seg.Bytes
	}
	return sum
}

func scaleDecodedSize(nzbBytes, firstEncoded, firstDecoded int64) int64 {
	if nzbBytes <= 0 {
		return firstDecoded
	}
	if firstEncoded <= 0 || firstDecoded <= 0 {
		return nzbBytes
	}
	return (nzbBytes * firstDecoded) / firstEncoded
}

func hasUniformNZBSegmentBytes(segments []*Segment) bool {
	if len(segments) <= 1 {
		return true
	}
	first := segments[0].Bytes
	for i := 1; i < len(segments)-1; i++ {
		if segments[i].Bytes != first {
			return false
		}
	}
	return true
}

func middleProbeIndex(segmentCount int) int {
	if segmentCount <= 2 {
		return 0
	}
	return segmentCount / 2
}

func shouldProbeMiddleSegment(ctx context.Context, segments []*Segment) bool {
	if len(segments) <= 2 {
		return false
	}
	if unpack.IsArchiveFastFailoverModeEnabled(ctx) {
		return false
	}
	return hasUniformNZBSegmentBytes(segments)
}

// segmentProbeIndices returns segment indices to BODY-probe in parallel.
// We probe the first occurrence of each distinct NZB bytes value (unless already
// known from the estimator), always probe the physical last segment (tail size
// often differs while NZB bytes match an earlier article), and optionally probe
// the middle segment when uniform NZB bytes need slow-mode calibration.
func segmentProbeIndices(segments []*Segment, knownByNZBBytes map[int64]int64, includeMiddle bool, skipGapProbing bool) []int {
	if len(segments) == 0 {
		return nil
	}

	lastIdx := len(segments) - 1
	seen := make(map[int]bool)
	var indices []int

	add := func(i int) {
		if i < 0 || i >= len(segments) || seen[i] {
			return
		}
		seen[i] = true
		indices = append(indices, i)
	}

	firstIndexByBytes := make(map[int64]int)
	for i, seg := range segments {
		if _, ok := firstIndexByBytes[seg.Bytes]; !ok {
			firstIndexByBytes[seg.Bytes] = i
		}
	}

	if skipGapProbing {
		var uniqueSizes []int64
		for sz := range firstIndexByBytes {
			uniqueSizes = append(uniqueSizes, sz)
		}
		sort.Slice(uniqueSizes, func(i, j int) bool { return uniqueSizes[i] < uniqueSizes[j] })

		var representatives []int64
		for _, sz := range uniqueSizes {
			matched := false
			for _, rep := range representatives {
				diff := sz - rep
				if diff < 0 {
					diff = -diff
				}
				if diff*10 < rep {
					matched = true
					break
				}
			}
			if !matched {
				representatives = append(representatives, sz)
			}
		}

		for _, repSz := range representatives {
			idx := firstIndexByBytes[repSz]
			if _, known := knownByNZBBytes[repSz]; known {
				continue
			}
			knownNearby := false
			for knownSz := range knownByNZBBytes {
				diff := repSz - knownSz
				if diff < 0 {
					diff = -diff
				}
				if diff*10 < knownSz {
					knownNearby = true
					break
				}
			}
			if knownNearby {
				continue
			}
			add(idx)
		}
	} else {
		for nbytes, idx := range firstIndexByBytes {
			if _, known := knownByNZBBytes[nbytes]; known {
				continue
			}
			add(idx)
		}
	}

	if lastIdx > 0 {
		add(lastIdx)
	}

	if includeMiddle {
		add(middleProbeIndex(len(segments)))
	}

	sort.Ints(indices)
	return indices
}

// segmentUnprobedIndices returns segment indices not covered by the initial probe plan.
// Same NZB bytes can decode to different lengths; a second pass probes these gaps only.
func segmentUnprobedIndices(segmentCount int, probedIndices []int) []int {
	if segmentCount <= 0 {
		return nil
	}
	seen := make(map[int]bool, len(probedIndices))
	for _, i := range probedIndices {
		seen[i] = true
	}
	var missing []int
	for i := 0; i < segmentCount; i++ {
		if !seen[i] {
			missing = append(missing, i)
		}
	}
	return missing
}

func buildSegmentDecodedSizesFromProbes(segments []*Segment, probedByIndex map[int]int64, knownByNZBBytes map[int64]int64, skipGapProbing bool) []int64 {
	n := len(segments)
	sizes := make([]int64, n)
	if n == 0 {
		return sizes
	}

	firstEncoded := segments[0].Bytes
	if firstEncoded <= 0 {
		firstEncoded = 1
	}

	decodedByBytes := make(map[int64]int64)
	for sz, dec := range knownByNZBBytes {
		decodedByBytes[sz] = dec
	}
	for idx, dec := range probedByIndex {
		if idx < 0 || idx >= n || dec <= 0 {
			continue
		}
		decodedByBytes[segments[idx].Bytes] = dec
	}

	firstDecoded := probedByIndex[0]
	if firstDecoded <= 0 {
		firstDecoded = decodedByBytes[firstEncoded]
	}
	if firstDecoded <= 0 {
		for _, dec := range probedByIndex {
			if dec > 0 {
				firstDecoded = dec
				break
			}
		}
	}

	findNearbyProbedDecoded := func(nzbBytes int64) (int64, bool) {
		for probedNzb, dec := range decodedByBytes {
			diff := nzbBytes - probedNzb
			if diff < 0 {
				diff = -diff
			}
			if diff*10 < probedNzb {
				return dec, true
			}
		}
		return 0, false
	}

	for i := 0; i < n; i++ {
		if dec, ok := probedByIndex[i]; ok && dec > 0 {
			sizes[i] = dec
			continue
		}
		if dec, ok := decodedByBytes[segments[i].Bytes]; ok {
			sizes[i] = dec
			continue
		}
		if skipGapProbing {
			if dec, ok := findNearbyProbedDecoded(segments[i].Bytes); ok {
				sizes[i] = dec
				continue
			}
		}
		sizes[i] = scaleDecodedSize(segments[i].Bytes, firstEncoded, firstDecoded)
	}
	return sizes
}

func applyUniformMiddleCalibration(segments []*Segment, sizes []int64, middleIdx int, middleDecoded int64) {
	n := len(segments)
	if n <= 2 || middleIdx <= 0 || middleIdx >= n-1 {
		return
	}
	if !hasUniformNZBSegmentBytes(segments) {
		return
	}

	firstEncoded := segments[0].Bytes
	if firstEncoded <= 0 {
		return
	}
	firstDecoded := sizes[0]
	scaledMid := scaleDecodedSize(segments[middleIdx].Bytes, firstEncoded, firstDecoded)
	diff := middleDecoded - scaledMid
	if diff < 0 {
		diff = -diff
	}
	if diff <= segmentSizeSlop {
		sizes[middleIdx] = middleDecoded
		return
	}

	ratio1 := float64(firstDecoded) / float64(firstEncoded)
	ratio2 := float64(middleDecoded) / float64(segments[middleIdx].Bytes)
	if segments[middleIdx].Bytes <= 0 {
		ratio2 = ratio1
	}
	for i := 0; i < middleIdx; i++ {
		sizes[i] = int64(float64(segments[i].Bytes) * ratio1)
	}
	sizes[middleIdx] = middleDecoded
	for i := middleIdx + 1; i < n-1; i++ {
		sizes[i] = int64(float64(segments[i].Bytes) * ratio2)
	}
}

func applySegmentDecodedSizes(segments []*Segment, sizes []int64) int64 {
	var offset int64
	for i := range segments {
		size := sizes[i]
		if size < 0 {
			size = 0
		}
		segments[i].StartOffset = offset
		segments[i].EndOffset = offset + size
		offset += size
	}
	return offset
}

func (f *File) probeSegmentIndicesParallel(ctx context.Context, indices []int) (map[int]int64, error) {
	probed := make(map[int]int64, len(indices))
	if len(indices) == 0 {
		return probed, nil
	}

	var (
		mu       sync.Mutex
		wg       sync.WaitGroup
		firstErr error
		sem      = make(chan struct{}, maxSegmentProbeConcurrency)
	)

	for _, idx := range indices {
		idx := idx
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			if err := ctx.Err(); err != nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				mu.Unlock()
				return
			}

			data, err := f.DownloadSegment(ctx, idx)
			if err != nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				mu.Unlock()
				return
			}
			if len(data) == 0 {
				mu.Lock()
				if firstErr == nil {
					firstErr = errors.New("empty probe segment")
				}
				mu.Unlock()
				return
			}

			mu.Lock()
			probed[idx] = int64(len(data))
			mu.Unlock()
		}()
	}
	wg.Wait()
	return probed, firstErr
}

func logSegmentProbePlan(ctx context.Context, name string, segments []*Segment, indices []int, knownByNZBBytes map[int64]int64, includeMiddle bool) {
	unique := len(firstIndexByBytesMap(segments))
	logger.Debug("Segment map probe plan",
		"name", name,
		"segments", len(segments),
		"unique_nzb_bytes", unique,
		"probe_indices", indices,
		"known_from_estimator", len(knownByNZBBytes),
		"uniform_nzb_bytes", hasUniformNZBSegmentBytes(segments),
		"middle_calibration", includeMiddle,
		"failover_fast_mode", unpack.IsArchiveFastFailoverModeEnabled(ctx))
}

func firstIndexByBytesMap(segments []*Segment) map[int64]int {
	m := make(map[int64]int)
	for i, seg := range segments {
		if _, ok := m[seg.Bytes]; !ok {
			m[seg.Bytes] = i
		}
	}
	return m
}
