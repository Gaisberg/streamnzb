package jellyfin

import (
	"bytes"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"streamnzb/pkg/core/logger"
)

// Images are relayed, not redirected. Jellyfin clients build image URLs from
// an item's ImageTags and fetch /Items/{id}/Images/{type}?tag=..., and the
// answer here fetches the provider's CDN — the same URL the Stremio meta
// hands out — and streams the bytes back, because some clients (Infuse on
// tvOS/macOS) do not follow redirects for artwork and are left with a blank
// image on a 302. The tag is a hash of the upstream URL, so the layer can
// answer a tag it has seen without touching metadata, and one it has not by
// resolving the item again: the map is a fast path, not a source of truth.

const imageTagCap = 50_000

type imageTags struct {
	mu sync.Mutex
	// urls maps a tag to the URL it was minted from.
	urls map[string]string
	// byItem maps an item and image kind to the same URL, for clients that
	// ask for an image without quoting a tag. Fladder is one: its URL builder
	// passes the tag for Backdrop but not for Primary, Thumb or Logo, so
	// every poster it asks for arrives with nothing to look up.
	byItem map[string]string
}

func newImageTags() *imageTags {
	return &imageTags{urls: make(map[string]string), byItem: make(map[string]string)}
}

// tagFor is the tag of a URL; "" for no URL.
func (t *imageTags) tagFor(url string) string {
	url = strings.TrimSpace(url)
	if url == "" {
		return ""
	}
	h := sha1.Sum([]byte(url))
	return hex.EncodeToString(h[:10])
}

// register remembers a URL under its tag and returns the tag.
func (t *imageTags) register(url string) string {
	tag := t.tagFor(url)
	if tag == "" {
		return ""
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.urls) >= imageTagCap {
		// A full map is dropped rather than evicted: misses re-resolve, and
		// a client only ever asks for tags it was just handed.
		t.urls = make(map[string]string)
	}
	t.urls[tag] = strings.TrimSpace(url)
	return tag
}

func (t *imageTags) urlFor(tag string) (string, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	url, ok := t.urls[tag]
	return url, ok
}

func itemImageKey(itemID, kind string) string {
	return itemID + "/" + strings.ToLower(strings.TrimSpace(kind))
}

// registerFor remembers a URL under its tag and under the item and kind it
// belongs to, and returns the tag. The second index is what lets a tagless
// request be answered: it needs no credentials and no metadata lookup, unlike
// resolveImage, because the item's own document already named this URL.
func (t *imageTags) registerFor(itemID, kind, url string) string {
	tag := t.register(url)
	url = strings.TrimSpace(url)
	if tag == "" || itemID == "" || kind == "" || url == "" {
		return tag
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.byItem) >= imageTagCap {
		// Dropped rather than evicted, exactly as urls is: a miss re-resolves.
		t.byItem = make(map[string]string)
	}
	t.byItem[itemImageKey(itemID, kind)] = url
	return tag
}

func (t *imageTags) urlForItem(itemID, kind string) (string, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	url, ok := t.byItem[itemImageKey(itemID, kind)]
	return url, ok
}

