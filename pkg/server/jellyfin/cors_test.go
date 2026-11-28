package jellyfin

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"streamnzb/pkg/auth"
	"streamnzb/pkg/server/stremio"
)

const corsOrigin = "https://test.example"

// corsToken is the credential a browser client sends, in the header form
// the AIOStreams-style web clients use.
var corsToken = []string{"Authorization", `MediaBrowser Client="Web", Token="` + testToken + `"`}

// mediaCatalog serves play requests the way the real addon does: a media
// response that already sets Access-Control-Allow-Origin itself.
type mediaCatalog struct{ *fakeCatalog }

func (c mediaCatalog) ServePlay(w http.ResponseWriter, r *http.Request, s *auth.Stream, slotPath string, opts stremio.PlayServeOptions) {
	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	c.fakeCatalog.ServePlay(w, r, s, slotPath, opts)
}

func TestCORSPreflightIsAnsweredBeforeAuthentication(t *testing.T) {
	cases := []struct {
		name, path, requestMethod, requestHeaders string
	}{
		{"reporter's request", "/jellyfin/System/Info/Public", "GET", "authorization,content-type"},
		{"protected route", "/jellyfin/UserViews", "GET", "authorization"},
		{"login POST with legacy headers", "/jellyfin/Users/AuthenticateByName", "POST", "content-type, x-emby-authorization, x-mediabrowser-token, x-emby-token"},
		{"no requested headers", "/jellyfin/Items", "DELETE", ""},
		{"unknown route", "/jellyfin/Nope", "GET", "authorization"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture()
			// "X-None" suppresses the fixture's default credentials: a
			// preflight never carries any.
			headers := []string{"X-None", "", "Origin", corsOrigin, "Access-Control-Request-Method", c.requestMethod}
			if c.requestHeaders != "" {
				headers = append(headers, "Access-Control-Request-Headers", c.requestHeaders)
			}
			rec := f.do(http.MethodOptions, c.path, "", headers...)
			if rec.Code != http.StatusNoContent || rec.Body.Len() != 0 {
				t.Fatalf("preflight: %d %q", rec.Code, rec.Body.String())
			}
			h := rec.Header()
			if h.Get("Access-Control-Allow-Origin") != "*" {
				t.Fatalf("allow-origin = %q", h.Get("Access-Control-Allow-Origin"))
			}
			if got := h.Get("Access-Control-Allow-Methods"); !strings.Contains(got, c.requestMethod) || !strings.Contains(got, "OPTIONS") {
				t.Fatalf("allow-methods = %q", got)
			}
			if got := h.Get("Access-Control-Allow-Headers"); got != c.requestHeaders {
				t.Fatalf("allow-headers = %q, want %q", got, c.requestHeaders)
			}
			for _, name := range []string{"Access-Control-Allow-Credentials", "Access-Control-Max-Age", "Access-Control-Expose-Headers"} {
				if _, ok := h[name]; ok {
					t.Fatalf("%s must not be set, got %q", name, h.Get(name))
				}
			}
		})
	}
}

// A Vary a response already carries is added to, not replaced.
func TestCORSPreflightKeepsExistingVary(t *testing.T) {
	f := newFixture()
	req := httptest.NewRequest(http.MethodOptions, "/jellyfin/UserViews", nil)
	req.Header.Set("Origin", corsOrigin)
	req.Header.Set("Access-Control-Request-Method", "GET")
	rec := httptest.NewRecorder()
	rec.Header().Set("Vary", "Accept-Encoding")
	f.server.Handler().ServeHTTP(rec, req)
	if vary := corsTokens(rec.Header().Values("Vary")); !vary["accept-encoding"] || !vary["origin"] {
		t.Fatalf("Vary = %q", rec.Header().Values("Vary"))
	}
}

// Actual responses — successes and errors alike — carry the origin header, or
// a browser reports them as network errors it cannot read.
func TestCORSHeaderOnActualResponses(t *testing.T) {
	login := `{"Username":"Living-Room","Pw":"` + testToken + `"}`
	bad := `{"Username":"Living-Room","Pw":"wrong"}`
	cases := []struct {
		name, method, path, body string
		headers                  []string
		want                     int
	}{
		{"public GET", "GET", "/jellyfin/System/Info/Public", "", []string{"X-None", ""}, 200},
		{"login success", "POST", "/jellyfin/Users/AuthenticateByName", login, []string{"X-None", ""}, 200},
		{"login failure", "POST", "/jellyfin/Users/AuthenticateByName", bad, []string{"X-None", ""}, 401},
		{"protected without credentials", "GET", "/jellyfin/UserViews", "", []string{"X-None", ""}, 401},
		{"protected with credentials", "GET", "/jellyfin/UserViews", "", corsToken, 200},
		{"unknown route", "GET", "/jellyfin/Nope", "", corsToken, 404},
		// Not a preflight, so these keep the status they always had.
		{"OPTIONS without Origin", "OPTIONS", "/jellyfin/UserViews", "", []string{"X-None", "", "Access-Control-Request-Method", "GET"}, 401},
		{"OPTIONS without request method", "OPTIONS", "/jellyfin/UserViews", "", []string{"X-None", "", "Origin", corsOrigin}, 401},
		{"authenticated OPTIONS without request method", "OPTIONS", "/jellyfin/UserViews", "", append([]string{"Origin", corsOrigin}, corsToken...), 404},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture()
			headers := c.headers
			if !strings.Contains(c.name, "without Origin") {
				headers = append([]string{"Origin", corsOrigin}, headers...)
			}
			rec := f.do(c.method, c.path, c.body, headers...)
			if rec.Code != c.want {
				t.Fatalf("status %d, want %d: %s", rec.Code, c.want, rec.Body.String())
			}
			if got := rec.Header().Values("Access-Control-Allow-Origin"); len(got) != 1 || got[0] != "*" {
				t.Fatalf("allow-origin = %q", got)
			}
			if c.method == "OPTIONS" && rec.Header().Get("Access-Control-Allow-Methods") != "" {
				t.Fatalf("a non-preflight OPTIONS was answered as a preflight")
			}
		})
	}
}

