package sabnzbd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"streamnzb/pkg/auth"
	"streamnzb/pkg/core/persistence"
	"streamnzb/pkg/server/stremio"
)

type fakeStreams struct{ byToken map[string]*auth.Stream }

func (f *fakeStreams) AuthenticateToken(token, adminUser, adminToken string) (*auth.Stream, error) {
	if token == adminToken {
		return &auth.Stream{Username: adminUser, Token: adminToken}, nil
	}
	if s, ok := f.byToken[token]; ok {
		return s, nil
	}
	return nil, errors.New("unknown")
}

type fakeState struct {
	mu   sync.Mutex
	data map[string]json.RawMessage
}

func (f *fakeState) Get(key string, target interface{}) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	raw, ok := f.data[key]
	if !ok {
		return false, nil
	}
	return true, json.Unmarshal(raw, target)
}

func (f *fakeState) Set(key string, value interface{}) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if f.data == nil {
		f.data = map[string]json.RawMessage{}
	}
	f.data[key] = raw
	return nil
}

const nzbDoc = `<?xml version="1.0"?><nzb><file subject="x"><segments><segment bytes="700000" number="1">a@b</segment><segment bytes="300000" number="2">c@d</segment></segments></file></nzb>`

type harness struct {
	t       *testing.T
	s       *Server
	srv     *httptest.Server
	token   string
	ingests chan stremio.IngestRequest
	result  func(req stremio.IngestRequest) ([]*persistence.LibraryItem, error)
	state   *fakeState
}

func newHarness(t *testing.T, result func(req stremio.IngestRequest) ([]*persistence.LibraryItem, error)) *harness {
	h := &harness{t: t, token: strings.Repeat("a", 64), ingests: make(chan stremio.IngestRequest, 8), result: result, state: &fakeState{}}
	stream := &auth.Stream{Username: "arr", Token: h.token}
	h.s = New(Options{
		Streams:     &fakeStreams{byToken: map[string]*auth.Stream{h.token: stream}},
		Admin:       func() (string, string) { return "admin", strings.Repeat("b", 64) },
		CompleteDir: func() string { return "/mnt/streamnzb/" },
		Ingest: func(ctx context.Context, st *auth.Stream, req stremio.IngestRequest) ([]*persistence.LibraryItem, error) {
			if st.Username != "arr" {
				return nil, errors.New("wrong stream")
			}
			h.ingests <- req
			return h.result(req)
		},
		Publish: func(_ context.Context, jobName string, item *persistence.LibraryItem) (string, bool) {
			if item.ID == "unexported" {
				return "", false
			}
			return "/downloads/" + jobName, true
		},
		State: h.state,
	})
	h.s.Start()
	h.srv = httptest.NewServer(h.s.Handler())
	t.Cleanup(func() { h.srv.Close(); h.s.Stop() })
	return h
}

func (h *harness) get(params url.Values) map[string]interface{} {
	h.t.Helper()
	resp, err := http.Get(h.srv.URL + APIPath + "?" + params.Encode())
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		h.t.Fatal(err)
	}
	return out
}

func (h *harness) addfile(name, cat string, apikey string) map[string]interface{} {
	h.t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	_ = mw.WriteField("mode", "addfile")
	_ = mw.WriteField("apikey", apikey)
	_ = mw.WriteField("cat", cat)
	_ = mw.WriteField("nzbname", name)
	_ = mw.WriteField("output", "json")
	part, _ := mw.CreateFormFile("name", name+".nzb")
	_, _ = io.WriteString(part, nzbDoc)
	_ = mw.Close()
	resp, err := http.Post(h.srv.URL+APIPath, mw.FormDataContentType(), &body)
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		h.t.Fatal(err)
	}
	return out
}

func (h *harness) waitHistory(n int) []interface{} {
	h.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		out := h.get(url.Values{"mode": {"history"}, "apikey": {h.token}, "output": {"json"}})
		slots := out["history"].(map[string]interface{})["slots"].([]interface{})
		if len(slots) >= n {
			return slots
		}
		time.Sleep(10 * time.Millisecond)
	}
	h.t.Fatalf("history never reached %d entries", n)
	return nil
}

func TestAuthAndVersion(t *testing.T) {
	h := newHarness(t, nil)
	if out := h.get(url.Values{"mode": {"version"}}); out["error"] != "API Key Incorrect" {
		t.Fatalf("no key: %v", out)
	}
	if out := h.get(url.Values{"mode": {"version"}, "apikey": {strings.Repeat("b", 64)}}); out["error"] != "API Key Incorrect" {
		t.Fatalf("admin token must be refused: %v", out)
	}
	if out := h.get(url.Values{"mode": {"version"}, "apikey": {h.token}}); out["version"] != reportedVersion {
		t.Fatalf("version: %v", out)
	}
	if out := h.get(url.Values{"mode": {"auth"}}); out["auth"] != "apikey" {
		t.Fatalf("auth: %v", out)
	}
	cfg := h.get(url.Values{"mode": {"get_config"}, "apikey": {h.token}})["config"].(map[string]interface{})
	if cfg["misc"].(map[string]interface{})["complete_dir"] != "/mnt/streamnzb" {
		t.Fatalf("complete_dir: %v", cfg["misc"])
	}
	found := false
	for _, c := range cfg["categories"].([]interface{}) {
		if c.(map[string]interface{})["name"] == "tv" {
			found = true
		}
	}
	if !found {
		t.Fatal("tv category missing from get_config")
	}
}

