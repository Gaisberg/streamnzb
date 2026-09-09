package jellyfin

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"streamnzb/pkg/server/stremio"
)

// The Jellyfin client SDKs generate their models from the server's OpenAPI
// description, and a field the description declares non-nullable with no
// default becomes a required constructor argument. kotlinx.serialization then
// refuses the whole document when such a field is missing or null — one absent
// boolean fails a login, and the client reports it as an unknown error with
// nothing in it to say which field was at fault.
//
// So the contract is asserted here instead of being discovered one client at a
// time. Each entry is a model the layer emits and the fields the SDK requires
// of it, taken from the generated models in jellyfin-sdk-kotlin. Adding a
// field to a DTO is free; removing one, or letting a slice marshal as null,
// breaks a real client and fails this test.
var sdkRequired = map[string][]string{
	"UserDto": {"Id"},
	"SessionInfoDto": {
		"PlayableMediaTypes", "UserId", "LastActivityDate", "LastPlaybackCheckIn", "IsActive",
		"SupportsMediaControl", "SupportsRemoteControl", "HasCustomDeviceName", "SupportedCommands",
	},
	"BaseItemDto":    {"Id", "Type"},
	"QueryResult":    {"Items", "TotalRecordCount", "StartIndex"},
	"UserItemData":   {"PlaybackPositionTicks", "PlayCount", "IsFavorite", "Played", "Key", "ItemId"},
	"SearchHint":     {"ItemId", "Id", "Name", "Type", "Artists"},
	"SearchHintList": {"SearchHints", "TotalRecordCount"},
	"PlaybackInfo":   {"MediaSources"},
	"MediaSourceInfo": {
		"Protocol", "Type", "IsRemote", "ReadAtNativeFramerate", "IgnoreDts", "IgnoreIndex", "GenPtsInput",
		"SupportsTranscoding", "SupportsDirectStream", "SupportsDirectPlay", "IsInfiniteStream",
		"RequiresOpening", "RequiresClosing", "RequiresLooping", "SupportsProbing",
		"TranscodingSubProtocol", "HasSegments",
	},
	"MediaStream": {
		"IsInterlaced", "IsDefault", "IsForced", "IsHearingImpaired", "IsOriginal", "Type", "Index",
		"IsExternal", "IsTextSubtitleStream", "SupportsExternalStream",
	},
	"SystemInfo":      {"HasPendingRestart", "IsShuttingDown", "SupportsLibraryMonitor", "WebSocketPortNumber"},
	"PlayerStateInfo": {"CanSeek", "IsPaused", "IsMuted", "RepeatMode", "PlaybackOrder"},
	"ClientCapabilities": {
		"PlayableMediaTypes", "SupportedCommands", "SupportsMediaControl", "SupportsPersistentIdentifier",
	},
	"UserConfiguration": {
		"PlayDefaultAudioTrack", "DisplayMissingEpisodes", "GroupedFolders", "SubtitleMode",
		"DisplayCollectionsView", "EnableLocalPassword", "OrderedViews", "LatestItemsExcludes",
		"MyMediaExcludes", "HidePlayedInLatest", "RememberAudioSelections",
		"RememberSubtitleSelections", "EnableNextEpisodeAutoPlay",
	},
	"UserPolicy": {
		"IsAdministrator", "IsHidden", "IsDisabled", "EnableUserPreferenceAccess",
		"EnableRemoteControlOfOtherUsers", "EnableSharedDeviceControl", "EnableRemoteAccess",
		"EnableLiveTvManagement", "EnableLiveTvAccess", "EnableMediaPlayback",
		"EnableAudioPlaybackTranscoding", "EnableVideoPlaybackTranscoding", "EnablePlaybackRemuxing",
		"ForceRemoteSourceTranscoding", "EnableContentDeletion", "EnableContentDownloading",
		"EnableSyncTranscoding", "EnableMediaConversion", "EnableAllDevices", "EnableAllChannels",
		"EnableAllFolders", "InvalidLoginAttemptCount", "LoginAttemptsBeforeLockout", "MaxActiveSessions",
		"EnablePublicSharing", "RemoteClientBitrateLimit", "AuthenticationProviderId",
		"PasswordResetProviderId", "SyncPlayAccess",
	},
}

