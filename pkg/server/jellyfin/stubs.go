package jellyfin

import (
	"net/http"
	"strings"
)

// The routes a client calls on its way to the ones that matter: the server
// handshake before login, and the settings, plugin and library-management
// lookups clients make on connect. Each is answered with the least a
// client accepts — an empty list, a default preference set, a version — so
// the client moves on rather than showing an error for a feature that does
// not exist here.

type publicSystemInfo struct {
	LocalAddress           string `json:"LocalAddress"`
	ServerName             string `json:"ServerName"`
	Version                string `json:"Version"`
	ProductName            string `json:"ProductName"`
	OperatingSystem        string `json:"OperatingSystem"`
	ID                     string `json:"Id"`
	StartupWizardCompleted bool   `json:"StartupWizardCompleted"`
}

func (s *Server) publicInfo(rq *request) publicSystemInfo {
	scheme := "http"
	if rq.TLS != nil || strings.EqualFold(rq.Header.Get("X-Forwarded-Proto"), "https") {
		scheme = "https"
	}
	return publicSystemInfo{
		LocalAddress:           scheme + "://" + rq.Host + strings.TrimSuffix(Mount, "/"),
		ServerName:             serverName,
		Version:                reportedVersion,
		ProductName:            "Jellyfin Server",
		OperatingSystem:        "",
		ID:                     s.serverID(),
		StartupWizardCompleted: true,
	}
}

// servePublic answers what a client asks before it has signed in.
func (s *Server) servePublic(w http.ResponseWriter, rq *request) bool {
	join := strings.Join(rq.segs, "/")
	switch {
	case join == "" && rq.is(http.MethodGet):
		// The bare mount, which is where Go's mux sends /jellyfin and what a
		// client fetches to check the URL the user typed is a server at all.
		// Real Jellyfin answers its root with the web app; there is none
		// here, so the handshake stands in for it. A 401 would read as a
		// wrong address and stop the client before it asked for anything.
		writeJSON(w, http.StatusOK, s.publicInfo(rq))
	case join == "system/info/public" && rq.is(http.MethodGet):
		writeJSON(w, http.StatusOK, s.publicInfo(rq))
	case join == "system/ping" && (rq.is(http.MethodGet) || rq.is(http.MethodPost)):
		writeJSON(w, http.StatusOK, "Jellyfin Server")
	case join == "branding/configuration" && rq.is(http.MethodGet):
		writeJSON(w, http.StatusOK, map[string]any{"LoginDisclaimer": "Sign in with a stream name and its token.", "CustomCss": "", "SplashscreenEnabled": false})
	case join == "branding/css" && rq.is(http.MethodGet):
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
		w.WriteHeader(http.StatusOK)
	case join == "quickconnect/enabled" && rq.is(http.MethodGet):
		writeJSON(w, http.StatusOK, false)
	case join == "users/public" && rq.is(http.MethodGet):
		writeJSON(w, http.StatusOK, []any{})
	case join == "users/authenticatebyname" && rq.is(http.MethodPost):
		s.handleAuthenticateByName(w, rq)
	default:
		return false
	}
	return true
}

// systemInfo is the signed-in /System/Info. Clients read the version, id
// and name; the paths are what Jellyfin reports for a container.
type systemInfo struct {
	publicSystemInfo
	OperatingSystemDisplayName string `json:"OperatingSystemDisplayName"`
	HasPendingRestart          bool   `json:"HasPendingRestart"`
	IsShuttingDown             bool   `json:"IsShuttingDown"`
	SupportsLibraryMonitor     bool   `json:"SupportsLibraryMonitor"`
	WebSocketPortNumber        int    `json:"WebSocketPortNumber"`
	CompletedInstallations     []any  `json:"CompletedInstallations"`
	CanSelfRestart             bool   `json:"CanSelfRestart"`
	CanLaunchWebBrowser        bool   `json:"CanLaunchWebBrowser"`
	ProgramDataPath            string `json:"ProgramDataPath"`
	WebPath                    string `json:"WebPath"`
	ItemsByNamePath            string `json:"ItemsByNamePath"`
	CachePath                  string `json:"CachePath"`
	LogPath                    string `json:"LogPath"`
	InternalMetadataPath       string `json:"InternalMetadataPath"`
	TranscodingTempPath        string `json:"TranscodingTempPath"`
	HasUpdateAvailable         bool   `json:"HasUpdateAvailable"`
	EncoderLocation            string `json:"EncoderLocation"`
	SystemArchitecture         string `json:"SystemArchitecture"`
	PackageName                string `json:"PackageName"`
	CastReceiverApplications   []any  `json:"CastReceiverApplications"`
}

