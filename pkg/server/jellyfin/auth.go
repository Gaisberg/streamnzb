package jellyfin

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"streamnzb/pkg/auth"
	"streamnzb/pkg/core/logger"
)

// A login is a stream: the stream name as the username and the stream token
// as the password. The admin signs in with the dashboard username and
// password instead and is served as the admin stream, exactly as the addon
// treats the admin token. What the client stores afterwards is the stream
// token, presented on every request the way Jellyfin clients present their
// access token — in the MediaBrowser authorization header, one of the legacy
// token headers, or an api_key query parameter.

// authorizationHeaders lists where a client puts its access token, in the
// order Jellyfin itself checks them.
var authorizationHeaders = []string{"Authorization", "X-Emby-Authorization"}
var tokenHeaders = []string{"X-Emby-Token", "X-MediaBrowser-Token"}

// clientIdentity is what the MediaBrowser header says about the client.
type clientIdentity struct {
	Client, Device, DeviceID, Version, Token string
}

// parseMediaBrowserHeader reads
//
//	MediaBrowser Client="Swiftfin", Device="iPhone", DeviceId="abc", Version="1.2", Token="..."
//
// Values may be quoted or bare; Emby-era clients wrote the scheme as "Emby".
func parseMediaBrowserHeader(value string) (clientIdentity, bool) {
	var id clientIdentity
	scheme, rest, ok := strings.Cut(strings.TrimSpace(value), " ")
	if !ok || (!strings.EqualFold(scheme, "MediaBrowser") && !strings.EqualFold(scheme, "Emby")) {
		return id, false
	}
	for _, pair := range splitHeaderPairs(rest) {
		key, val, ok := strings.Cut(pair, "=")
		if !ok {
			continue
		}
		val = strings.Trim(strings.TrimSpace(val), `"`)
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "client":
			id.Client = val
		case "device":
			id.Device = val
		case "deviceid":
			id.DeviceID = val
		case "version":
			id.Version = val
		case "token":
			id.Token = val
		}
	}
	return id, true
}

// splitHeaderPairs splits on commas outside quotes, since device names carry
// commas ("iPad Pro 11-inch, 4th gen").
func splitHeaderPairs(s string) []string {
	var pairs []string
	var cur strings.Builder
	quoted := false
	for _, r := range s {
		switch {
		case r == '"':
			quoted = !quoted
			cur.WriteRune(r)
		case r == ',' && !quoted:
			pairs = append(pairs, cur.String())
			cur.Reset()
		default:
			cur.WriteRune(r)
		}
	}
	if cur.Len() > 0 {
		pairs = append(pairs, cur.String())
	}
	return pairs
}

// clientOf reads the client identity, token included, from wherever the
// request carries it.
func clientOf(rq *request) clientIdentity {
	var id clientIdentity
	for _, name := range authorizationHeaders {
		if parsed, ok := parseMediaBrowserHeader(rq.Header.Get(name)); ok {
			id = parsed
			break
		}
	}
	if id.Token == "" {
		for _, name := range tokenHeaders {
			if v := strings.TrimSpace(rq.Header.Get(name)); v != "" {
				id.Token = v
				break
			}
		}
	}
	if id.Token == "" {
		id.Token = rq.param("api_key")
	}
	if id.Token == "" {
		id.Token = rq.param("apikey")
	}
	return id
}

func (s *Server) admin() (username, passwordHash, token string) {
	if s.opts.Admin == nil {
		return "", "", ""
	}
	return s.opts.Admin()
}

// authenticate resolves the request's access token to a stream. The admin
// token is refused for the same reason the admin login is: it names an
// identity with nothing to play. Refusing it here too means a client holding
// an admin session from before is made to sign in again rather than browsing
// its way to a failure.
func (s *Server) authenticate(rq *request) (*auth.Stream, bool) {
	token := clientOf(rq).Token
	if token == "" || s.opts.Streams == nil {
		return nil, false
	}
	adminUser, _, adminToken := s.admin()
	stream, err := s.opts.Streams.AuthenticateToken(token, adminUser, adminToken)
	if err != nil || stream == nil {
		return nil, false
	}
	if adminUser != "" && strings.EqualFold(stream.Username, adminUser) {
		return nil, false
	}
	return stream, true
}