// serveImages answers /Items/{id}/Images/{type}[/{index}].
func (s *Server) serveImages(w http.ResponseWriter, rq *request) bool {
	if !rq.is(http.MethodGet) && !rq.is(http.MethodHead) {
		return false
	}
	var rawID, kind string
	if c, ok := rq.match("items", "*", "images", "*"); ok {
		rawID, kind = c[0], c[1]
	} else if c, ok := rq.match("items", "*", "images", "*", "*"); ok {
		rawID, kind = c[0], c[1]
	} else if _, ok := rq.match("users", "*", "images", "*"); ok {
		http.NotFound(w, rq.Request)
		return true
	} else {
		return false
	}
	fallbacks := s.imageFallbacks(rawID, kind)
	if tag := rq.param("tag"); tag != "" {
		if url, ok := s.images.urlFor(tag); ok {
			s.relayImage(w, rq, kind, append([]string{url}, fallbacks...)...)
			return true
		}
	}
	// No tag, or one this process never minted: answer from what the item's
	// own document advertised. Clients are not consistent about quoting tags —
	// Fladder sends one for Backdrop and omits it for Primary, Thumb and Logo
	// — and the resolve path below cannot help them, because an image is
	// fetched by an image loader that sends no credentials.
	if url, ok := s.itemImageURL(rawID, kind); ok {
		s.relayImage(w, rq, kind, append([]string{url}, fallbacks...)...)
		return true
	}
	url := s.resolveImage(rq, rawID, kind)
	if url == "" {
		if len(fallbacks) == 0 {
			http.NotFound(w, rq.Request)
			return true
		}
		s.relayImage(w, rq, kind, fallbacks...)
		return true
	}
	s.relayImage(w, rq, kind, append([]string{url}, fallbacks...)...)
	return true
}

// posterFallbackURL is where a poster comes from when the advertised one
// cannot be fetched. The advertised URL may be a profile's poster overlay
// (poster_url_pattern), which replaces the provider's poster before this layer
// sees it, so the original is not available to fall back on; Metahub serves a
// poster for any IMDb id without credentials — the same CDN the meta builder
// already uses for logos — and is the one poster source reachable from an id
// alone. A format string taking the IMDb id; a variable so tests can point it
// at a local server.
var posterFallbackURL = "https://images.metahub.space/poster/medium/%s/img"

// imageFallbacks lists the URLs to try when an item's advertised image cannot
// be relayed: today only a Metahub poster for the Primary image of an
// IMDb-keyed movie or series. Episodes are left out — their Primary is a
// still, and a series poster in its place would be wrong rather than missing.
func (s *Server) imageFallbacks(rawID, kind string) []string {
	if strings.ToLower(strings.TrimSpace(kind)) != "primary" {
		return nil
	}
	id, err := decodeItemID(rawID)
	if err != nil || id.Scheme != schemeIMDb || (id.Kind != kindMovie && id.Kind != kindSeries) {
		return nil
	}
	return []string{fmt.Sprintf(posterFallbackURL, id.baseStremioID())}
}

// itemImageURL is the URL this item's document advertised for a kind, if one
// was rendered by this process. The id is normalised through decodeItemID
// first: clients echo item ids back in whatever casing and hyphenation they
// like, and the index is keyed by the canonical form.
func (s *Server) itemImageURL(rawID, kind string) (string, bool) {
	id, err := decodeItemID(rawID)
	if err != nil {
		return "", false
	}
	return s.images.urlForItem(id.encode(), kind)
}

// resolveImage finds an item's image of a kind from its metadata, for a tag
// the map no longer holds (restart, eviction, another instance).
func (s *Server) resolveImage(rq *request, rawID, kind string) string {
	id, err := decodeItemID(rawID)
	if err != nil || id.Kind == kindView {
		return ""
	}
	// Resolving reads metadata, which is per-stream. An image request that
	// carried no token has the tag path and nothing else.
	if rq.stream == nil {
		return ""
	}
	meta, err := s.opts.Catalog.Meta(rq.Context(), rq.stream, id.ContentType, id.baseStremioID())
	if err != nil || meta == nil {
		logger.Debug("Jellyfin image resolve failed", "item", rawID, "err", err)
		return ""
	}
	switch kind {
	case "primary", "thumb":
		if id.Kind == kindEpisode {
			for _, v := range videosOf(meta) {
				if v.Season == id.Season && v.Episode == id.Episode && v.Thumbnail != "" {
					return v.Thumbnail
				}
			}
		}
		return meta.Poster
	case "backdrop":
		return meta.Background
	case "logo":
		return meta.Logo
	}
	return ""
}

