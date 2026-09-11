package jellyfin

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"streamnzb/pkg/auth"
	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/release"
	"streamnzb/pkg/search/parser"
	"streamnzb/pkg/server/stremio"
)

// Playback is the Stremio flow with Jellyfin names. PlaybackInfo is the
// stream request: it searches, ranks and returns every candidate as a media
// source, best first. The stream URL is the addon's /play/ slot, and a
// candidate that fails moves to the next one the way it does for Stremio —
// by redirect, so the client re-issues its Range request against the new
// slot instead of receiving the tail of a different file.

// mediaSource is Jellyfin's MediaSourceInfo. Each source is an HTTP resource
// served by this addon's /Videos/{id}/stream endpoint, not a local Jellyfin
// file. Advertising it as remote HTTP is important: Apple and Android clients
// otherwise may choose local-file/direct-stream code paths even though the
// only supported operation here is a ranged direct-play request.
type mediaSource struct {
	Protocol              string `json:"Protocol"`
	ID                    string `json:"Id"`
	Path                  string `json:"Path"`
	Type                  string `json:"Type"`
	Container             string `json:"Container,omitempty"`
	Size                  *int64 `json:"Size,omitempty"`
	Name                  string `json:"Name"`
	IsRemote              bool   `json:"IsRemote"`
	ETag                  string `json:"ETag,omitempty"`
	RunTimeTicks          *int64 `json:"RunTimeTicks,omitempty"`
	ReadAtNativeFramerate bool   `json:"ReadAtNativeFramerate"`
	IgnoreDts             bool   `json:"IgnoreDts"`
	IgnoreIndex           bool   `json:"IgnoreIndex"`
	GenPtsInput           bool   `json:"GenPtsInput"`
	SupportsTranscoding   bool   `json:"SupportsTranscoding"`
	SupportsDirectStream  bool   `json:"SupportsDirectStream"`
	SupportsDirectPlay    bool   `json:"SupportsDirectPlay"`
	IsInfiniteStream      bool   `json:"IsInfiniteStream"`
	RequiresOpening       bool   `json:"RequiresOpening"`
	RequiresClosing       bool   `json:"RequiresClosing"`
	RequiresLooping       bool   `json:"RequiresLooping"`
	SupportsProbing       bool   `json:"SupportsProbing"`
	// TranscodingSubProtocol is "http" — the plain stream URL. Nothing is
	// transcoded here, but the field is required and has no empty form.
	TranscodingSubProtocol string `json:"TranscodingSubProtocol"`
	// HasSegments is false: the source is one file, not a segmented stream.
	HasSegments             bool              `json:"HasSegments"`
	VideoType               string            `json:"VideoType"`
	MediaStreams            []mediaStream     `json:"MediaStreams"`
	MediaAttachments        []any             `json:"MediaAttachments"`
	Formats                 []string          `json:"Formats"`
	Bitrate                 *int              `json:"Bitrate,omitempty"`
	RequiredHTTPHeaders     map[string]string `json:"RequiredHttpHeaders"`
	DefaultAudioStreamIndex *int              `json:"DefaultAudioStreamIndex,omitempty"`
}

type mediaStream struct {
	Codec                  string `json:"Codec,omitempty"`
	Language               string `json:"Language,omitempty"`
	DisplayTitle           string `json:"DisplayTitle,omitempty"`
	VideoRange             string `json:"VideoRange,omitempty"`
	VideoRangeType         string `json:"VideoRangeType,omitempty"`
	IsInterlaced           bool   `json:"IsInterlaced"`
	BitDepth               *int   `json:"BitDepth,omitempty"`
	Height                 *int   `json:"Height,omitempty"`
	Width                  *int   `json:"Width,omitempty"`
	Type                   string `json:"Type"`
	Index                  int    `json:"Index"`
	IsDefault              bool   `json:"IsDefault"`
	IsForced               bool   `json:"IsForced"`
	IsHearingImpaired      bool   `json:"IsHearingImpaired"`
	IsOriginal             bool   `json:"IsOriginal"`
	IsExternal             bool   `json:"IsExternal"`
	IsTextSubtitleStream   bool   `json:"IsTextSubtitleStream"`
	SupportsExternalStream bool   `json:"SupportsExternalStream"`
	Profile                string `json:"Profile,omitempty"`
	Level                  *int   `json:"Level,omitempty"`
}