// streamFromTag resolves the stream a bare video request belongs to, from the
// token carried in its tag parameter.
//
// A client that builds its own stream URL rather than playing the media
// source's Path sends nothing that identifies the stream — no Authorization
// header, no api_key — except the media source's ETag, which Swiftfin and
// Wholphin both pass through as tag. That is where the token is put, and this
// reads it back; see streamURLFor for the whole arrangement.
//
// It is scoped to the stream route alone. A token lifted out of a tag opens
// one video and nothing else — it cannot list a library, read progress or
// reach any other route, because no other route consults it.
func (s *Server) streamFromTag(rq *request) (*auth.Stream, bool) {
	if len(rq.segs) != 3 || rq.segs[0] != "videos" {
		return nil, false
	}
	if name, _, _ := strings.Cut(rq.segs[2], "."); name != "stream" {
		return nil, false
	}
	token := rq.param("tag")
	if token == "" || s.opts.Streams == nil {
		return nil, false
	}
	adminUser, _, adminToken := s.admin()
	stream, err := s.opts.Streams.AuthenticateToken(token, adminUser, adminToken)
	if err != nil || stream == nil {
		return nil, false
	}
	if adminUser != "" && strings.EqualFold(stream.Username, adminUser) {
		return nil, false
	}
	return stream, true
}

// login resolves a username and password to a stream: a stream name with its
// own password, or with its token in place of one.
//
// The dashboard admin is deliberately not a login here. It is an identity
// rather than a stream — AuthenticateToken mints it with no search queries,
// indexers or filter profile — so it can browse catalogs and then fail at the
// one thing a media client is for. Refusing it at the door turns that into an
// error the user sees while typing credentials, instead of a spinner that ends
// in "failed to start" after everything else appeared to work.
func (s *Server) login(username, password string) (*auth.Stream, bool) {
	username = strings.TrimSpace(username)
	if username == "" || password == "" || s.opts.Streams == nil {
		return nil, false
	}
	if adminUser, _, _ := s.admin(); adminUser != "" && strings.EqualFold(username, adminUser) {
		logger.Warn("Jellyfin login refused for the admin account",
			"username", username,
			"reason", "the admin is not a stream and has nothing to play; sign in with a stream name")
		return nil, false
	}
	stream, err := s.opts.Streams.AuthenticateStream(username, password)
	if err != nil || stream == nil {
		return nil, false
	}
	return stream, true
}

// userID is the GUID a stream is presented as. Derived, so it is the same
// across restarts and instances.
func userID(streamName string) string {
	h := sha1.Sum([]byte("streamnzb-user:" + streamName))
	return hex.EncodeToString(h[:16])
}

// jellyfinTime is the timestamp form Jellyfin writes (seven fractional
// digits, UTC).
func jellyfinTime(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.0000000Z")
}

type userConfiguration struct {
	PlayDefaultAudioTrack      bool     `json:"PlayDefaultAudioTrack"`
	SubtitleLanguagePreference string   `json:"SubtitleLanguagePreference"`
	DisplayMissingEpisodes     bool     `json:"DisplayMissingEpisodes"`
	GroupedFolders             []string `json:"GroupedFolders"`
	SubtitleMode               string   `json:"SubtitleMode"`
	DisplayCollectionsView     bool     `json:"DisplayCollectionsView"`
	EnableLocalPassword        bool     `json:"EnableLocalPassword"`
	OrderedViews               []string `json:"OrderedViews"`
	LatestItemsExcludes        []string `json:"LatestItemsExcludes"`
	MyMediaExcludes            []string `json:"MyMediaExcludes"`
	HidePlayedInLatest         bool     `json:"HidePlayedInLatest"`
	RememberAudioSelections    bool     `json:"RememberAudioSelections"`
	RememberSubtitleSelections bool     `json:"RememberSubtitleSelections"`
	EnableNextEpisodeAutoPlay  bool     `json:"EnableNextEpisodeAutoPlay"`
	CastReceiverID             string   `json:"CastReceiverId"`
}