func TestAddFileCompletesIntoHistory(t *testing.T) {
	release := make(chan struct{})
	published := &atomic.Int32{}
	h := newHarness(t, func(req stremio.IngestRequest) ([]*persistence.LibraryItem, error) {
		<-release
		// A two-episode pack: two rows, one job folder, summed size.
		return []*persistence.LibraryItem{
			{ID: "series:tt1:1:1:" + req.Name, ReleaseTitle: req.Name, MediaFileName: "e01.mkv", MediaFileSize: 3 << 30, Status: "good"},
			{ID: "series:tt1:1:2:" + req.Name, ReleaseTitle: req.Name, MediaFileName: "e02.mkv", MediaFileSize: 2 << 30, Status: "good"},
		}, nil
	})
	h.s.opts.Publish = func(_ context.Context, jobName string, item *persistence.LibraryItem) (string, bool) {
		published.Add(1)
		return "/downloads/" + jobName, true
	}
	out := h.addfile("Title.2020.1080p-GRP", "movies", h.token)
	if out["status"] != true {
		t.Fatalf("addfile: %v", out)
	}
	ids := out["nzo_ids"].([]interface{})
	if len(ids) != 1 || !strings.HasPrefix(ids[0].(string), "SABnzbd_nzo_") {
		t.Fatalf("nzo_ids: %v", ids)
	}

	req := <-h.ingests
	if req.ContentType != "movie" || req.Source != "Radarr" || !bytes.Equal(req.NZBData, []byte(nzbDoc)) {
		t.Fatalf("ingest request: type=%q source=%q", req.ContentType, req.Source)
	}
	q := h.get(url.Values{"mode": {"queue"}, "apikey": {h.token}})["queue"].(map[string]interface{})
	slots := q["slots"].([]interface{})
	if len(slots) != 1 {
		t.Fatalf("queue slots: %v", slots)
	}
	slot := slots[0].(map[string]interface{})
	if slot["status"] != "Downloading" || slot["nzo_id"] != ids[0] || slot["cat"] != "movies" || slot["mb"] != "0.95" {
		t.Fatalf("queue slot: %v", slot)
	}

	close(release)
	hist := h.waitHistory(1)
	entry := hist[0].(map[string]interface{})
	if entry["status"] != "Completed" || entry["nzo_id"] != ids[0] {
		t.Fatalf("history entry: %v", entry)
	}
	if entry["storage"] != "/mnt/streamnzb/downloads/Title.2020.1080p-GRP" {
		t.Fatalf("storage: %v", entry["storage"])
	}
	if entry["bytes"].(float64) != float64(5<<30) {
		t.Fatalf("bytes should be the summed media size: %v", entry["bytes"])
	}
	if published.Load() != 2 {
		t.Fatalf("every file of the job must be published, got %d", published.Load())
	}
	q = h.get(url.Values{"mode": {"queue"}, "apikey": {h.token}})["queue"].(map[string]interface{})
	if len(q["slots"].([]interface{})) != 0 {
		t.Fatal("queue should be empty once completed")
	}

	// History survived into the state store, and a new server reads it back.
	var stored []*job
	if found, _ := h.state.Get(historyStateKey, &stored); !found || len(stored) != 1 || stored[0].Storage == "" {
		t.Fatalf("persisted history: found=%v %+v", found, stored)
	}
	again := New(Options{State: h.state})
	if len(again.history) != 1 {
		t.Fatal("history not reloaded")
	}

	// Delete from history.
	h.get(url.Values{"mode": {"history"}, "name": {"delete"}, "value": {ids[0].(string)}, "apikey": {h.token}})
	hist = h.get(url.Values{"mode": {"history"}, "apikey": {h.token}})["history"].(map[string]interface{})["slots"].([]interface{})
	if len(hist) != 0 {
		t.Fatal("history delete did not remove the entry")
	}
}

func TestRejectedReleaseIsFailedWithReason(t *testing.T) {
	h := newHarness(t, func(req stremio.IngestRequest) ([]*persistence.LibraryItem, error) {
		return nil, errors.New(stremio.ErrIngestRejected.Error() + ": archive volume segment unavailable")
	})
	h.addfile("Show.S01E02.720p-GRP", "tv", h.token)
	req := <-h.ingests
	if req.ContentType != "series" || req.Source != "Sonarr" {
		t.Fatalf("tv category should mean series/Sonarr: %+v", req)
	}
	entry := h.waitHistory(1)[0].(map[string]interface{})
	if entry["status"] != "Failed" || !strings.Contains(entry["fail_message"].(string), "archive volume segment unavailable") {
		t.Fatalf("failed entry: %v", entry)
	}
}

