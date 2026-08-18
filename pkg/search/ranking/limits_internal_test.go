package ranking

import (
	"testing"

	jhinparser "github.com/dreulavelle/jhin/parser"
)

// Size bounds judge films whole, multi-episode releases per episode, and skip
// packs whose episode count the title does not reveal.
func TestEffectiveSize(t *testing.T) {
	gb := int64(1e9)
	tests := []struct {
		name     string
		size     int64
		episodic bool
		parsed   *jhinparser.Result
		want     int64
		wantOK   bool
	}{
		{"movie uses full size", 20 * gb, false, &jhinparser.Result{}, 20 * gb, true},
		{"unknown size not judged", 0, false, &jhinparser.Result{}, 0, false},
		{"single episode uses full size", 4 * gb, true, &jhinparser.Result{Episodes: []int{1}}, 4 * gb, true},
		{"multi episode divides", 20 * gb, true, &jhinparser.Result{Episodes: []int{1, 2, 3, 4}}, 5 * gb, true},
		{"season pack unknown count not judged", 40 * gb, true, &jhinparser.Result{Seasons: []int{1}}, 0, false},
		{"complete pack not judged", 80 * gb, true, &jhinparser.Result{Complete: true}, 0, false},
		{"unparsed episodic uses full size", 4 * gb, true, nil, 4 * gb, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := effectiveSize(tt.size, tt.episodic, tt.parsed)
			if got != tt.want || ok != tt.wantOK {
				t.Errorf("effectiveSize = (%d, %v), want (%d, %v)", got, ok, tt.want, tt.wantOK)
			}
		})
	}
}