type playbackInfoResponse struct {
	MediaSources  []mediaSource `json:"MediaSources"`
	PlaySessionID string        `json:"PlaySessionId"`
	ErrorCode     string        `json:"ErrorCode,omitempty"`
}

func (s *Server) servePlayback(w http.ResponseWriter, rq *request) bool {
	segs := rq.segs
	if len(segs) >= 2 && segs[0] == "users" {
		segs = segs[2:]
	}
	switch {
	case len(segs) == 3 && segs[0] == "items" && segs[2] == "playbackinfo" && (rq.is(http.MethodGet) || rq.is(http.MethodPost)):
		s.handlePlaybackInfo(w, rq, segs[1])
	case len(segs) == 3 && segs[0] == "videos" && (rq.is(http.MethodGet) || rq.is(http.MethodHead)):
		// /Videos/{id}/stream and /Videos/{id}/stream.mkv; the HLS forms are
		// transcodes, which do not exist here.
		if name, _, _ := strings.Cut(segs[2], "."); name != "stream" {
			return false
		}
		s.handleStream(w, rq, segs[1])
	case len(segs) == 3 && segs[0] == "items" && segs[2] == "refresh" && rq.is(http.MethodPost):
		writeEmpty(w)
	case len(segs) >= 2 && segs[0] == "sessions" && segs[1] == "playing" && rq.is(http.MethodPost):
		s.handlePlaying(w, rq, strings.Join(segs[2:], "/"), "")
	case len(segs) == 2 && segs[0] == "playeditems":
		// /Users/{id}/PlayedItems/{itemId}
		s.handlePlayed(w, rq, segs[1])
	case len(segs) == 2 && segs[0] == "userplayeditems":
		s.handlePlayed(w, rq, segs[1])
	case len(segs) == 2 && (segs[0] == "favoriteitems" || segs[0] == "userfavoriteitems"):
		// Favourites have nowhere to live; the client gets its item's
		// state back unchanged.
		s.writeUserData(w, rq, segs[1])
	case len(segs) == 3 && segs[0] == "useritems" && segs[2] == "userdata":
		if rq.is(http.MethodPost) {
			s.handleUserDataUpdate(w, rq, segs[1])
			return true
		}
		s.writeUserData(w, rq, segs[1])
	case len(segs) == 3 && segs[0] == "playingitems" && rq.is(http.MethodPost):
		// The legacy /Users/{id}/PlayingItems/{itemId}[/Progress] forms carry
		// the report in the query.
		s.handlePlaying(w, rq, segs[2], segs[1])
	case len(segs) == 2 && segs[0] == "playingitems":
		if rq.is(http.MethodDelete) {
			s.handlePlaying(w, rq, "stopped", segs[1])
		} else {
			s.handlePlaying(w, rq, "", segs[1])
		}
	default:
		return false
	}
	return true
}

// playableID resolves an item or media-source id to the movie or episode
// it plays and the slot index it names (0 for an item id).
func playableID(raw string) (itemID, int, bool) {
	id, err := decodeItemID(raw)
	if err != nil {
		return itemID{}, 0, false
	}
	switch id.Kind {
	case kindMovie, kindEpisode:
		return id, 0, true
	case kindSource:
		return id.playable(), id.Slot, true
	}
	return itemID{}, 0, false
}

// mediaSourceIDFor names a slot. It is 32 hex characters and nothing else,
// because clients read it as a GUID: Wholphin parses it with the SDK's
// toUUID(), which throws on anything that is not one, and takes the app down
// with it. The Jellyfin API types the field as a plain string, so that is
// arguably their bug — but a media source id has no reason not to be a GUID,
// and being one costs nothing.
//
// What it deliberately does not carry is the stream's token; see
// streamURLFor for where that went and why it had to go somewhere.
func mediaSourceIDFor(id itemID, index int) string {
	return id.source(index).encode()
}