func TestQueueDeleteCancelsActiveJob(t *testing.T) {
	h := newHarness(t, nil)
	h.result = func(req stremio.IngestRequest) ([]*persistence.LibraryItem, error) {
		return nil, errors.New("should not matter")
	}
	started := make(chan context.Context, 1)
	h.s.opts.Ingest = func(ctx context.Context, _ *auth.Stream, req stremio.IngestRequest) ([]*persistence.LibraryItem, error) {
		started <- ctx
		<-ctx.Done()
		return nil, ctx.Err()
	}
	out := h.addfile("Title.2020-GRP", "movies", h.token)
	id := out["nzo_ids"].([]interface{})[0].(string)
	ctx := <-started
	h.get(url.Values{"mode": {"queue"}, "name": {"delete"}, "value": {id}, "apikey": {h.token}})
	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("delete did not cancel the running validation")
	}
	time.Sleep(50 * time.Millisecond)
	hist := h.get(url.Values{"mode": {"history"}, "apikey": {h.token}})["history"].(map[string]interface{})["slots"].([]interface{})
	if len(hist) != 0 {
		t.Fatal("a deleted job must not appear in history")
	}
}

func TestStorageFallsBackWhenExportIsOff(t *testing.T) {
	h := newHarness(t, func(req stremio.IngestRequest) ([]*persistence.LibraryItem, error) {
		return []*persistence.LibraryItem{{ID: "unexported", ReleaseTitle: req.Name, MediaFileName: "file.mkv", Status: "good"}}, nil
	})
	h.s.opts.ExportEnabled = func() bool { return false }
	h.addfile("Title.2020-GRP", "movies", h.token)
	<-h.ingests
	entry := h.waitHistory(1)[0].(map[string]interface{})
	if entry["storage"] != "/mnt/streamnzb/file.mkv" {
		t.Fatalf("storage: %v", entry["storage"])
	}
}

func TestPartialPublishFailsJob(t *testing.T) {
	h := newHarness(t, func(req stremio.IngestRequest) ([]*persistence.LibraryItem, error) {
		return []*persistence.LibraryItem{{ID: "published"}, {ID: "unexported", MediaFileName: "second.mkv"}}, nil
	})
	h.addfile("pack", "tv", h.token)
	<-h.ingests
	entry := h.waitHistory(1)[0].(map[string]interface{})
	if entry["status"] != "Failed" || entry["storage"] != "" {
		t.Fatalf("partial publication: %v", entry)
	}
}

func TestCategoryMapping(t *testing.T) {
	cases := map[string]string{"tv": "series", "Sonarr": "series", "anime-tv": "series", "movies": "movie", "radarr-4k": "movie", "": "", "books": ""}
	for in, want := range cases {
		if got := contentTypeForCategory(in); got != want {
			t.Errorf("contentTypeForCategory(%q) = %q, want %q", in, got, want)
		}
	}
	if declaredSize([]byte(nzbDoc)) != 1_000_000 {
		t.Fatalf("declaredSize = %d", declaredSize([]byte(nzbDoc)))
	}
}

func TestDisabledEndpointIsNotFound(t *testing.T) {
	s := New(Options{Enabled: func() bool { return false }})
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	resp, err := http.Get(srv.URL + APIPath + "?mode=version")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("disabled endpoint answered %d", resp.StatusCode)
	}
}

func TestQueueReportsSetProgress(t *testing.T) {
	step := make(chan struct{})
	release := make(chan struct{})
	h := newHarness(t, func(req stremio.IngestRequest) ([]*persistence.LibraryItem, error) {
		req.Progress(0, 4)
		<-step
		req.Progress(1, 4)
		<-step
		req.Progress(3, 4)
		<-step
		req.Progress(3.5, 4)
		<-release
		return []*persistence.LibraryItem{{ID: "x", ReleaseTitle: req.Name, MediaFileName: "a.mkv", MediaFileSize: 1 << 30, Status: "good"}}, nil
	})
	h.addfile("Show.S01.1080p-GRP", "tv", h.token)
	<-h.ingests
	slot := func() map[string]interface{} {
		q := h.get(url.Values{"mode": {"queue"}, "apikey": {h.token}})["queue"].(map[string]interface{})
		slots := q["slots"].([]interface{})
		if len(slots) != 1 {
			t.Fatalf("slots: %v", slots)
		}
		return slots[0].(map[string]interface{})
	}
	if sl := slot(); sl["status"] != "Downloading" || sl["percentage"] != "0" {
		t.Fatalf("before any set: %v", sl)
	}
	step <- struct{}{}
	step <- struct{}{}
	deadline := time.Now().Add(2 * time.Second)
	for {
		sl := slot()
		if sl["percentage"] == "75" {
			if sl["mbleft"] == sl["mb"] {
				t.Fatalf("mbleft must shrink with progress: %v", sl)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("progress never reached 75%%: %v", sl)
		}
		time.Sleep(10 * time.Millisecond)
	}
	// A set half way through its census counts for half a set.
	step <- struct{}{}
	deadline = time.Now().Add(2 * time.Second)
	for {
		sl := slot()
		if sl["percentage"] == "87" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("fractional progress never reached 87%%: %v", sl)
		}
		time.Sleep(10 * time.Millisecond)
	}
	close(release)
}