// userPolicy is the Jellyfin permission set. Nobody is an administrator
// here, the admin included: a client that sees the flag renders a server
// dashboard, user management and library scans that lead nowhere.
type userPolicy struct {
	IsAdministrator                  bool     `json:"IsAdministrator"`
	IsHidden                         bool     `json:"IsHidden"`
	IsDisabled                       bool     `json:"IsDisabled"`
	BlockedTags                      []string `json:"BlockedTags"`
	AllowedTags                      []string `json:"AllowedTags"`
	EnableUserPreferenceAccess       bool     `json:"EnableUserPreferenceAccess"`
	AccessSchedules                  []any    `json:"AccessSchedules"`
	BlockUnratedItems                []string `json:"BlockUnratedItems"`
	EnableRemoteControlOfOtherUsers  bool     `json:"EnableRemoteControlOfOtherUsers"`
	EnableSharedDeviceControl        bool     `json:"EnableSharedDeviceControl"`
	EnableRemoteAccess               bool     `json:"EnableRemoteAccess"`
	EnableLiveTvManagement           bool     `json:"EnableLiveTvManagement"`
	EnableLiveTvAccess               bool     `json:"EnableLiveTvAccess"`
	EnableMediaPlayback              bool     `json:"EnableMediaPlayback"`
	EnableAudioPlaybackTranscoding   bool     `json:"EnableAudioPlaybackTranscoding"`
	EnableVideoPlaybackTranscoding   bool     `json:"EnableVideoPlaybackTranscoding"`
	EnablePlaybackRemuxing           bool     `json:"EnablePlaybackRemuxing"`
	ForceRemoteSourceTranscoding     bool     `json:"ForceRemoteSourceTranscoding"`
	EnableContentDeletion            bool     `json:"EnableContentDeletion"`
	EnableContentDeletionFromFolders []string `json:"EnableContentDeletionFromFolders"`
	EnableContentDownloading         bool     `json:"EnableContentDownloading"`
	EnableSyncTranscoding            bool     `json:"EnableSyncTranscoding"`
	EnableMediaConversion            bool     `json:"EnableMediaConversion"`
	EnabledDevices                   []string `json:"EnabledDevices"`
	EnableAllDevices                 bool     `json:"EnableAllDevices"`
	EnabledChannels                  []string `json:"EnabledChannels"`
	EnableAllChannels                bool     `json:"EnableAllChannels"`
	EnabledFolders                   []string `json:"EnabledFolders"`
	EnableAllFolders                 bool     `json:"EnableAllFolders"`
	InvalidLoginAttemptCount         int      `json:"InvalidLoginAttemptCount"`
	LoginAttemptsBeforeLockout       int      `json:"LoginAttemptsBeforeLockout"`
	MaxActiveSessions                int      `json:"MaxActiveSessions"`
	EnablePublicSharing              bool     `json:"EnablePublicSharing"`
	BlockedMediaFolders              []string `json:"BlockedMediaFolders"`
	BlockedChannels                  []string `json:"BlockedChannels"`
	RemoteClientBitrateLimit         int      `json:"RemoteClientBitrateLimit"`
	AuthenticationProviderID         string   `json:"AuthenticationProviderId"`
	PasswordResetProviderID          string   `json:"PasswordResetProviderId"`
	SyncPlayAccess                   string   `json:"SyncPlayAccess"`
}

type userDto struct {
	Name                      string            `json:"Name"`
	ServerID                  string            `json:"ServerId"`
	ID                        string            `json:"Id"`
	HasPassword               bool              `json:"HasPassword"`
	HasConfiguredPassword     bool              `json:"HasConfiguredPassword"`
	HasConfiguredEasyPassword bool              `json:"HasConfiguredEasyPassword"`
	EnableAutoLogin           bool              `json:"EnableAutoLogin"`
	LastLoginDate             string            `json:"LastLoginDate"`
	LastActivityDate          string            `json:"LastActivityDate"`
	Configuration             userConfiguration `json:"Configuration"`
	Policy                    userPolicy        `json:"Policy"`
}