// streamURLFor builds the absolute, authenticated URL for a slot.
//
// The player that fetches the video is not the client that signed in, and it
// is handed a URL rather than a session: Findroid gives ExoPlayer a bare URL
// built by the SDK, and neither SDK attaches credentials to it — Kotlin's
// createUrl is baseUrl + path + query, and Swift's url(with:) only appends
// api_key when asked, which Swiftfin does not. So the token has to be
// somewhere in what the client is given, and the media source id used to be
// where it was.
//
// Now it rides on two fields instead, because the clients disagree about
// which one reaches the player:
//
//   - Path, with Protocol "Http" and IsRemote — Findroid plays
//     MediaSourceInfo.path verbatim for an HTTP protocol source, and Wholphin
//     does the same whenever IsRemote is set and the path is non-empty.
//   - ETag — Swiftfin builds its own URL and passes mediaSource.eTag through
//     as the tag parameter (falling back to the *item's* etag when it is
//     empty, which would not authenticate anything), and Wholphin passes it
//     the same way on the path where it does not use Path.
//
// Both are honoured only on the stream route, so a token read off either one
// opens one video and cannot be spent on browsing.
func (s *Server) streamURLFor(id itemID, index int, stream *auth.Stream) string {
	base := s.baseURL()
	if base == "" {
		return ""
	}
	q := url.Values{}
	q.Set("mediaSourceId", mediaSourceIDFor(id, index))
	q.Set("static", "true")
	if token := streamTokenOf(stream); token != "" {
		q.Set("api_key", token)
	}
	return base + Mount + "videos/" + id.encode() + "/stream?" + q.Encode()
}

func streamTokenOf(stream *auth.Stream) string {
	if stream == nil {
		return ""
	}
	return stream.Token
}

// sourceIndexFrom resolves a media source id a client handed back to the slot
// it names on this item. An id naming a different item is reported as absent
// rather than honoured, so a pick left over from another title cannot steer
// playback here.
func sourceIndexFrom(src string, id itemID) (int, bool) {
	if src == "" {
		return 0, false
	}
	if sid, sidx, ok := playableID(src); ok && sid.encode() == id.encode() {
		return sidx, true
	}
	return 0, false
}

// mediaSourceIDFromBody reads the pick out of a posted PlaybackInfoDto. The
// Jellyfin SDK sends MediaSourceId in the body on this route rather than the
// query, so reading the query alone would miss every client that uses it.
// Nothing else in the DTO is read: device profiles and bitrate caps describe
// transcoding decisions this layer does not make.
func mediaSourceIDFromBody(rq *request) string {
	if rq.Body == nil || !rq.is(http.MethodPost) {
		return ""
	}
	var body struct {
		MediaSourceID string `json:"MediaSourceId"`
	}
	// Device profiles are large; the limit only guards against a body that
	// never ends, since a truncated decode simply reports no pick.
	if err := json.NewDecoder(io.LimitReader(rq.Body, 1<<20)).Decode(&body); err != nil {
		return ""
	}
	return body.MediaSourceID
}

// promoteSource moves the entry a client asked for to the front of the ranked
// list, keeping the rest in rank order.
//
// Clients disagree about what a media source list means after a pick. Some
// re-request PlaybackInfo naming the source they chose and then play
// mediaSources[0] without rereading the ids (Wholphin does), so a pick that
// survives only as an id is silently ignored and the best-ranked release plays
// instead. Stock Jellyfin answers that by returning the picked source alone,
// but here the list is also the picker for clients that build their version
// menu from it, and returning one entry would collapse that menu to a single
// choice. Reordering satisfies both readings.
func promoteSource(entries []stremio.PlaylistEntry, index int) []stremio.PlaylistEntry {
	if index <= 0 {
		return entries
	}
	for i, entry := range entries {
		if entry.Index != index {
			continue
		}
		if i == 0 {
			return entries
		}
		// A fresh slice: the entries belong to a cached playlist view that
		// other requests are reading concurrently.
		out := make([]stremio.PlaylistEntry, 0, len(entries))
		out = append(out, entry)
		out = append(out, entries[:i]...)
		return append(out, entries[i+1:]...)
	}
	return entries
}

func playSessionID(streamName, itemID string) string {
	h := sha1.Sum([]byte(streamName + ":" + itemID + ":" + strconv.FormatInt(time.Now().UnixNano(), 10)))
	return hex.EncodeToString(h[:16])
}

// cappedEntries trims a ranked playlist down to maxSources, keeping the best
// candidates: entries arrive best-first, so truncation only ever drops the tail.
func (s *Server) cappedEntries(rq *request, id itemID, entries []stremio.PlaylistEntry) []stremio.PlaylistEntry {
	limit := s.maxSources()
	if len(entries) <= limit {
		return entries
	}
	logger.Debug("Jellyfin media sources truncated", "stream", rq.streamName(), "content", id.playStremioID(), "total", len(entries), "sent", limit)
	return entries[:limit]
}