// imageClient fetches provider images to relay. The dial, handshake and
// response-header timeouts are short so a dead CDN fails fast; the overall
// timeout is longer to cover a large backdrop over a slow upstream.
var imageClient = &http.Client{
	Timeout: 30 * time.Second,
	Transport: &http.Transport{
		DialContext:           (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: 10 * time.Second,
		MaxIdleConnsPerHost:   8,
	},
}

// maxImageBody caps a relayed image, upstream Content-Length and all: this
// layer never has to hold more than a poster in flight, and a CDN promising
// more is answered with a 502 rather than trusted to actually send that much.
const maxImageBody = 20 << 20 // 20 MiB

// relayableImageTypes are the inert raster formats a poster or backdrop can
// legitimately be. Anything outside this set is refused rather than relayed:
// the relay serves the result from this server's origin, so the upstream must
// not get to choose a type the browser will execute.
var relayableImageTypes = map[string]bool{
	"image/jpeg":               true,
	"image/png":                true,
	"image/webp":               true,
	"image/gif":                true,
	"image/avif":               true,
	"image/bmp":                true,
	"image/tiff":               true,
	"image/x-icon":             true,
	"image/vnd.microsoft.icon": true,
}

// relayableImageType normalises an upstream Content-Type and reports whether
// it may be relayed. An absent header is treated as image/jpeg, the same
// assumption the relay has always made for CDNs that omit it.
func relayableImageType(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "image/jpeg", true
	}
	mediaType, _, err := mime.ParseMediaType(raw)
	if err != nil {
		return "", false
	}
	mediaType = strings.ToLower(mediaType)
	if !relayableImageTypes[mediaType] {
		return "", false
	}
	return mediaType, true
}

// relayOutcome is what one attempt to relay a URL came to.
type relayOutcome int

const (
	// relayServed: the response has been written; nothing more to do.
	relayServed relayOutcome = iota
	// relayMissing: the upstream has no such image (404, or a URL this layer
	// cannot fetch). The next candidate may.
	relayMissing
	// relayFailed: the upstream misbehaved — unreachable, an error status, a
	// body past the cap, a type that is not an image. The next candidate may
	// still succeed, but if none does the client is told the gateway failed.
	relayFailed
)

// relayImage fetches the first of urls that answers and streams it back as
// the response, instead of redirecting the client to it: Infuse and some
// other clients ignore a 302 on an artwork request and are left with a blank
// image. Later urls are fallbacks, tried only when an earlier one is missing
// or fails; when none serves, the client gets a 502 if any upstream failed
// and a 404 when every one of them simply had no image.
func (s *Server) relayImage(w http.ResponseWriter, rq *request, kind string, urls ...string) {
	failed := false
	for _, rawURL := range urls {
		if rq.Context().Err() != nil {
			// The client went away; no fallback is worth fetching for it.
			return
		}
		switch s.relayOnce(w, rq, rawURL, kind) {
		case relayServed:
			return
		case relayFailed:
			failed = true
		}
	}
	if failed {
		http.Error(w, "bad gateway", http.StatusBadGateway)
		return
	}
	http.NotFound(w, rq.Request)
}

