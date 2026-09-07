package newznab

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/indexer"
)

// grabRef is what a rewritten download link stands for: which indexer owns the
// release and where that indexer serves the NZB.
type grabRef struct {
	Indexer string `json:"i"`
	URL     string `json:"u"`
	Title   string `json:"t,omitempty"`
	// Class is what the search that produced this link was after ("movie",
	// "tv", "tv_anime"), so the grab can present the same identity.
	Class string `json:"c,omitempty"`
}

var errBadGrabRef = errors.New("newznab: malformed download reference")

// grabKey derives the sealing key from a server secret. Deriving rather than
// using the secret directly keeps the admin token out of the cipher key, and
// ties every link's lifetime to that token: rotating it invalidates links
// handed out earlier, which is the behaviour you want from a credential.
func grabKey(secret string) []byte {
	sum := sha256.Sum256([]byte("streamnzb/newznab/grab\x00" + secret))
	return sum[:]
}

// sealGrabRef encrypts ref into an opaque, URL-safe id. Upstream download URLs
// carry that indexer's API key, so they are never handed to a client in the
// clear — not even base64'd.
func sealGrabRef(secret string, ref grabRef) (string, error) {
	payload, err := json.Marshal(ref)
	if err != nil {
		return "", err
	}
	gcm, err := grabCipher(secret)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	sealed := gcm.Seal(nonce, nonce, payload, nil)
	return base64.RawURLEncoding.EncodeToString(sealed), nil
}

// openGrabRef reverses sealGrabRef. A reference that fails to open — tampered
// with, or sealed under a since-rotated secret — is reported as missing rather
// than as an error the client could probe.
func openGrabRef(secret, id string) (grabRef, error) {
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(id))
	if err != nil {
		return grabRef{}, errBadGrabRef
	}
	gcm, err := grabCipher(secret)
	if err != nil {
		return grabRef{}, err
	}
	if len(raw) < gcm.NonceSize() {
		return grabRef{}, errBadGrabRef
	}
	payload, err := gcm.Open(nil, raw[:gcm.NonceSize()], raw[gcm.NonceSize():], nil)
	if err != nil {
		return grabRef{}, errBadGrabRef
	}
	var ref grabRef
	if err := json.Unmarshal(payload, &ref); err != nil || strings.TrimSpace(ref.URL) == "" {
		return grabRef{}, errBadGrabRef
	}
	return ref, nil
}

func grabCipher(secret string) (cipher.AEAD, error) {
	block, err := aes.NewCipher(grabKey(secret))
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// handleGet serves t=get: the NZB behind a sealed download reference, fetched
// through the indexer that published it so its grab quota and grab User-Agent
// still apply.
func (s *Server) handleGet(w http.ResponseWriter, r *http.Request, idx indexer.Indexer) {
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	if id == "" {
		s.writeError(w, r, http.StatusBadRequest, errMissingParameter, "Missing parameter (id)")
		return
	}
	ref, err := openGrabRef(s.grabSecret(), id)
	if err != nil {
		logger.Debug("Newznab grab rejected", "reason", "unresolvable id", "err", err)
		s.writeError(w, r, http.StatusNotFound, errNoSuchItem, "No such item")
		return
	}
	source := findIndexer(idx, ref.Indexer)
	if source == nil {
		logger.Debug("Newznab grab rejected", "reason", "indexer unavailable", "indexer", ref.Indexer)
		s.writeError(w, r, http.StatusNotFound, errNoSuchItem, "No such item")
		return
	}

	data, err := source.DownloadNZB(indexer.WithMediaClass(r.Context(), ref.Class), ref.URL)
	if err != nil {
		logger.Warn("Newznab grab failed", "indexer", ref.Indexer, "title", ref.Title, "err", err)
		s.writeError(w, r, http.StatusBadGateway, errUnknownError, "NZB download failed")
		return
	}
	logger.Info("Newznab grab", "indexer", ref.Indexer, "title", ref.Title, "bytes", len(data))

	w.Header().Set("Content-Type", "application/x-nzb")
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(data)))
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", nzbFilename(ref.Title)))
	if r.Method == http.MethodHead {
		return
	}
	if _, err := w.Write(data); err != nil {
		logger.Debug("Newznab grab write failed", "indexer", ref.Indexer, "err", err)
	}
}

// nzbFilename renders a release title as a safe attachment filename.
func nzbFilename(title string) string {
	name := strings.TrimSpace(title)
	if name == "" {
		name = "release"
	}
	name = strings.Map(func(r rune) rune {
		switch r {
		case '/', '\\', ':', '*', '?', '"', '<', '>', '|', '\r', '\n':
			return '_'
		}
		return r
	}, name)
	if len(name) > 180 {
		name = name[:180]
	}
	if !strings.HasSuffix(strings.ToLower(name), ".nzb") {
		name += ".nzb"
	}
	return name
}

// findIndexer resolves one indexer out of the fan-out by the name its results
// were tagged with.
func findIndexer(idx indexer.Indexer, name string) indexer.Indexer {
	name = strings.TrimSpace(name)
	if idx == nil || name == "" {
		return nil
	}
	agg, ok := idx.(*indexer.Aggregator)
	if !ok {
		if strings.EqualFold(idx.Name(), name) {
			return idx
		}
		return nil
	}
	for _, candidate := range agg.GetIndexers() {
		if strings.EqualFold(candidate.Name(), name) {
			return candidate
		}
	}
	return nil
}
