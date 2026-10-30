package jellyfin

import (
	"fmt"
	"net/http"
	"testing"

	"streamnzb/pkg/server/stremio"
)

// Every Sessions/Playing report is passed on for scrobbling with the client's
// own position, as the title's addon id, with its device, client, address
// and play session.
func TestPlayingReportsArePassedOnForScrobbling(t *testing.T) {
	f := newFixture()
	series, _ := itemIDFor("series", "tt0903747")
	ep := series.episode(1, 2)
	id := ep.encode()
	report := func(event string, body string) {
		t.Helper()
		if rec := f.do(http.MethodPost, "/jellyfin/Sessions/Playing"+event, body); rec.Code != http.StatusNoContent {
			t.Fatalf("%s: %d", event, rec.Code)
		}
	}
	at := func(ticks int64, paused bool) string {
		return fmt.Sprintf(`{"ItemId":"%s","PositionTicks":%d,"IsPaused":%v,"PlaySessionId":"ps-1"}`, id, ticks, paused)
	}
	report("", at(119*ticksPerSecond, false))
	report("/Progress", at(127*ticksPerSecond, false))
	report("/Progress", at(127*ticksPerSecond, true))
	report("/Stopped", at(187*ticksPerSecond, false))
	// A stop with no position says nothing about where the player was.
	report("/Stopped", fmt.Sprintf(`{"ItemId":"%s","PlaySessionId":"ps-1"}`, id))
	// Pings carry no position and are not playback.
	report("/Ping", "")

	want := []stremio.ClientPlayback{
		{Event: stremio.ClientPlaybackStart, PositionSeconds: 119},
		{Event: stremio.ClientPlaybackProgress, PositionSeconds: 127},
		{Event: stremio.ClientPlaybackProgress, PositionSeconds: 127},
		{Event: stremio.ClientPlaybackStop, PositionSeconds: 187},
		{Event: stremio.ClientPlaybackStop, PositionSeconds: -1},
	}
	if len(f.catalog.reports) != len(want) {
		t.Fatalf("reports: %+v", f.catalog.reports)
	}
	for i, got := range f.catalog.reports {
		want[i].ContentID, want[i].Device, want[i].PlaySession = "tt0903747:1:2", "dev-1", "ps-1"
		// httptest's request address.
		want[i].Client, want[i].Address = "Swiftfin|iPhone 15, Pro", "192.0.2.1"
		if got != want[i] {
			t.Fatalf("report %d = %+v, want %+v", i, got, want[i])
		}
	}

	// A folder is not playable, so it reports nothing.
	before := len(f.catalog.reports)
	report("/Stopped", fmt.Sprintf(`{"ItemId":"%s","PositionTicks":1}`, series.encode()))
	if len(f.catalog.reports) != before {
		t.Fatalf("a series report was passed on: %+v", f.catalog.reports[before:])
	}
}

// Marking an item watched is the user's own bookkeeping, not playback, and is
// never scrobbled.
func TestMarkingPlayedIsNotScrobbled(t *testing.T) {
	f := newFixture()
	movie, _ := itemIDFor("movie", "tt0111161")
	f.do(http.MethodPost, "/jellyfin/UserPlayedItems/"+movie.encode(), "")
	f.do(http.MethodDelete, "/jellyfin/UserPlayedItems/"+movie.encode(), "")
	if len(f.catalog.reports) != 0 {
		t.Fatalf("a manual mark was scrobbled: %+v", f.catalog.reports)
	}
}

// Video served to a Jellyfin client is marked as reporting its own playback,
// which is what keeps the transfer from being scrobbled as well, with the
// device it went to — from the header, or failing that the URL — so that
// only that device's reports stand the transfer down.
func TestStreamServesAsAReportingClient(t *testing.T) {
	f := newFixture()
	movie, _ := itemIDFor("movie", "tt0111161")
	if rec := f.do(http.MethodGet, "/jellyfin/Videos/"+movie.encode()+"/stream?static=true", ""); rec.Code != http.StatusOK {
		t.Fatalf("stream: %d", rec.Code)
	}
	if rec := f.do(http.MethodGet, "/jellyfin/Videos/"+movie.encode()+"/stream?static=true&DeviceId=d9&api_key="+testToken, "", "X-Nothing", "x"); rec.Code != http.StatusOK {
		t.Fatalf("stream: %d", rec.Code)
	}
	if len(f.catalog.served) != 2 {
		t.Fatalf("served: %+v", f.catalog.served)
	}
	for i, device := range []string{"dev-1", "d9"} {
		if opts := f.catalog.served[i].opts; !opts.ClientReportsPlayback || opts.ClientDevice != device {
			t.Fatalf("serve %d: %+v, want device %q", i, opts, device)
		}
	}
}

// A report that names its device only in the URL, as a stream request may,
// is matched by it the same way: a serve and the reports of the client it went
// to must name one device, or the transfer is scrobbled alongside the client.
func TestReportDeviceFromTheURL(t *testing.T) {
	f := newFixture()
	movie, _ := itemIDFor("movie", "tt0111161")
	body := fmt.Sprintf(`{"ItemId":"%s","PositionTicks":0}`, movie.encode())
	if rec := f.do(http.MethodPost, "/jellyfin/Sessions/Playing?DeviceId=d9&api_key="+testToken, body, "X-Nothing", "x"); rec.Code != http.StatusNoContent {
		t.Fatalf("report: %d", rec.Code)
	}
	if len(f.catalog.reports) != 1 || f.catalog.reports[0].Device != "d9" {
		t.Fatalf("reports: %+v, want device d9", f.catalog.reports)
	}
}