// A native client sends no Origin and gets the same answers it always did.
func TestCORSLeavesNativeClientsAlone(t *testing.T) {
	f := newFixture()
	var info publicSystemInfo
	decodeInto(t, f.do(http.MethodGet, "/jellyfin/System/Info/Public", "", "X-None", ""), &info)
	if info.ID != "srv-0001" || info.ServerName != serverName {
		t.Fatalf("public info: %+v", info)
	}
	if rec := f.do(http.MethodGet, "/jellyfin/UserViews", ""); rec.Code != http.StatusOK {
		t.Fatalf("authenticated: %d", rec.Code)
	}
	if rec := f.do(http.MethodGet, "/jellyfin/UserViews", "", "X-None", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated: %d", rec.Code)
	}
}

// The stream route sets its own origin header; the layer's must not stack a
// second value on it, which browsers reject.
func TestCORSDoesNotDuplicateMediaOriginHeader(t *testing.T) {
	f := newFixture()
	f.server.opts.Catalog = mediaCatalog{f.catalog}
	movie, _ := itemIDFor("movie", "tt0111161")
	rec := f.do(http.MethodGet, "/jellyfin/Videos/"+movie.encode()+"/stream?static=true&mediaSourceId="+movie.source(0).encode()+"&api_key="+testToken, "", "Origin", corsOrigin, "X-None", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("stream: %d", rec.Code)
	}
	if got := rec.Header().Values("Access-Control-Allow-Origin"); len(got) != 1 || got[0] != "*" {
		t.Fatalf("allow-origin = %q", got)
	}
	if rec.Header().Get("Content-Type") != "video/mp4" || rec.Header().Get("Accept-Ranges") != "bytes" {
		t.Fatalf("media headers lost: %v", rec.Header())
	}
}

// A client may send the requested headers as several fields; all of them are
// permitted, not just the first.
func TestCORSPreflightReflectsRepeatedRequestHeaderFields(t *testing.T) {
	f := newFixture()
	req := httptest.NewRequest(http.MethodOptions, "/jellyfin/UserViews", nil)
	req.Header.Set("Origin", corsOrigin)
	req.Header.Set("Access-Control-Request-Method", "GET")
	req.Header.Add("Access-Control-Request-Headers", "content-type")
	req.Header.Add("Access-Control-Request-Headers", "authorization")
	rec := httptest.NewRecorder()
	f.server.Handler().ServeHTTP(rec, req)
	allowed := corsTokens(rec.Header().Values("Access-Control-Allow-Headers"))
	if rec.Code != http.StatusNoContent || !allowed["content-type"] || !allowed["authorization"] {
		t.Fatalf("status %d, allow-headers = %q", rec.Code, rec.Header().Values("Access-Control-Allow-Headers"))
	}
}

// corsTokens splits comma-separated header values into lower-cased tokens.
func corsTokens(values []string) map[string]bool {
	out := map[string]bool{}
	for _, v := range values {
		for _, tok := range strings.Split(v, ",") {
			if tok = strings.ToLower(strings.TrimSpace(tok)); tok != "" {
				out[tok] = true
			}
		}
	}
	return out
}

// Only OPTIONS answers depend on the preflight request headers, so only they
// vary on them.
func TestCORSVaryOnlyOnOptions(t *testing.T) {
	cors := []string{"origin", "access-control-request-method", "access-control-request-headers"}
	cases := []struct {
		name, method, path string
		headers            []string
		wantVary           bool
	}{
		{"preflight", "OPTIONS", "/jellyfin/UserViews", []string{"X-None", "", "Origin", corsOrigin, "Access-Control-Request-Method", "GET"}, true},
		{"OPTIONS without Origin", "OPTIONS", "/jellyfin/UserViews", []string{"X-None", "", "Access-Control-Request-Method", "GET"}, true},
		{"OPTIONS without request method", "OPTIONS", "/jellyfin/UserViews", []string{"X-None", "", "Origin", corsOrigin}, true},
		{"GET", "GET", "/jellyfin/System/Info/Public", []string{"X-None", "", "Origin", corsOrigin}, false},
		{"POST", "POST", "/jellyfin/Users/AuthenticateByName", []string{"X-None", "", "Origin", corsOrigin}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := newFixture().do(c.method, c.path, "", c.headers...)
			got := corsTokens(rec.Header().Values("Vary"))
			for _, name := range cors {
				if got[name] != c.wantVary {
					t.Fatalf("Vary %q: has %s = %v, want %v", rec.Header().Values("Vary"), name, got[name], c.wantVary)
				}
			}
		})
	}
}
