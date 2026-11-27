// Package sabnzbd serves a SABnzbd-compatible download-client API, so Radarr,
// Sonarr and anything else that drives SABnzbd can hand StreamNZB the NZBs it
// grabs. Nothing is downloaded to disk: each NZB is validated and stored in
// the release library the way a preload would store it, then reported as a
// completed download whose path is the file's place on the NFS export. A
// media server pointed at the export sees the file; the *arr sees a download
// client that finished.
//
// Only the slice of the SABnzbd API those clients use is implemented:
// version, get_config, queue, history, addfile, addurl and the delete forms
// of queue and history. A stream token is the API key — the same login the
// addon, the Jellyfin endpoint and the NFS export use — and the validation
// runs through that stream's providers and connection caps.
package sabnzbd

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	neturl "net/url"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"streamnzb/pkg/auth"
	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/core/persistence"
	"streamnzb/pkg/server/stremio"
)

const (
	// Mount is the path prefix the endpoint is served under; APIPath is the
	// endpoint itself. A client is configured with the host, the port and a
	// URL base of /sabnzbd, which every SABnzbd client has a field for.
	Mount   = "/sabnzbd/"
	APIPath = "/sabnzbd/api"

	// reportedVersion is what the version call answers. Clients gate
	// features on it; this is a recent release with nothing they test for
	// that the endpoint lacks.
	reportedVersion = "4.3.3"

	historyStateKey = "sabnzbd_history"
	maxHistory      = 500
	// maxQueue bounds what a runaway client can enqueue.
	maxQueue = 200

	// jobTimeout bounds one validation: an NZB download the *arr already
	// did, a STAT sweep, an article check and a forced decode of the head.
	jobTimeout = 30 * time.Minute

	addURLTimeout = 60 * time.Second
)

// Streams is the slice of the stream manager the endpoint needs.
type Streams interface {
	AuthenticateToken(token, adminUsername, adminToken string) (*auth.Stream, error)
}

// State persists the history across restarts. *persistence.StateManager
// satisfies it.
type State interface {
	Get(key string, target interface{}) (bool, error)
	Set(key string, value interface{}) error
}

// IngestFunc validates and stores one release for a stream: one row for a
// movie or episode, one per episode for a pack. *stremio.Server's
// IngestLibraryRelease is the implementation.
type IngestFunc func(ctx context.Context, stream *auth.Stream, req stremio.IngestRequest) ([]*persistence.LibraryItem, error)

// PublishFunc places a finished job on the NFS export as a folder named
// after the job holding the media file — SABnzbd's own layout — and returns
// that folder's path on the export. A false result fails the job.
type PublishFunc func(ctx context.Context, jobName string, item *persistence.LibraryItem) (string, bool)

type Options struct {
	// Enabled is read per request; nil means always on.
	Enabled func() bool
	Streams Streams
	// Admin returns the dashboard username and admin token, so the admin
	// identity can be refused as an API key: it binds no providers.
	Admin func() (username, token string)
	// CompleteDir is the directory the client is told downloads land in:
	// where the NFS export is mounted on the client's host. Read per job.
	CompleteDir   func() string
	Ingest        IngestFunc
	Publish       PublishFunc
	ExportEnabled func() bool
	State         State
	Version       string
}

type jobStatus string

const (
	statusQueued      jobStatus = "queued"
	statusDownloading jobStatus = "downloading"
	statusCompleted   jobStatus = "completed"
	statusFailed      jobStatus = "failed"
)

// job is one NZB handed in. The exported fields are what the history keeps.
type job struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Category    string    `json:"category"`
	Stream      string    `json:"stream"`
	Status      jobStatus `json:"status"`
	FailMessage string    `json:"fail_message,omitempty"`
	// Size is the release size as the NZB declares it; the stored media
	// file's exact size replaces it once known.
	Size      int64     `json:"size"`
	Added     time.Time `json:"added"`
	Started   time.Time `json:"started,omitempty"`
	Completed time.Time `json:"completed,omitempty"`
	// Storage is the completed path the client imports from.
	Storage string   `json:"storage,omitempty"`
	ItemIDs []string `json:"item_ids,omitempty"`

	nzb           []byte
	stream        *auth.Stream
	cancel        context.CancelFunc
	ctxForProcess context.Context
	// setsDone and setsTotal are the validation's progress: file sets done
	// so far, a set under way counting by the share of its articles the
	// census has answered for, out of the sets the release has. Zero total
	// until the ingest has parsed the NZB.
	setsDone  float64
	setsTotal int
}

