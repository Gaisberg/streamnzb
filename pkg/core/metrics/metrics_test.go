package metrics

import (
	"testing"
	"time"
)

func TestPerformanceCollector(t *testing.T) {
	c := NewCollector(5)

	for i := 1; i <= 5; i++ {
		c.RecordStreamAPI(StreamAPISample{
			Timestamp:        time.Now(),
			ContentType:      "movie",
			ID:               "tt1234567",
			TotalDuration:    time.Duration(i*100) * time.Millisecond,
			MetadataDuration: time.Duration(i*20) * time.Millisecond,
			SearchDuration:   time.Duration(i*50) * time.Millisecond,
			RankingDuration:  time.Duration(i*10) * time.Millisecond,
			AvailNZBDuration: time.Duration(i*10) * time.Millisecond,
			CandidateCount:   10,
			ResultCount:      5,
		})
	}

	samples := c.GetStreamAPISamples()
	if len(samples) != 5 {
		t.Fatalf("expected 5 samples, got %d", len(samples))
	}

	summary := c.GetStreamAPISummary()
	if summary.Total.SampleCount != 5 {
		t.Fatalf("expected sample count 5, got %d", summary.Total.SampleCount)
	}
	if summary.Total.MinMS != 100.0 {
		t.Errorf("expected MinMS 100.0, got %f", summary.Total.MinMS)
	}
	if summary.Total.MaxMS != 500.0 {
		t.Errorf("expected MaxMS 500.0, got %f", summary.Total.MaxMS)
	}
	if summary.Total.AvgMS != 300.0 {
		t.Errorf("expected AvgMS 300.0, got %f", summary.Total.AvgMS)
	}
	if summary.Total.P50MS != 300.0 {
		t.Errorf("expected P50MS 300.0, got %f", summary.Total.P50MS)
	}

	// Verify buffer ring capacity overflow
	c.RecordStreamAPI(StreamAPISample{
		TotalDuration: 600 * time.Millisecond,
	})

	samplesAfter := c.GetStreamAPISamples()
	if len(samplesAfter) != 5 {
		t.Fatalf("expected 5 samples after overflow, got %d", len(samplesAfter))
	}
	if samplesAfter[0].TotalDuration != 200*time.Millisecond {
		t.Errorf("expected oldest sample to be 200ms, got %v", samplesAfter[0].TotalDuration)
	}
}

func TestTTFFCollector(t *testing.T) {
	c := NewCollector(10)
	c.RecordPlaybackTTFF(PlaybackTTFFSample{
		Timestamp:           time.Now(),
		SessionID:           "sess1",
		ProviderName:        "ProviderA",
		TTFF:                450 * time.Millisecond,
		NZBFetchDuration:    100 * time.Millisecond,
		NNTPConnectDuration: 50 * time.Millisecond,
		ProbeDuration:       200 * time.Millisecond,
		FirstByteDuration:   100 * time.Millisecond,
	})

	summary := c.GetPlaybackTTFFSummary()
	if summary.Total.SampleCount != 1 {
		t.Fatalf("expected 1 sample, got %d", summary.Total.SampleCount)
	}
	if summary.Total.P50MS != 450.0 {
		t.Errorf("expected P50MS 450.0, got %f", summary.Total.P50MS)
	}
}