// relayOnce attempts to relay one URL. It writes to w only when it serves the
// image, so a failed attempt leaves the response untouched for the next.
func (s *Server) relayOnce(w http.ResponseWriter, rq *request, rawURL, kind string) relayOutcome {
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		// Nothing here to fetch on the client's behalf, and a redirect is
		// exactly what this relay exists to avoid: the clients that need
		// relaying would not follow it either.
		logger.Debug("Jellyfin image relay skipped", "url", rawURL, "reason", "not an http(s) url")
		return relayMissing
	}
	target := rewriteImageSize(rawURL, kind)
	// A GET is sent even for a HEAD request: some CDNs answer HEAD with the
	// wrong content type, or refuse it outright.
	req, err := http.NewRequestWithContext(rq.Context(), http.MethodGet, target, nil)
	if err != nil {
		logger.Debug("Jellyfin image relay failed", "url", target, "err", err)
		return relayFailed
	}
	resp, err := imageClient.Do(req)
	if err != nil {
		// Including a request the client abandoned: its context is what was
		// passed above, so the fetch dies with it instead of running to
		// completion for no one.
		logger.Debug("Jellyfin image relay failed", "url", target, "err", err)
		return relayFailed
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		logger.Debug("Jellyfin image relay missing", "url", target)
		return relayMissing
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		logger.Debug("Jellyfin image relay failed", "url", target, "status", resp.StatusCode)
		return relayFailed
	}
	length := int64(-1)
	if cl := resp.Header.Get("Content-Length"); cl != "" {
		if n, err := strconv.ParseInt(cl, 10, 64); err == nil {
			length = n
		}
	}
	if length > maxImageBody {
		// Refused before a header is written: a Content-Length promising
		// more than the cap would otherwise reach the client alongside a
		// body truncated well short of it.
		logger.Debug("Jellyfin image relay refused", "url", target, "length", length)
		return relayFailed
	}
	ct, ok := relayableImageType(resp.Header.Get("Content-Type"))
	if !ok {
		// Whatever this is, it is not a poster. Relaying it would put a
		// document of the upstream's choosing on this server's own origin,
		// which is exactly what an image relay must not do — image/svg+xml
		// carries script, text/html plainly so.
		logger.Debug("Jellyfin image relay refused", "url", target, "type", resp.Header.Get("Content-Type"))
		return relayFailed
	}
	var body io.Reader = resp.Body
	if length < 0 {
		// No Content-Length: the cap can only be enforced by reading. Read to
		// the cap plus one byte before any header is written, so an oversized
		// body is refused outright instead of cut off mid-image behind a 200 —
		// a client cannot tell a truncated JPEG from a whole one. Bounded by
		// the cap, and rare: CDNs send a length for a static file.
		buf, err := io.ReadAll(io.LimitReader(resp.Body, maxImageBody+1))
		if err != nil {
			logger.Debug("Jellyfin image relay failed", "url", target, "err", err)
			return relayFailed
		}
		if len(buf) > maxImageBody {
			logger.Debug("Jellyfin image relay refused", "url", target, "length", ">"+strconv.Itoa(maxImageBody))
			return relayFailed
		}
		length = int64(len(buf))
		body = bytes.NewReader(buf)
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Content-Length", strconv.FormatInt(length, 10))
	if etag := resp.Header.Get("ETag"); etag != "" {
		w.Header().Set("ETag", etag)
	}
	if lm := resp.Header.Get("Last-Modified"); lm != "" {
		w.Header().Set("Last-Modified", lm)
	}
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.WriteHeader(http.StatusOK)
	if rq.Method == http.MethodHead {
		return relayServed
	}
	// A declared length is enforced by net/http on the upstream side: the body
	// ends at Content-Length whatever the CDN sends after it.
	io.Copy(w, body) // write errors ignored: the client went away
	return relayServed
}

// rewriteImageSize points a TMDB image at a size proportional to how large
// clients render it, rather than the "original" full-resolution file the
// catalog hands out: relaying a multi-megabyte poster for a thumbnail wastes
// bandwidth neither side needs. Any other host is passed through unchanged.
// "person" is a cast headshot — there is no /Items/{personId}/Images/Primary
// request to carry a kind, so a person photo is sized once, at registration,
// rather than by the request that later relays it.
func rewriteImageSize(rawURL, kind string) string {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host != "image.tmdb.org" {
		return rawURL
	}
	segs := strings.Split(strings.TrimPrefix(u.Path, "/"), "/")
	if len(segs) != 4 || segs[0] != "t" || segs[1] != "p" {
		return rawURL
	}
	var size string
	switch kind {
	case "backdrop":
		size = "w1280"
	case "primary", "thumb":
		size = "w780"
	case "person":
		size = "h632"
	default:
		return rawURL
	}
	segs[2] = size
	u.Path = "/" + strings.Join(segs, "/")
	return u.String()
}
