package fileutil

import "testing"

func TestExtractFilenameFallsBackWhenQuotedNameEmpty(t *testing.T) {
	subject := `[N3wZ] \Y3Ve8Q363933\::[PRiVATE]-[WtFnZb]-[Send.Help.2026.UHD.BluRay.2160p.TrueHD.Atmos.7.1.DV.HDR10P.HEVC.HYBRID.REMUX-FraMeSToR.mkv]-[1/1] - "" yEnc  53346083266 (1/74423)`
	got := ExtractFilename(subject)
	want := "Send.Help.2026.UHD.BluRay.2160p.TrueHD.Atmos.7.1.DV.HDR10P.HEVC.HYBRID.REMUX-FraMeSToR.mkv"
	if got != want {
		t.Fatalf("ExtractFilename() = %q, want %q", got, want)
	}
}

func TestExtractFilenamePrefersNonEmptyQuotedName(t *testing.T) {
	subject := `foo "Real.Name.2026.mkv" yEnc`
	got := ExtractFilename(subject)
	want := "Real.Name.2026.mkv"
	if got != want {
		t.Fatalf("ExtractFilename() = %q, want %q", got, want)
	}
}