// progress is the job's completed fraction in [0, 1). A job with no sets
// known yet is at zero; one that has validated every set but is still
// publishing sits just below done.
func (j *job) progress() float64 {
	if j == nil || j.Started.IsZero() || j.setsTotal <= 0 {
		return 0
	}
	p := j.setsDone / float64(j.setsTotal)
	if p >= 1 {
		return 0.99
	}
	if p < 0 {
		return 0
	}
	return p
}

type Server struct {
	opts Options

	mu      sync.Mutex
	queue   []*job // waiting, in order; the active job is queue[0] while Started is set
	history []*job // newest first
	wake    chan struct{}

	stopOnce sync.Once
	stopCh   chan struct{}
	ctx      context.Context
	cancel   context.CancelFunc
	wg       sync.WaitGroup
	started  bool
}

func New(opts Options) *Server {
	ctx, cancel := context.WithCancel(context.Background())
	s := &Server{opts: opts, wake: make(chan struct{}, 1), stopCh: make(chan struct{}), ctx: ctx, cancel: cancel}
	s.loadHistory()
	return s
}

// Start begins working the queue. Safe to call once.
func (s *Server) Start() {
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return
	}
	s.started = true
	s.mu.Unlock()
	s.wg.Add(1)
	go s.worker()
}

// Stop ends the worker. A job mid-validation is cancelled and returned to
// the queue's memory as failed, so the client is told rather than left with
// a download that silently vanished.
func (s *Server) Stop() {
	s.stopOnce.Do(func() {
		close(s.stopCh)
		s.cancel()
		s.wg.Wait()
		s.mu.Lock()
		for _, j := range s.queue {
			j.Status = statusFailed
			j.FailMessage = "StreamNZB stopped before this was processed"
			j.Completed = time.Now()
			s.history = append([]*job{j}, s.history...)
		}
		s.queue = nil
		s.mu.Unlock()
		s.saveHistory()
	})
}

func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(s.serve)
}

func (s *Server) enabled() bool {
	return s.opts.Enabled == nil || s.opts.Enabled()
}

// --- HTTP --------------------------------------------------------------

type apiError struct {
	Status bool   `json:"status"`
	Error  string `json:"error"`
}

func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(v)
}

// writeError answers the way SABnzbd does: HTTP 200 with status false. The
// clients read the body, not the status code, and SABnzbd's own wording is
// what they match on for a bad key.
func writeError(w http.ResponseWriter, msg string) {
	writeJSON(w, apiError{Status: false, Error: msg})
}

func isAPIPath(p string) bool {
	rest := strings.Trim(strings.TrimPrefix(p, strings.TrimSuffix(Mount, "/")), "/")
	return rest == "" || rest == "api"
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	if !s.enabled() || !isAPIPath(r.URL.Path) {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if r.Method == http.MethodPost && strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			writeError(w, "could not read upload: "+err.Error())
			return
		}
		defer func() {
			if r.MultipartForm != nil {
				_ = r.MultipartForm.RemoveAll()
			}
		}()
	} else if r.Method == http.MethodPost {
		_ = r.ParseForm()
	}
	param := func(name string) string { return strings.TrimSpace(r.FormValue(name)) }

	mode := strings.ToLower(param("mode"))
	if mode == "auth" {
		writeJSON(w, map[string]string{"auth": "apikey"})
		return
	}
	stream, err := s.authenticate(param("apikey"))
	if err != nil {
		logger.Warn("SABnzbd API request rejected", "reason", err.Error(), "remote", r.RemoteAddr)
		writeError(w, "API Key Incorrect")
		return
	}

	switch mode {
	case "version":
		writeJSON(w, map[string]string{"version": reportedVersion})
	case "get_config":
		s.handleGetConfig(w)
	case "fullstatus", "status":
		s.handleFullStatus(w)
	case "queue":
		if strings.EqualFold(param("name"), "delete") {
			s.handleDelete(w, param("value"), false)
			return
		}
		s.handleQueue(w, stream)
	case "history":
		if strings.EqualFold(param("name"), "delete") {
			s.handleDelete(w, param("value"), true)
			return
		}
		s.handleHistory(w, stream)
	case "addfile":
		s.handleAddFile(w, r, stream, param)
	case "addurl":
		s.handleAddURL(w, r, stream, param)
	case "":
		writeError(w, "expect one parameter")
	default:
		writeError(w, "not implemented")
	}
}

var errUnauthorized = errors.New("invalid API key")