// sdkEnums are required fields the SDK types as an enum rather than a string.
// An empty value is as fatal as a missing one — "" is not a member of any of
// these — but it would pass a mere presence check, so they are named here.
var sdkEnums = map[string]bool{
	"Type": true, "Protocol": true, "TranscodingSubProtocol": true,
	"RepeatMode": true, "PlaybackOrder": true, "SubtitleMode": true, "SyncPlayAccess": true,
}

// requireFields checks one object against a model's contract. A missing field
// and a null one fail alike: the client cannot construct the model either way.
func requireFields(t *testing.T, model string, obj map[string]any, where string) {
	t.Helper()
	fields, ok := sdkRequired[model]
	if !ok {
		t.Fatalf("no contract recorded for %s", model)
	}
	for _, field := range fields {
		value, present := obj[field]
		switch {
		case !present:
			t.Errorf("%s (%s): required field %q is missing", where, model, field)
		case value == nil:
			t.Errorf("%s (%s): required field %q is null", where, model, field)
		case sdkEnums[field] && value == "":
			t.Errorf("%s (%s): required enum field %q is empty", where, model, field)
		}
	}
}

func objectOf(t *testing.T, raw []byte, where string) map[string]any {
	t.Helper()
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		t.Fatalf("%s: %v: %s", where, err, raw)
	}
	return obj
}

func child(obj map[string]any, key string) map[string]any {
	nested, _ := obj[key].(map[string]any)
	return nested
}