// renderedSources caps and renders a playlist view as media sources. Shared
// by attachMediaSources' cached branch and the resolve-on-open path in
// items.go, so the two callers that render a full playlist agree on it.
func (s *Server) renderedSources(rq *request, id itemID, view *stremio.PlaylistView) []mediaSource {
	if view == nil || len(view.Entries) == 0 {
		return nil
	}
	var out []mediaSource
	for _, entry := range s.cappedEntries(rq, id, view.Entries) {
		out = append(out, s.mediaSourceOf(rq, id, entry, view.RuntimeSeconds))
	}
	return out
}

// setItemMediaSources applies Jellyfin's item-level multi-version contract.
// Clients such as Infuse use MediaSourceCount and EnableMediaSourceDisplay to
// decide whether to surface a version picker; the MediaSources array by itself
// is treated as ordinary playback metadata.
func setItemMediaSources(item *baseItem, sources []mediaSource) {
	if item == nil {
		return
	}
	item.MediaSources = sources
	item.AlternateMediaSources = sources
	item.MediaSourceCount = nil
	item.EnableMediaSourceDisplay = nil
	if len(sources) == 0 {
		return
	}
	item.EnableMediaSourceDisplay = boolPtr(true)
	if len(sources) > 1 {
		item.MediaSourceCount = intPtr(len(sources))
	}
}

// attachMediaSourceStubs supplies the two list-level sources Infuse's Direct
// Mode uses to decide whether a movie or episode has selectable versions.
// They are deliberately cheap: the item-open path replaces them with actual
// ranked releases, and slot 0 remains a valid direct-play fallback.
func (s *Server) attachMediaSourceStubs(rq *request, id itemID, item *baseItem) {
	if item == nil || (id.Kind != kindMovie && id.Kind != kindEpisode) {
		return
	}
	runtime := float64(0)
	if item.RunTimeTicks != nil {
		runtime = float64(*item.RunTimeTicks) / float64(ticksPerSecond)
	}
	setItemMediaSources(item, []mediaSource{
		s.mediaSourceOf(rq, id, stremio.PlaylistEntry{Index: 0, Title: item.Name}, runtime),
		s.mediaSourceOf(rq, id, stremio.PlaylistEntry{Index: 1, Title: item.Name + " (2)"}, runtime),
	})
}

func (s *Server) handlePlaybackInfo(w http.ResponseWriter, rq *request, raw string) {
	// The slot travels three ways: encoded in the path when the client asks
	// about a source id, in the query, or in the posted DTO. The later forms
	// win because they are the client naming a pick, while the path may be
	// nothing more than where it happened to navigate from.
	id, index, ok := playableID(raw)
	if !ok {
		http.NotFound(w, rq.Request)
		return
	}
	if idx, ok := sourceIndexFrom(rq.param("mediaSourceId"), id); ok {
		index = idx
	} else if idx, ok := sourceIndexFrom(mediaSourceIDFromBody(rq), id); ok {
		index = idx
	}
	resp := playbackInfoResponse{MediaSources: []mediaSource{}, PlaySessionID: playSessionID(rq.streamName(), id.encode())}
	view, err := s.opts.Catalog.Playlist(rq.Context(), rq.stream, id.ContentType, id.playStremioID())
	if err != nil {
		if errors.Is(err, stremio.ErrMetadataDisabled) {
			http.NotFound(w, rq.Request)
			return
		}
		logger.Warn("Jellyfin playback info failed", "item", raw, "stream", rq.streamName(), "err", err)
		resp.ErrorCode = "NoCompatibleStream"
		writeJSON(w, http.StatusOK, resp)
		return
	}
	// Promoting before capping keeps a pick that ranked outside the cap: the
	// client is naming a release it was offered, so dropping it here would
	// answer a switch with the release it was switching away from.
	for _, entry := range s.cappedEntries(rq, id, promoteSource(view.Entries, index)) {
		resp.MediaSources = append(resp.MediaSources, s.mediaSourceOf(rq, id, entry, view.RuntimeSeconds))
	}
	if len(resp.MediaSources) == 0 {
		resp.ErrorCode = "NoCompatibleStream"
	}
	logger.Info("Jellyfin playback info", "stream", rq.streamName(), "content", id.playStremioID(), "sources", len(resp.MediaSources), "slot", index)
	writeJSON(w, http.StatusOK, resp)
}