// authenticate resolves the apikey parameter to a stream. The admin token is
// refused: the admin binds no providers to validate through.
func (s *Server) authenticate(apikey string) (*auth.Stream, error) {
	if apikey == "" || s.opts.Streams == nil {
		return nil, errUnauthorized
	}
	adminUser, adminToken := "", ""
	if s.opts.Admin != nil {
		adminUser, adminToken = s.opts.Admin()
	}
	stream, err := s.opts.Streams.AuthenticateToken(apikey, adminUser, adminToken)
	if err != nil || stream == nil {
		return nil, errUnauthorized
	}
	if (adminUser != "" && strings.EqualFold(stream.Username, adminUser)) || (adminToken != "" && apikey == adminToken) {
		return nil, fmt.Errorf("the admin token is not a stream; use a stream token as the API key")
	}
	return stream, nil
}

func (s *Server) completeDir() string {
	if s.opts.CompleteDir != nil {
		if dir := strings.TrimRight(strings.TrimSpace(s.opts.CompleteDir()), "/\\"); dir != "" {
			return dir
		}
	}
	return "/mnt/streamnzb"
}

// knownCategories is what get_config lists. Clients test that their
// configured category exists; the usual names are here, and any category a
// job arrives with is accepted regardless.
var knownCategories = []string{"*", "tv", "movies", "anime", "sonarr", "radarr"}

func (s *Server) handleGetConfig(w http.ResponseWriter) {
	cats := make([]map[string]interface{}, 0, len(knownCategories))
	for i, name := range knownCategories {
		cats = append(cats, map[string]interface{}{
			"name": name, "dir": "", "priority": 0, "pp": "", "script": "Default", "newzbin": "", "order": i,
		})
	}
	writeJSON(w, map[string]interface{}{
		"config": map[string]interface{}{
			"misc": map[string]interface{}{
				"complete_dir":         s.completeDir(),
				"download_dir":         s.completeDir(),
				"enable_tv_sorting":    false,
				"tv_categories":        []string{},
				"enable_movie_sorting": false,
				"movie_categories":     []string{},
				"enable_date_sorting":  false,
				"date_categories":      []string{},
				"pre_check":            false,
				"history_retention":    "",
				"api_key":              "",
			},
			"categories": cats,
			"sorters":    []interface{}{},
			"servers":    []interface{}{},
		},
	})
}

func (s *Server) handleFullStatus(w http.ResponseWriter) {
	s.mu.Lock()
	active := len(s.queue) > 0 && !s.queue[0].Started.IsZero()
	s.mu.Unlock()
	state := "Idle"
	if active {
		state = "Downloading"
	}
	writeJSON(w, map[string]interface{}{
		"status": map[string]interface{}{
			"version": reportedVersion, "paused": false, "status": state, "speedlimit": "", "speedlimit_abs": "",
			"have_warnings": "0", "pause_int": "0", "uptime": "", "folders": []string{}, "servers": []interface{}{},
			"completedir": s.completeDir(), "loadavg": "", "cache_art": "0", "cache_size": "0 B", "quota": "", "left_quota": "",
		},
	})
}

func mb(n int64) string { return fmt.Sprintf("%.2f", float64(n)/(1024*1024)) }

func sizeString(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	default:
		return fmt.Sprintf("%d B", n)
	}
}

// handleQueue lists the jobs still being worked, the stream's own only.
func (s *Server) handleQueue(w http.ResponseWriter, stream *auth.Stream) {
	s.mu.Lock()
	slots := make([]map[string]interface{}, 0, len(s.queue))
	var total, left int64
	state := "Idle"
	for i, j := range s.queue {
		if j.Stream != stream.Username {
			continue
		}
		status, pct := "Queued", "0"
		remaining := j.Size
		if !j.Started.IsZero() {
			// A validation moves no bytes; its progress is file sets
			// validated out of sets in the release, so a pack's bar moves
			// and the client knows the job is alive.
			status = "Downloading"
			state = "Downloading"
			p := j.progress()
			pct = strconv.Itoa(int(p * 100))
			remaining = j.Size - int64(float64(j.Size)*p)
		}
		total += j.Size
		left += remaining
		slots = append(slots, map[string]interface{}{
			"status": status, "index": i, "timeleft": "0:00:00", "eta": "unknown",
			"mb": mb(j.Size), "mbleft": mb(remaining), "size": sizeString(j.Size), "sizeleft": sizeString(remaining),
			"filename": j.Name, "priority": "Normal", "cat": j.Category, "percentage": pct, "nzo_id": j.ID,
			"unpackopts": "3", "avg_age": "0d", "script": "None", "labels": []string{}, "password": "",
		})
	}
	s.mu.Unlock()
	writeJSON(w, map[string]interface{}{
		"queue": map[string]interface{}{
			"version": reportedVersion, "paused": false, "pause_int": "0", "status": state,
			"speed": "0 ", "kbpersec": "0.00", "speedlimit": "", "speedlimit_abs": "",
			"mb": mb(total), "mbleft": mb(left), "size": sizeString(total), "sizeleft": sizeString(left),
			"timeleft": "0:00:00", "eta": "unknown", "noofslots": len(slots), "noofslots_total": len(slots),
			"start": 0, "limit": len(slots), "finish": len(slots), "slots": slots,
		},
	})
}