func children(obj map[string]any, key string) []map[string]any {
	list, _ := obj[key].([]any)
	var out []map[string]any
	for _, item := range list {
		if m, ok := item.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

// checkVideoStreams asserts no video track is offered without its dimensions.
// A client that sees a video track reads height and width unconditionally —
// Findroid asserts both non-null — so an undimensioned track is a crash, not a
// missing badge.
func checkVideoStreams(t *testing.T, source map[string]any, where string) {
	t.Helper()
	for i, stream := range children(source, "MediaStreams") {
		if stream["Type"] != "Video" {
			continue
		}
		for _, field := range []string{"Width", "Height"} {
			if value, ok := stream[field]; !ok || value == nil {
				t.Errorf("%s.MediaStreams[%d]: video track without %s", where, i, field)
			}
		}
	}
}

// checkQuery asserts a listing envelope and the items in it.
func checkQuery(t *testing.T, obj map[string]any, where string) {
	t.Helper()
	requireFields(t, "QueryResult", obj, where)
	for i, item := range children(obj, "Items") {
		at := fmt.Sprintf("%s.Items[%d]", where, i)
		requireFields(t, "BaseItemDto", item, at)
		if data := child(item, "UserData"); data != nil {
			requireFields(t, "UserItemData", data, at+".UserData")
		}
	}
}

func TestResponsesSatisfyTheClientSDKContract(t *testing.T) {
	f := newFixture()

	// Login: the document a client fails on before it can do anything else.
	auth := objectOf(t, f.do(http.MethodPost, "/jellyfin/Users/AuthenticateByName",
		`{"Username":"living-room","Pw":"`+testToken+`"}`).Body.Bytes(), "AuthenticateByName")
	user := child(auth, "User")
	requireFields(t, "UserDto", user, "AuthenticateByName.User")
	requireFields(t, "UserConfiguration", child(user, "Configuration"), "AuthenticateByName.User.Configuration")
	requireFields(t, "UserPolicy", child(user, "Policy"), "AuthenticateByName.User.Policy")
	session := child(auth, "SessionInfo")
	requireFields(t, "SessionInfoDto", session, "AuthenticateByName.SessionInfo")
	requireFields(t, "PlayerStateInfo", child(session, "PlayState"), "AuthenticateByName.SessionInfo.PlayState")
	requireFields(t, "ClientCapabilities", child(session, "Capabilities"), "AuthenticateByName.SessionInfo.Capabilities")

	requireFields(t, "UserDto", objectOf(t, f.do(http.MethodGet, "/jellyfin/Users/Me", "").Body.Bytes(), "Users/Me"), "Users/Me")
	requireFields(t, "SystemInfo", objectOf(t, f.do(http.MethodGet, "/jellyfin/System/Info", "").Body.Bytes(), "System/Info"), "System/Info")

	var sessions []map[string]any
	if err := json.Unmarshal(f.do(http.MethodGet, "/jellyfin/Sessions", "").Body.Bytes(), &sessions); err != nil {
		t.Fatalf("Sessions: %v", err)
	}
	for i, s := range sessions {
		requireFields(t, "SessionInfoDto", s, fmt.Sprintf("Sessions[%d]", i))
	}

	// Browsing: libraries, a library's rows, a detail page, an episode list.
	checkQuery(t, objectOf(t, f.do(http.MethodGet, "/jellyfin/UserViews", "").Body.Bytes(), "UserViews"), "UserViews")
	movie, _ := itemIDFor("movie", "tt0111161")
	requireFields(t, "BaseItemDto",
		objectOf(t, f.do(http.MethodGet, "/jellyfin/Items/"+movie.encode(), "").Body.Bytes(), "Items/{id}"), "Items/{id}")
	// A playable item's detail document must carry media sources: a client
	// builds its detail screen from the first one and breaks on an empty list.
	detail := objectOf(t, f.do(http.MethodGet, "/jellyfin/Items/"+movie.encode(), "").Body.Bytes(), "Items/{id}")
	sources := children(detail, "MediaSources")
	if len(sources) == 0 {
		t.Fatalf("movie detail carries no MediaSources")
	}
	for i, source := range sources {
		at := fmt.Sprintf("Items/{id}.MediaSources[%d]", i)
		requireFields(t, "MediaSourceInfo", source, at)
		checkVideoStreams(t, source, at)
	}

	series, _ := itemIDFor("series", "tt0903747")
	checkQuery(t, objectOf(t, f.do(http.MethodGet, "/jellyfin/Shows/"+series.encode()+"/Episodes?Season=1", "").Body.Bytes(),
		"Shows/{id}/Episodes"), "Shows/{id}/Episodes")
	episode := objectOf(t, f.do(http.MethodGet, "/jellyfin/Items/"+series.episode(1, 1).encode(), "").Body.Bytes(), "Items/{episode}")
	if len(children(episode, "MediaSources")) == 0 {
		t.Fatalf("episode detail carries no MediaSources")
	}
	checkQuery(t, objectOf(t, f.do(http.MethodGet, "/jellyfin/UserItems/Resume", "").Body.Bytes(), "Resume"), "Resume")

	// Search hints, the shape the client's search box reads.
	hints := objectOf(t, f.do(http.MethodGet, "/jellyfin/Search/Hints?searchTerm=bad&includeItemTypes=Series", "").Body.Bytes(), "Search/Hints")
	requireFields(t, "SearchHintList", hints, "Search/Hints")
	for i, hint := range children(hints, "SearchHints") {
		requireFields(t, "SearchHint", hint, fmt.Sprintf("Search/Hints.SearchHints[%d]", i))
	}

	// Playback: the media sources and their streams.
	f.catalog.playlist = &stremio.PlaylistView{
		ContentTitle:   "The Shawshank Redemption",
		RuntimeSeconds: 142 * 60,
		Entries: []stremio.PlaylistEntry{
			{Index: 0, SlotPath: "stream:living-room:movie:tt0111161:0", Title: "The.Shawshank.Redemption.1994.2160p.UHD.BluRay.x265.HDR.DTS-HD.MA.5.1-GRP", Size: 40 << 30, Caps: &releaseCaps},
			{Index: 1, SlotPath: "stream:living-room:movie:tt0111161:1", Title: "The.Shawshank.Redemption.1994.1080p.BluRay.x264.DD5.1-GRP.mkv", Size: 12 << 30},
		},
	}
	info := objectOf(t, f.do(http.MethodPost, "/jellyfin/Items/"+movie.encode()+"/PlaybackInfo?UserId=u",
		`{"DeviceProfile":{}}`).Body.Bytes(), "PlaybackInfo")
	requireFields(t, "PlaybackInfo", info, "PlaybackInfo")
	for i, source := range children(info, "MediaSources") {
		at := fmt.Sprintf("PlaybackInfo.MediaSources[%d]", i)
		requireFields(t, "MediaSourceInfo", source, at)
		checkVideoStreams(t, source, at)
		for j, stream := range children(source, "MediaStreams") {
			requireFields(t, "MediaStream", stream, fmt.Sprintf("%s.MediaStreams[%d]", at, j))
		}
	}
}