func (s *Server) userDto(stream *auth.Stream) userDto {
	now := jellyfinTime(time.Now())
	return userDto{
		Name:                  stream.Username,
		ServerID:              s.serverID(),
		ID:                    userID(stream.Username),
		HasPassword:           true,
		HasConfiguredPassword: true,
		LastLoginDate:         now,
		LastActivityDate:      now,
		Configuration: userConfiguration{
			PlayDefaultAudioTrack:      true,
			GroupedFolders:             []string{},
			SubtitleMode:               "Default",
			OrderedViews:               []string{},
			LatestItemsExcludes:        []string{},
			MyMediaExcludes:            []string{},
			HidePlayedInLatest:         true,
			RememberAudioSelections:    true,
			RememberSubtitleSelections: true,
			EnableNextEpisodeAutoPlay:  true,
		},
		Policy: userPolicy{
			BlockedTags:                      []string{},
			AllowedTags:                      []string{},
			EnableUserPreferenceAccess:       true,
			AccessSchedules:                  []any{},
			BlockUnratedItems:                []string{},
			EnableRemoteAccess:               true,
			EnableMediaPlayback:              true,
			EnableContentDeletionFromFolders: []string{},
			EnabledDevices:                   []string{},
			EnableAllDevices:                 true,
			EnabledChannels:                  []string{},
			EnableAllChannels:                true,
			EnabledFolders:                   []string{},
			EnableAllFolders:                 true,
			LoginAttemptsBeforeLockout:       -1,
			BlockedMediaFolders:              []string{},
			BlockedChannels:                  []string{},
			AuthenticationProviderID:         "Jellyfin.Server.Implementations.Users.DefaultAuthenticationProvider",
			PasswordResetProviderID:          "Jellyfin.Server.Implementations.Users.DefaultPasswordResetProvider",
			SyncPlayAccess:                   "None",
		},
	}
}

type playState struct {
	CanSeek       bool   `json:"CanSeek"`
	IsPaused      bool   `json:"IsPaused"`
	IsMuted       bool   `json:"IsMuted"`
	RepeatMode    string `json:"RepeatMode"`
	PlaybackOrder string `json:"PlaybackOrder"`
}

type clientCapabilities struct {
	PlayableMediaTypes           []string `json:"PlayableMediaTypes"`
	SupportedCommands            []string `json:"SupportedCommands"`
	SupportsMediaControl         bool     `json:"SupportsMediaControl"`
	SupportsPersistentIdentifier bool     `json:"SupportsPersistentIdentifier"`
}

type sessionInfo struct {
	PlayState          playState          `json:"PlayState"`
	AdditionalUsers    []any              `json:"AdditionalUsers"`
	Capabilities       clientCapabilities `json:"Capabilities"`
	RemoteEndPoint     string             `json:"RemoteEndPoint"`
	PlayableMediaTypes []string           `json:"PlayableMediaTypes"`
	// SupportedCommands is empty: nothing here is remote-controllable. It is
	// still sent, because a client's model requires the field to be present.
	SupportedCommands        []string `json:"SupportedCommands"`
	ID                       string   `json:"Id"`
	UserID                   string   `json:"UserId"`
	UserName                 string   `json:"UserName"`
	Client                   string   `json:"Client"`
	LastActivityDate         string   `json:"LastActivityDate"`
	LastPlaybackCheckIn      string   `json:"LastPlaybackCheckIn"`
	DeviceName               string   `json:"DeviceName"`
	DeviceID                 string   `json:"DeviceId"`
	ApplicationVersion       string   `json:"ApplicationVersion"`
	IsActive                 bool     `json:"IsActive"`
	SupportsMediaControl     bool     `json:"SupportsMediaControl"`
	SupportsRemoteControl    bool     `json:"SupportsRemoteControl"`
	NowPlayingQueue          []any    `json:"NowPlayingQueue"`
	NowPlayingQueueFullItems []any    `json:"NowPlayingQueueFullItems"`
	HasCustomDeviceName      bool     `json:"HasCustomDeviceName"`
	ServerID                 string   `json:"ServerId"`
}