// handleHistory lists finished jobs, newest first, the stream's own only.
func (s *Server) handleHistory(w http.ResponseWriter, stream *auth.Stream) {
	s.mu.Lock()
	slots := make([]map[string]interface{}, 0, len(s.history))
	for _, j := range s.history {
		if j.Stream != stream.Username {
			continue
		}
		status := "Completed"
		if j.Status == statusFailed {
			status = "Failed"
		}
		downloadTime := int64(0)
		if !j.Started.IsZero() && !j.Completed.IsZero() {
			downloadTime = int64(j.Completed.Sub(j.Started).Seconds())
		}
		slots = append(slots, map[string]interface{}{
			"status": status, "fail_message": j.FailMessage, "category": j.Category, "nzo_id": j.ID,
			"name": j.Name, "nzb_name": j.Name + ".nzb", "storage": j.Storage, "path": j.Storage,
			"bytes": j.Size, "size": sizeString(j.Size), "completed": j.Completed.Unix(),
			"download_time": downloadTime, "postproc_time": 0, "stage_log": []interface{}{},
			"script": "None", "script_line": "", "script_log": "", "url": "", "url_info": "", "pp": "D",
			"loaded": false, "retry": 0, "report": "", "action_line": "", "show_details": "False",
			"meta": nil, "downloaded": j.Size, "md5sum": "", "archive": false, "duplicate_key": "", "password": "",
		})
	}
	s.mu.Unlock()
	writeJSON(w, map[string]interface{}{
		"history": map[string]interface{}{
			"version": reportedVersion, "paused": false, "pause_int": "0",
			"noofslots": len(slots), "last_history_update": time.Now().Unix(),
			"total_size": "", "month_size": "", "week_size": "", "day_size": "", "slots": slots,
		},
	})
}

// handleDelete removes jobs by id from the queue or the history. Deleting
// the active job cancels its validation.
func (s *Server) handleDelete(w http.ResponseWriter, value string, fromHistory bool) {
	ids := map[string]bool{}
	for _, id := range strings.Split(value, ",") {
		if id = strings.TrimSpace(id); id != "" {
			ids[id] = true
		}
	}
	all := ids["all"]
	s.mu.Lock()
	if fromHistory {
		kept := s.history[:0]
		for _, j := range s.history {
			if !all && !ids[j.ID] {
				kept = append(kept, j)
			}
		}
		s.history = kept
	} else {
		kept := s.queue[:0]
		for _, j := range s.queue {
			if all || ids[j.ID] {
				if j.cancel != nil {
					j.cancel()
				}
				continue
			}
			kept = append(kept, j)
		}
		s.queue = kept
	}
	s.mu.Unlock()
	if fromHistory {
		s.saveHistory()
	}
	writeJSON(w, map[string]interface{}{"status": true, "nzo_ids": sortedKeys(ids)})
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func (s *Server) handleAddFile(w http.ResponseWriter, r *http.Request, stream *auth.Stream, param func(string) string) {
	if r.Method != http.MethodPost || r.MultipartForm == nil {
		writeError(w, "addfile expects a multipart POST")
		return
	}
	var file io.ReadCloser
	uploadName := ""
	for _, field := range []string{"name", "nzbfile"} {
		if files := r.MultipartForm.File[field]; len(files) > 0 {
			f, err := files[0].Open()
			if err != nil {
				writeError(w, "could not read upload: "+err.Error())
				return
			}
			file = f
			uploadName = files[0].Filename
			break
		}
	}
	if file == nil {
		writeError(w, "no NZB file in upload")
		return
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, stremio.MaxDirectPlayNZBSize+1))
	if err != nil {
		writeError(w, "could not read upload: "+err.Error())
		return
	}
	if len(data) > stremio.MaxDirectPlayNZBSize {
		writeError(w, "NZB too large")
		return
	}
	name := firstNonEmpty(param("nzbname"), param("name"), strings.TrimSuffix(uploadName, ".nzb"))
	s.enqueue(w, stream, name, param("cat"), data, r.RemoteAddr)
}

