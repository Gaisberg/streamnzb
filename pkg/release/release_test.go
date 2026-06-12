package release

import "testing"

func TestIsFullDiscRelease(t *testing.T) {
	tests := []struct {
		title string
		want  bool
	}{
		{"Friday.Night.Lights.2004.2160p.UHD.BluRay.HDR10.HEVC.TrueHD.7.1.Atmos-UnKn0wn.mkv", false},
		{"Friday.Night.Lights.2004.COMPLETE.UHD.BLURAY-B0MBARDiERS", true},
		{"Some.Movie.2023.1080p.BD25", true},
		{"Another.Movie.2023.1080p.BD50", true},
		{"Movie.With.ISO.Extension.iso", true},
		{"Movie.With.ISO.In.Middle.iso.rar", true},
		{"Movie.With.BDMV.In.Title.BDMV", true},
		{"Isolation.2015.1080p.mkv", false},
		{"Normal.Movie.2023.1080p.BluRay.REMUX-Group", false},
	}

	for _, tt := range tests {
		got := IsFullDiscRelease(tt.title)
		if got != tt.want {
			t.Errorf("IsFullDiscRelease(%q) = %v, want %v", tt.title, got, tt.want)
		}
	}
}