// attachMediaSources gives a playable item the media sources a real Jellyfin
// server carries on its detail document. Clients do not treat them as
// optional: Findroid reads sources.first() while building its detail screen
// and throws on an empty list, leaving the screen loading forever.
//
// Resolving candidates means searching indexers, which is far too slow to run
// every time a poster is opened, so this is deliberately cheap. A playlist
// already in cache is rendered in full; otherwise the item gets one stand-in
// source naming slot 0 — the best candidate, whichever it turns out to be —
// and the real ranked list arrives when the client asks for PlaybackInfo on
// play. Playing the stand-in is correct either way: the slot resolves and
// fails over exactly as it does for a Stremio client.
func (s *Server) attachMediaSources(rq *request, id itemID, item *baseItem) {
	if item == nil || (id.Kind != kindMovie && id.Kind != kindEpisode) {
		return
	}
	if view, ok := s.opts.Catalog.PlaylistCached(rq.stream, id.ContentType, id.playStremioID()); ok && view != nil && len(view.Entries) > 0 {
		setItemMediaSources(item, s.renderedSources(rq, id, view))
		return
	}
	s.attachMediaSourceStubs(rq, id, item)
}

// mediaSourceOf renders one candidate.
//
// Protocol is "Http" and Path is the stream URL rather than the release
// title: the release title is what a user picking between sources wants to
// read, but it is already in Name, and Path is one of the two fields that
// carry the token to the player — see streamURLFor.
func (s *Server) mediaSourceOf(rq *request, id itemID, entry stremio.PlaylistEntry, runtimeSeconds float64) mediaSource {
	parsed := parser.ParseReleaseTitle(entry.Title)
	container := "mkv"
	if parsed != nil && parsed.Result != nil && parsed.Container != "" {
		container = strings.ToLower(parsed.Container)
	}
	src := mediaSource{
		Protocol:               "Http",
		ID:                     mediaSourceIDFor(id, entry.Index),
		Path:                   s.streamURLFor(id, entry.Index, rq.stream),
		ETag:                   streamTokenOf(rq.stream),
		Type:                   "Default",
		Container:              container,
		Name:                   entry.Title,
		IsRemote:               true,
		SupportsDirectStream:   false,
		SupportsDirectPlay:     true,
		SupportsProbing:        false,
		TranscodingSubProtocol: "http",
		VideoType:              "VideoFile",
		MediaStreams:           mediaStreamsOf(parsed, entry.Caps),
		MediaAttachments:       []any{},
		Formats:                []string{},
		RequiredHTTPHeaders:    map[string]string{},
	}
	if entry.Size > 0 {
		src.Size = int64Ptr(entry.Size)
	}
	seconds := runtimeSeconds
	if entry.Caps != nil && entry.Caps.DurationSeconds > 0 {
		seconds = entry.Caps.DurationSeconds
	}
	if seconds > 0 {
		src.RunTimeTicks = int64Ptr(int64(seconds * float64(ticksPerSecond)))
		if entry.Size > 0 {
			src.Bitrate = intPtr(int(float64(entry.Size) * 8 / seconds))
		}
	}
	for i, stream := range src.MediaStreams {
		if stream.Type == "Audio" {
			src.DefaultAudioStreamIndex = intPtr(i)
			break
		}
	}
	return src
}

var resolutionSizes = map[string][2]int{
	"2160p": {3840, 2160}, "4k": {3840, 2160}, "1440p": {2560, 1440},
	"1080p": {1920, 1080}, "720p": {1280, 720}, "576p": {1024, 576}, "480p": {720, 480},
}