func (s *Server) sessionInfo(rq *request, stream *auth.Stream, client clientIdentity) sessionInfo {
	now := jellyfinTime(time.Now())
	h := sha1.Sum([]byte("streamnzb-session:" + stream.Username + ":" + client.DeviceID))
	return sessionInfo{
		PlayState:                playState{CanSeek: true, RepeatMode: "RepeatNone", PlaybackOrder: "Default"},
		AdditionalUsers:          []any{},
		Capabilities:             clientCapabilities{PlayableMediaTypes: []string{"Video"}, SupportedCommands: []string{}},
		RemoteEndPoint:           rq.RemoteAddr,
		PlayableMediaTypes:       []string{"Video"},
		SupportedCommands:        []string{},
		ID:                       hex.EncodeToString(h[:16]),
		UserID:                   userID(stream.Username),
		UserName:                 stream.Username,
		Client:                   client.Client,
		LastActivityDate:         now,
		LastPlaybackCheckIn:      now,
		DeviceName:               client.Device,
		DeviceID:                 client.DeviceID,
		ApplicationVersion:       client.Version,
		IsActive:                 true,
		NowPlayingQueue:          []any{},
		NowPlayingQueueFullItems: []any{},
		ServerID:                 s.serverID(),
	}
}

type authenticationResult struct {
	User        userDto     `json:"User"`
	SessionInfo sessionInfo `json:"SessionInfo"`
	AccessToken string      `json:"AccessToken"`
	ServerID    string      `json:"ServerId"`
}

// handleAuthenticateByName serves POST /Users/AuthenticateByName.
func (s *Server) handleAuthenticateByName(w http.ResponseWriter, rq *request) {
	var body struct {
		Username string `json:"Username"`
		Pw       string `json:"Pw"`
		Password string `json:"Password"`
	}
	raw, err := io.ReadAll(io.LimitReader(rq.Body, 64<<10))
	if err != nil || json.Unmarshal(raw, &body) != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}
	password := body.Pw
	if password == "" {
		password = body.Password
	}
	stream, ok := s.login(body.Username, password)
	if !ok {
		logger.Warn("Jellyfin login rejected", "username", body.Username, "remote", rq.RemoteAddr)
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	client := clientOf(rq)
	logger.Info("Jellyfin login", "stream", stream.Username, "client", client.Client, "device", client.Device)
	writeJSON(w, http.StatusOK, authenticationResult{
		User:        s.userDto(stream),
		SessionInfo: s.sessionInfo(rq, stream, client),
		AccessToken: stream.Token,
		ServerID:    s.serverID(),
	})
}

// serveUser answers the user and session routes of a signed-in client. The
// user id in the path is ignored: the token names the stream, and a client
// only ever asks about itself.
func (s *Server) serveUser(w http.ResponseWriter, rq *request) bool {
	switch {
	case rq.is(http.MethodGet) && len(rq.segs) == 2 && rq.segs[0] == "users":
		// /Users/Me and /Users/{id}; /Users/Public is answered before auth.
		writeJSON(w, http.StatusOK, s.userDto(rq.stream))
	case rq.is(http.MethodGet) && len(rq.segs) == 1 && rq.segs[0] == "users":
		writeJSON(w, http.StatusOK, []userDto{s.userDto(rq.stream)})
	case rq.is(http.MethodGet) && len(rq.segs) == 1 && rq.segs[0] == "sessions":
		writeJSON(w, http.StatusOK, []sessionInfo{s.sessionInfo(rq, rq.stream, clientOf(rq))})
	case rq.is(http.MethodPost) && len(rq.segs) == 2 && rq.segs[0] == "sessions" && rq.segs[1] == "logout":
		writeEmpty(w)
	case rq.is(http.MethodPost) && len(rq.segs) >= 2 && rq.segs[0] == "sessions" && rq.segs[1] == "capabilities":
		writeEmpty(w)
	case rq.is(http.MethodPost) && len(rq.segs) == 3 && rq.segs[0] == "users" && rq.segs[2] == "configuration":
		writeEmpty(w)
	default:
		return false
	}
	return true
}