func (s *Server) serveStubs(w http.ResponseWriter, rq *request) bool {
	join := strings.Join(rq.segs, "/")
	if !rq.is(http.MethodGet) {
		switch {
		case join == "displaypreferences/usersettings" || (len(rq.segs) == 2 && rq.segs[0] == "displaypreferences"):
			writeEmpty(w)
		case len(rq.segs) >= 1 && rq.segs[0] == "sessions":
			// Remote-control commands, viewing reports, capabilities.
			writeEmpty(w)
		default:
			return false
		}
		return true
	}
	switch {
	case join == "system/info":
		info := systemInfo{publicSystemInfo: s.publicInfo(rq), CompletedInstallations: []any{}, CastReceiverApplications: []any{}, SystemArchitecture: "X64", PackageName: "streamnzb " + s.opts.Version}
		info.OperatingSystemDisplayName = "StreamNZB"
		writeJSON(w, http.StatusOK, info)
	case join == "system/endpoint":
		writeJSON(w, http.StatusOK, map[string]bool{"IsLocal": false, "IsInNetwork": true})
	case join == "system/configuration":
		writeJSON(w, http.StatusOK, map[string]any{"EnableMetrics": false, "ServerName": serverName})
	case len(rq.segs) == 2 && rq.segs[0] == "displaypreferences":
		writeJSON(w, http.StatusOK, displayPreferences(rq.segs[1], rq.param("client")))
	case len(rq.segs) >= 1 && rq.segs[0] == "localization":
		writeJSON(w, http.StatusOK, []any{})
	case join == "plugins" || join == "scheduledtasks" || join == "packages" || join == "repositories" ||
		join == "library/virtualfolders" || join == "devices" || join == "system/activitylog/entries" ||
		join == "notifications/types" || join == "channels" || join == "livetv/channels" || join == "livetv/programs" ||
		join == "livetv/recordings" || join == "livetv/timers" || join == "livetv/seriestimers":
		writeJSON(w, http.StatusOK, []any{})
	case join == "livetv/info":
		writeJSON(w, http.StatusOK, map[string]any{"Services": []any{}, "IsEnabled": false, "EnabledUsers": []any{}})
	case join == "genres" || join == "studios" || join == "persons" || join == "years" ||
		join == "musicgenres" || join == "artists" || join == "artists/albumartists" ||
		join == "channels/features" || join == "livetv/channels/items":
		writeJSON(w, http.StatusOK, emptyResult())
	case join == "playback/bitratetest":
		// The client times a download to size its bitrate; a small payload
		// tells it the link is fast, which is what a direct-play server
		// wants it to think.
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(make([]byte, min(max(rq.intParam("size", 102400), 1), 1<<20)))
	case join == "socket":
		// No websocket: clients that want one retry quietly.
		http.NotFound(w, rq.Request)
	default:
		return false
	}
	return true
}

// displayPreferences is the per-client view settings object every client
// loads on start. Defaults only; nothing here is saved.
func displayPreferences(id, client string) map[string]any {
	return map[string]any{
		"Id":                 id,
		"ViewType":           "",
		"SortBy":             "SortName",
		"IndexBy":            nil,
		"RememberIndexing":   false,
		"PrimaryImageHeight": 250,
		"PrimaryImageWidth":  250,
		"CustomPrefs":        map[string]string{},
		"ScrollDirection":    "Horizontal",
		"ShowBackdrop":       true,
		"RememberSorting":    false,
		"SortOrder":          "Ascending",
		"ShowSidebar":        false,
		"Client":             client,
	}
}