// mediaStreamsOf describes the tracks: what ffprobe measured when the
// release played before, otherwise what the title claims.
func mediaStreamsOf(parsed *parser.ParsedRelease, caps *release.MediaCaps) []mediaStream {
	video := mediaStream{Type: "Video", IsDefault: true}
	var audio []mediaStream
	if caps != nil {
		video.Codec = strings.ToLower(caps.VideoCodec)
		video.Profile = caps.Profile
		if caps.Width > 0 {
			video.Width = intPtr(caps.Width)
		}
		if caps.Height > 0 {
			video.Height = intPtr(caps.Height)
		}
		if caps.BitDepth > 0 {
			video.BitDepth = intPtr(caps.BitDepth)
		}
		switch {
		case caps.DolbyVision:
			video.VideoRange, video.VideoRangeType = "HDR", "DOVI"
		case caps.HDR != "":
			video.VideoRange, video.VideoRangeType = "HDR", strings.ReplaceAll(caps.HDR, "+", "Plus")
		default:
			video.VideoRange, video.VideoRangeType = "SDR", "SDR"
		}
		if caps.TracksProbed {
			for i := 0; i < caps.AudioStreams; i++ {
				track := mediaStream{Type: "Audio", Codec: strings.ToLower(caps.AudioCodec), IsDefault: i == 0}
				if i < len(caps.AudioLanguages) {
					track.Language = caps.AudioLanguages[i]
				}
				audio = append(audio, track)
			}
		} else if caps.AudioCodec != "" {
			audio = append(audio, mediaStream{Type: "Audio", Codec: strings.ToLower(caps.AudioCodec), IsDefault: true})
		}
	} else if parsed != nil && parsed.Result != nil {
		switch strings.ToLower(parsed.Codec) {
		case "hevc", "x265", "h265":
			video.Codec = "hevc"
		case "avc", "x264", "h264":
			video.Codec = "h264"
		case "av1":
			video.Codec = "av1"
		}
		if size, ok := resolutionSizes[strings.ToLower(parsed.Resolution)]; ok {
			video.Width, video.Height = intPtr(size[0]), intPtr(size[1])
		}
		video.VideoRange, video.VideoRangeType = "SDR", "SDR"
		for _, hdr := range parsed.HDR {
			switch strings.ToUpper(hdr) {
			case "DV", "DOLBY VISION":
				video.VideoRange, video.VideoRangeType = "HDR", "DOVI"
			case "HDR10+":
				video.VideoRange, video.VideoRangeType = "HDR", "HDR10Plus"
			case "HDR", "HDR10", "HLG":
				if video.VideoRangeType != "DOVI" && video.VideoRangeType != "HDR10Plus" {
					video.VideoRange, video.VideoRangeType = "HDR", "HDR10"
				}
			}
		}
		for i, codec := range parsed.Audio {
			track := mediaStream{Type: "Audio", Codec: strings.ToLower(codec), IsDefault: i == 0}
			if i < len(parsed.Languages) {
				track.Language = parsed.Languages[i]
			}
			audio = append(audio, track)
		}
	}
	// A video track without dimensions is worse than no video track. Once a
	// client sees one it reads the height and width unconditionally — Findroid
	// asserts both non-null — and a release that was never probed and whose
	// title names no resolution has neither. Dropping the track costs a badge;
	// keeping it crashes the screen the badge would sit on.
	streams := []mediaStream{}
	if video.Width != nil && video.Height != nil {
		video.DisplayTitle = strings.TrimSpace(strings.ToUpper(video.Codec) + " " + video.VideoRangeType)
		streams = append(streams, video)
	}
	for _, track := range audio {
		track.DisplayTitle = strings.TrimSpace(track.Language + " " + strings.ToUpper(track.Codec))
		streams = append(streams, track)
	}
	for i := range streams {
		streams[i].Index = i
	}
	return streams
}

// handleStream serves /Videos/{id}/stream: the slot the media source names,
// through the addon's play path. Failover redirects come back here with the
// next source's id, so the client sees a media-source switch rather than a
// foreign URL.
func (s *Server) handleStream(w http.ResponseWriter, rq *request, raw string) {
	id, index, ok := playableID(raw)
	if !ok {
		http.NotFound(w, rq.Request)
		return
	}
	if idx, ok := sourceIndexFrom(rq.param("mediaSourceId"), id); ok {
		index = idx
	}
	contentID := id.playStremioID()
	slotPath := stremio.SlotPathFor(rq.stream, id.ContentType, contentID, index)
	logger.Debug("Jellyfin stream request", "stream", rq.streamName(), "content", contentID, "slot", index)

	// The redirect must keep authenticating: a client that sent the token
	// in a header may or may not replay it on a redirect, so the query
	// carries it from here on when it did not already.
	query := rq.URL.Query()
	if clientOf(rq).Token != "" && rq.param("api_key") == "" && rq.param("apikey") == "" {
		query.Set("api_key", clientOf(rq).Token)
	}
	opts := stremio.PlayServeOptions{
		FailWithStatus: true,
		SlotLocation: func(nextSlot string) string {
			nextIndex, ok := stremio.SlotIndexOf(nextSlot)
			if !ok {
				nextIndex = index
			}
			q := url.Values{}
			for k, v := range query {
				q[k] = v
			}
			setParam(q, "mediaSourceId", mediaSourceIDFor(id, nextIndex))
			return Mount + "videos/" + id.encode() + "/stream?" + q.Encode()
		},
	}
	s.opts.Catalog.ServePlay(w, rq.Request, rq.stream, slotPath, opts)
}