func (s *Server) handleAddURL(w http.ResponseWriter, r *http.Request, stream *auth.Stream, param func(string) string) {
	raw := firstNonEmpty(param("name"), param("url"))
	u, err := neturl.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		writeError(w, "addurl expects an http(s) URL")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), addURLTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		writeError(w, err.Error())
		return
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		writeError(w, "fetch NZB: "+err.Error())
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		writeError(w, fmt.Sprintf("fetch NZB: HTTP %d", resp.StatusCode))
		return
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, stremio.MaxDirectPlayNZBSize+1))
	if err != nil || len(data) == 0 || len(data) > stremio.MaxDirectPlayNZBSize {
		writeError(w, "fetch NZB: unusable body")
		return
	}
	name := firstNonEmpty(param("nzbname"), strings.TrimSuffix(path.Base(u.Path), ".nzb"))
	s.enqueue(w, stream, name, param("cat"), data, r.RemoteAddr)
}

func (s *Server) enqueue(w http.ResponseWriter, stream *auth.Stream, name, category string, data []byte, remote string) {
	name = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(name), ".nzb"))
	if name == "" {
		writeError(w, "the NZB has no name")
		return
	}
	if len(data) == 0 {
		writeError(w, "empty NZB")
		return
	}
	j := &job{
		ID:       newJobID(),
		Name:     name,
		Category: strings.ToLower(strings.TrimSpace(category)),
		Stream:   stream.Username,
		Status:   statusQueued,
		Size:     declaredSize(data),
		Added:    time.Now(),
		nzb:      data,
		stream:   stream,
	}
	s.mu.Lock()
	if len(s.queue) >= maxQueue {
		s.mu.Unlock()
		writeError(w, "queue is full")
		return
	}
	s.queue = append(s.queue, j)
	s.mu.Unlock()
	logger.Info("SABnzbd API accepted release", "stream", stream.Username, "title", name, "category", j.Category, "remote", remote)
	select {
	case s.wake <- struct{}{}:
	default:
	}
	writeJSON(w, map[string]interface{}{"status": true, "nzo_ids": []string{j.ID}})
}

func newJobID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("SABnzbd_nzo_%d", time.Now().UnixNano())
	}
	return "SABnzbd_nzo_" + hex.EncodeToString(b[:])
}

// declaredSize sums the segment byte counts an NZB declares — a cheap,
// decode-free read of the size to show in the queue before the validation
// knows the real media size.
func declaredSize(data []byte) int64 {
	var total int64
	rest := string(data)
	for {
		i := strings.Index(rest, "bytes=\"")
		if i < 0 {
			return total
		}
		rest = rest[i+len("bytes=\""):]
		end := strings.IndexByte(rest, '"')
		if end < 0 {
			return total
		}
		var n int64
		for _, c := range rest[:end] {
			if c < '0' || c > '9' {
				n = 0
				break
			}
			n = n*10 + int64(c-'0')
		}
		total += n
		rest = rest[end:]
	}
}

// --- worker ------------------------------------------------------------

func (s *Server) worker() {
	defer s.wg.Done()
	for {
		j := s.nextJob()
		if j == nil {
			select {
			case <-s.stopCh:
				return
			case <-s.wake:
				continue
			}
		}
		s.process(j)
	}
}

// nextJob marks the first waiting job active and returns it, or nil.
func (s *Server) nextJob() *job {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.queue) == 0 {
		return nil
	}
	j := s.queue[0]
	j.Started = time.Now()
	j.Status = statusDownloading
	ctx, cancel := context.WithTimeout(s.ctx, jobTimeout)
	j.cancel = cancel
	j.ctxForProcess = ctx
	return j
}

