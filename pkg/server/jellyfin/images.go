package jellyfin

import (
	"crypto/sha1"
	"encoding/hex"
	"net/http"
	"strings"
	"sync"

	"streamnzb/pkg/core/logger"
)

// Images are never proxied. Jellyfin clients build image URLs from an item's
// ImageTags and fetch /Items/{id}/Images/{type}?tag=..., and the answer here
// is a redirect to the provider's CDN — the same URL the Stremio meta hands
// out. The tag is a hash of that URL, so the layer can answer a tag it has
// seen without touching metadata, and one it has not by resolving the item
// again: the map is a fast path, not a source of truth.

const imageTagCap = 50_000

type imageTags struct {
	mu   sync.Mutex
	urls map[string]string
}

func newImageTags() *imageTags {
	return &imageTags{urls: make(map[string]string)}
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
	if tag := rq.param("tag"); tag != "" {
		if url, ok := s.images.urlFor(tag); ok {
			http.Redirect(w, rq.Request, url, http.StatusFound)
			return true
		}
	}
	url := s.resolveImage(rq, rawID, kind)
	if url == "" {
		http.NotFound(w, rq.Request)
		return true
	}
	http.Redirect(w, rq.Request, url, http.StatusFound)
	return true
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