// playbackReport is what a client says about where it is. The body form is
// the Jellyfin one; the query form is the legacy PlayingItems route.
type playbackReport struct {
	ItemID        string `json:"ItemId"`
	MediaSourceID string `json:"MediaSourceId"`
	PositionTicks *int64 `json:"PositionTicks"`
	IsPaused      bool   `json:"IsPaused"`
	PlaySessionID string `json:"PlaySessionId"`
}

func readReport(rq *request, pathItem string) (playbackReport, bool) {
	var report playbackReport
	if rq.Body != nil {
		raw, err := io.ReadAll(io.LimitReader(rq.Body, 256<<10))
		if err == nil && len(raw) > 0 {
			_ = json.Unmarshal(raw, &report)
		}
	}
	if report.ItemID == "" {
		report.ItemID = rq.param("itemId")
	}
	if report.ItemID == "" {
		report.ItemID = pathItem
	}
	if report.MediaSourceID == "" {
		report.MediaSourceID = rq.param("mediaSourceId")
	}
	if report.PositionTicks == nil {
		if v := rq.param("positionTicks"); v != "" {
			if n, err := strconv.ParseInt(v, 10, 64); err == nil {
				report.PositionTicks = &n
			}
		}
	}
	if !report.IsPaused {
		report.IsPaused = rq.boolParam("isPaused")
	}
	return report, report.ItemID != ""
}

// handlePlaying serves the Sessions/Playing family: "" (started),
// "progress" and "stopped".
func (s *Server) handlePlaying(w http.ResponseWriter, rq *request, event, pathItem string) {
	report, ok := readReport(rq, pathItem)
	if !ok {
		writeEmpty(w)
		return
	}
	id, _, ok := playableID(report.ItemID)
	if !ok {
		writeEmpty(w)
		return
	}
	position := int64(0)
	if report.PositionTicks != nil {
		position = *report.PositionTicks
	}
	switch event {
	case "", "playing":
		s.recordProgress(rq, id, position, playEventStart)
	case "progress":
		if report.IsPaused {
			s.recordProgress(rq, id, position, playEventPause)
		} else {
			s.recordProgress(rq, id, position, playEventTick)
		}
	case "stopped":
		s.recordProgress(rq, id, position, playEventStop)
	case "ping":
	default:
		http.NotFound(w, rq.Request)
		return
	}
	writeEmpty(w)
}

func (s *Server) handlePlayed(w http.ResponseWriter, rq *request, raw string) {
	id, _, ok := playableID(raw)
	if !ok {
		http.NotFound(w, rq.Request)
		return
	}
	switch {
	case rq.is(http.MethodPost):
		s.setPlayed(rq, id, true)
	case rq.is(http.MethodDelete):
		s.setPlayed(rq, id, false)
	default:
		http.NotFound(w, rq.Request)
		return
	}
	writeJSON(w, http.StatusOK, s.userData(rq, id))
}

func (s *Server) writeUserData(w http.ResponseWriter, rq *request, raw string) {
	id, err := decodeItemID(raw)
	if err != nil {
		http.NotFound(w, rq.Request)
		return
	}
	writeJSON(w, http.StatusOK, s.userData(rq, id.playable()))
}

// handleUserDataUpdate serves POST /UserItems/{id}/UserData, the newer way
// clients mark an item played or set its position.
func (s *Server) handleUserDataUpdate(w http.ResponseWriter, rq *request, raw string) {
	id, _, ok := playableID(raw)
	if !ok {
		http.NotFound(w, rq.Request)
		return
	}
	var body struct {
		Played                *bool  `json:"Played"`
		PlaybackPositionTicks *int64 `json:"PlaybackPositionTicks"`
	}
	if data, err := io.ReadAll(io.LimitReader(rq.Body, 64<<10)); err == nil {
		_ = json.Unmarshal(data, &body)
	}
	if body.Played != nil {
		s.setPlayed(rq, id, *body.Played)
	}
	if body.PlaybackPositionTicks != nil {
		s.recordProgress(rq, id, *body.PlaybackPositionTicks, playEventStop)
	}
	writeJSON(w, http.StatusOK, s.userData(rq, id))
}