func (s *Server) process(j *job) {
	ctx := j.ctxForProcess
	defer j.cancel()
	contentType := contentTypeForCategory(j.Category)
	var items []*persistence.LibraryItem
	var err error
	if s.opts.Ingest == nil {
		err = errors.New("library ingest is not available")
	} else {
		items, err = s.opts.Ingest(ctx, j.stream, stremio.IngestRequest{
			Name:        j.Name,
			NZBData:     j.nzb,
			ContentType: contentType,
			Source:      sourceForCategory(j.Category),
			ClientAddr:  "download client",
			Progress: func(done float64, total int) {
				s.mu.Lock()
				j.setsDone, j.setsTotal = done, total
				s.mu.Unlock()
			},
		})
	}

	var storage string
	if err == nil {
		storage, err = s.storagePath(ctx, j.Name, items)
	}
	s.mu.Lock()
	// Removed while running (a queue delete): say nothing further about it.
	stillQueued := len(s.queue) > 0 && s.queue[0] == j
	if stillQueued {
		s.queue = s.queue[1:]
	}
	j.Completed = time.Now()
	j.nzb = nil
	switch {
	case err != nil:
		j.Status = statusFailed
		j.FailMessage = failMessage(err, ctx)
	default:
		j.Status = statusCompleted
		var total int64
		for _, item := range items {
			j.ItemIDs = append(j.ItemIDs, item.ID)
			total += item.MediaFileSize
		}
		if total > 0 {
			j.Size = total
		}
		// Every file of the job lands in the same folder; that folder is
		// what the client is told.
		j.Storage = storage
	}
	if stillQueued {
		s.history = append([]*job{j}, s.history...)
		if len(s.history) > maxHistory {
			s.history = s.history[:maxHistory]
		}
	}
	s.mu.Unlock()
	if stillQueued {
		s.saveHistory()
	}
	if err != nil {
		logger.Warn("SABnzbd API job failed", "title", j.Name, "err", err)
	} else {
		logger.Info("SABnzbd API job completed", "title", j.Name, "storage", j.Storage)
	}
}

func failMessage(err error, ctx context.Context) string {
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return "validation timed out"
	case errors.Is(ctx.Err(), context.Canceled):
		return "cancelled"
	case errors.Is(err, stremio.ErrIngestRejected):
		return "Release unusable: " + strings.TrimPrefix(err.Error(), stremio.ErrIngestRejected.Error()+": ")
	}
	return err.Error()
}

// storagePath publishes every file of the job and returns the completed path
// the client imports from: the complete directory plus the job's folder on
// the export, or the first media file's name under the complete directory
// when the export is off.
func (s *Server) storagePath(ctx context.Context, jobName string, items []*persistence.LibraryItem) (string, error) {
	dir := s.completeDir()
	if s.opts.Publish != nil && (s.opts.ExportEnabled == nil || s.opts.ExportEnabled()) {
		folder := ""
		for _, item := range items {
			p, ok := s.opts.Publish(ctx, jobName, item)
			if !ok || p == "" {
				return "", fmt.Errorf("could not publish %s to NFS", item.MediaFileName)
			}
			if folder == "" {
				folder = p
			}
		}
		if folder != "" {
			return dir + folder, nil
		}
	}
	if len(items) == 0 {
		return dir, nil
	}
	name := strings.TrimSpace(items[0].MediaFileName)
	if name == "" {
		name = items[0].ReleaseTitle + ".mkv"
	}
	return dir + "/" + name, nil
}

// contentTypeForCategory reads the *arr's category: Sonarr's defaults say
// tv, Radarr's say movies, and users rename them along those lines. Unknown
// categories leave the decision to the release name.
func contentTypeForCategory(cat string) string {
	cat = strings.ToLower(cat)
	switch {
	case strings.Contains(cat, "tv"), strings.Contains(cat, "sonarr"), strings.Contains(cat, "series"), strings.Contains(cat, "show"), strings.Contains(cat, "anime"):
		return "series"
	case strings.Contains(cat, "movie"), strings.Contains(cat, "radarr"), strings.Contains(cat, "film"):
		return "movie"
	}
	return ""
}

func sourceForCategory(cat string) string {
	switch contentTypeForCategory(cat) {
	case "series":
		return "Sonarr"
	case "movie":
		return "Radarr"
	}
	return "Download client"
}

// --- history persistence ----------------------------------------------

func (s *Server) loadHistory() {
	if s.opts.State == nil {
		return
	}
	var stored []*job
	if found, err := s.opts.State.Get(historyStateKey, &stored); err != nil || !found {
		return
	}
	s.mu.Lock()
	s.history = stored
	s.mu.Unlock()
}

func (s *Server) saveHistory() {
	if s.opts.State == nil {
		return
	}
	s.mu.Lock()
	snapshot := make([]*job, len(s.history))
	copy(snapshot, s.history)
	s.mu.Unlock()
	if err := s.opts.State.Set(historyStateKey, snapshot); err != nil {
		logger.Debug("SABnzbd API could not persist history", "err", err)
	}
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
