// Package health tracks whether each configured indexer and Usenet provider is
// actually usable, as opposed to merely enabled.
//
// The distinction matters because the two answers come from different places.
// Enabled is user intent and lives in config.json; health is what the remote
// side told us last time we asked, and it is never written back into config —
// overwriting the user's switch would lose the intent and fight config reload,
// leaving nobody able to say whether a disabled component was disabled by its
// owner or by us. Effective participation is therefore Enabled AND not blocked,
// evaluated by the callers, with the two facts kept separate.
//
// Only definitive evidence may reach StateBlocked: credentials the server
// rejected, or a subscription it says has ended. Timeouts, 5xx, resets and 429s
// are inconclusive about the account and must stay fail-open, exactly as the
// validation layers do for releases — a network blip that retires a working
// provider is worse than the failed request it came from.
package health

import (
	"sort"
	"strings"
	"sync"
	"time"

	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/core/persistence"
)

// Kind names the sort of component a record describes.
type Kind string

const (
	KindIndexer  Kind = "indexer"
	KindProvider Kind = "provider"
)

// State is the runtime verdict on a component.
//
// StateDegraded is self-healing — a spent daily quota or a throttle window ends
// on its own — so degraded components keep being used where they can be.
// StateBlocked needs a human (new password, renewed subscription) and takes the
// component out of first-choice selection until a probe says otherwise.
type State string

const (
	StateOK       State = "ok"
	StateDegraded State = "degraded"
	StateBlocked  State = "blocked"
)

// Reason codes. These travel to the UI, which maps them to copy, so they are
// stable identifiers rather than sentences.
const (
	ReasonAuthFailed      = "auth_failed"
	ReasonQuotaExhausted  = "quota_exhausted"
	ReasonThrottled       = "throttled"
	ReasonConnectionLimit = "connection_limit"
)

// Probe backoff. A blocked component is re-checked on a schedule so a renewed
// subscription or corrected password heals without anyone clicking anything,
// but the interval grows because the overwhelmingly likely answer to "are your
// credentials fixed yet" is still no.
const (
	probeInterval    = 15 * time.Minute
	maxProbeInterval = 60 * time.Minute
)

// Record is one component's health, as served to the API and persisted.
type Record struct {
	Kind          Kind      `json:"kind"`
	Name          string    `json:"name"`
	State         State     `json:"state"`
	Reason        string    `json:"reason,omitempty"`
	Detail        string    `json:"detail,omitempty"`
	Since         time.Time `json:"since"`
	LastCheckedAt time.Time `json:"last_checked_at,omitempty"`
	RetryAfter    time.Time `json:"retry_after,omitempty"`
	Failures      int       `json:"failures"`
}

// Registry holds the current health of every component that has ever reported
// something other than "fine", persisted so a password that changed yesterday
// is not rediscovered by hammering the provider on every boot.
type Registry struct {
	mu      sync.RWMutex
	state   *persistence.StateManager
	records map[string]*Record

	subMu sync.Mutex
	subs  []func(Record)
}

const persistKey = "component_health"

var (
	globalMu sync.Mutex
	global   *Registry
)

// Init builds the global registry and restores persisted records. Callers that
// only read may use Global, whose methods are nil-safe.
func Init(sm *persistence.StateManager) (*Registry, error) {
	globalMu.Lock()
	defer globalMu.Unlock()
	if global != nil {
		return global, nil
	}
	r := &Registry{state: sm, records: make(map[string]*Record)}
	if err := r.load(); err != nil {
		return nil, err
	}
	global = r
	return r, nil
}

// Global returns the registry built by Init, or nil before that. Every method
// tolerates a nil receiver, so callers on the hot path need no guard.
func Global() *Registry {
	globalMu.Lock()
	defer globalMu.Unlock()
	return global
}

// Reload re-reads records after the database behind the state manager changed,
// mirroring the usage managers: without it the registry keeps serving — and
// saving — state that belongs to the database we just left.
func Reload() error {
	r := Global()
	if r == nil {
		return nil
	}
	return r.load()
}

// Flush persists the in-memory records, called before a database swap.
func Flush() error {
	r := Global()
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.saveLocked()
}

func key(kind Kind, name string) string {
	return string(kind) + "\x00" + strings.TrimSpace(name)
}

func (r *Registry) load() error {
	if r == nil || r.state == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	records := make(map[string]*Record)
	if _, err := r.state.Get(persistKey, &records); err != nil {
		return err
	}
	if records == nil {
		records = make(map[string]*Record)
	}
	r.records = records
	return nil
}

// saveLocked persists under at least a read lock held by the caller.
func (r *Registry) saveLocked() error {
	if r == nil || r.state == nil {
		return nil
	}
	return r.state.Set(persistKey, r.records)
}

// Subscribe registers a callback fired on every material change, used to push
// state to connected browsers instead of making them poll.
func (r *Registry) Subscribe(fn func(Record)) {
	if r == nil || fn == nil {
		return
	}
	r.subMu.Lock()
	defer r.subMu.Unlock()
	r.subs = append(r.subs, fn)
}

func (r *Registry) notify(rec Record) {
	if r == nil {
		return
	}
	r.subMu.Lock()
	subs := make([]func(Record), len(r.subs))
	copy(subs, r.subs)
	r.subMu.Unlock()
	for _, fn := range subs {
		fn(rec)
	}
}

// Report records a non-OK verdict.
//
// Repeat reports of the same state are cheap by design: this runs on every
// failed search and every failed dial, so only a material change (a different
// state or reason) persists and notifies. Repeats just advance the counters in
// memory.
func (r *Registry) Report(kind Kind, name string, state State, reason, detail string) {
	if r == nil || strings.TrimSpace(name) == "" || state == StateOK {
		return
	}
	now := time.Now()
	k := key(kind, name)

	r.mu.Lock()
	rec, existed := r.records[k]
	if !existed {
		rec = &Record{Kind: kind, Name: strings.TrimSpace(name), Since: now}
		r.records[k] = rec
	}
	changed := !existed || rec.State != state || rec.Reason != reason
	if changed {
		rec.Since = now
		rec.Failures = 0
	}
	rec.State = state
	rec.Reason = reason
	rec.Detail = detail
	rec.LastCheckedAt = now
	rec.Failures++
	if state == StateBlocked && (changed || rec.RetryAfter.IsZero()) {
		rec.RetryAfter = now.Add(probeInterval)
	}
	snapshot := *rec
	if changed {
		if err := r.saveLocked(); err != nil {
			logger.Error("Failed to persist component health", "kind", kind, "name", name, "err", err)
		}
	}
	r.mu.Unlock()

	if changed {
		logger.Warn("Component health changed",
			"kind", kind, "name", name, "state", state, "reason", reason, "detail", detail)
		r.notify(snapshot)
	}
}

// MarkOK clears a component that just worked. Success is the only evidence that
// outranks a stored verdict, so this drops the record entirely rather than
// keeping a healthy row nobody needs to look at.
func (r *Registry) MarkOK(kind Kind, name string) {
	if r == nil || strings.TrimSpace(name) == "" {
		return
	}
	k := key(kind, name)

	r.mu.Lock()
	rec, existed := r.records[k]
	if !existed {
		r.mu.Unlock()
		return
	}
	delete(r.records, k)
	if err := r.saveLocked(); err != nil {
		logger.Error("Failed to persist component health", "kind", kind, "name", name, "err", err)
	}
	r.mu.Unlock()

	logger.Info("Component recovered", "kind", kind, "name", name, "was", rec.State, "reason", rec.Reason)
	r.notify(Record{Kind: kind, Name: strings.TrimSpace(name), State: StateOK, Since: time.Now()})
}

// Forget drops a component's health without claiming it recovered. Editing the
// credentials is the strongest statement of human intent there is, so the old
// verdict stops applying the moment they change — the next real request decides
// afresh.
func (r *Registry) Forget(kind Kind, name string) {
	if r == nil || strings.TrimSpace(name) == "" {
		return
	}
	k := key(kind, name)

	r.mu.Lock()
	_, existed := r.records[k]
	if existed {
		delete(r.records, k)
		if err := r.saveLocked(); err != nil {
			logger.Error("Failed to persist component health", "kind", kind, "name", name, "err", err)
		}
	}
	r.mu.Unlock()

	if existed {
		r.notify(Record{Kind: kind, Name: strings.TrimSpace(name), State: StateOK, Since: time.Now()})
	}
}

// Retain drops records for components of this kind that are no longer
// configured, so a deleted indexer cannot leave a permanent warning behind.
func (r *Registry) Retain(kind Kind, names []string) {
	if r == nil {
		return
	}
	keep := make(map[string]bool, len(names))
	for _, n := range names {
		keep[strings.TrimSpace(n)] = true
	}

	r.mu.Lock()
	var dropped []string
	for k, rec := range r.records {
		if rec == nil || rec.Kind != kind {
			continue
		}
		if !keep[rec.Name] {
			dropped = append(dropped, rec.Name)
			delete(r.records, k)
		}
	}
	if len(dropped) > 0 {
		if err := r.saveLocked(); err != nil {
			logger.Error("Failed to persist component health", "kind", kind, "err", err)
		}
	}
	r.mu.Unlock()

	for _, name := range dropped {
		r.notify(Record{Kind: kind, Name: name, State: StateOK, Since: time.Now()})
	}
}

// Blocked reports whether this component is out of first-choice selection.
func (r *Registry) Blocked(kind Kind, name string) bool {
	if r == nil {
		return false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	rec, ok := r.records[key(kind, name)]
	return ok && rec != nil && rec.State == StateBlocked
}

// Lookup returns a copy of one component's record.
func (r *Registry) Lookup(kind Kind, name string) (Record, bool) {
	if r == nil {
		return Record{}, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	rec, ok := r.records[key(kind, name)]
	if !ok || rec == nil {
		return Record{}, false
	}
	return *rec, true
}

// Snapshot returns every non-OK record, ordered blocked first so the worst news
// leads any list built from it.
func (r *Registry) Snapshot() []Record {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	out := make([]Record, 0, len(r.records))
	for _, rec := range r.records {
		if rec != nil {
			out = append(out, *rec)
		}
	}
	r.mu.RUnlock()

	sort.Slice(out, func(i, j int) bool {
		if (out[i].State == StateBlocked) != (out[j].State == StateBlocked) {
			return out[i].State == StateBlocked
		}
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// DueForProbe lists blocked components whose retry window has elapsed.
func (r *Registry) DueForProbe(now time.Time) []Record {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	var due []Record
	for _, rec := range r.records {
		if rec == nil || rec.State != StateBlocked {
			continue
		}
		if rec.RetryAfter.IsZero() || !rec.RetryAfter.After(now) {
			due = append(due, *rec)
		}
	}
	return due
}

// NoteProbeFailed pushes the next probe out without changing the verdict, so a
// component that is still rejecting us is asked less and less often.
func (r *Registry) NoteProbeFailed(kind Kind, name string, detail string) {
	if r == nil {
		return
	}
	now := time.Now()
	k := key(kind, name)

	r.mu.Lock()
	defer r.mu.Unlock()
	rec, ok := r.records[k]
	if !ok || rec == nil {
		return
	}
	rec.LastCheckedAt = now
	rec.Failures++
	if detail != "" {
		rec.Detail = detail
	}
	rec.RetryAfter = now.Add(probeBackoff(rec.Failures))
	if err := r.saveLocked(); err != nil {
		logger.Error("Failed to persist component health", "kind", kind, "name", name, "err", err)
	}
}

// probeBackoff doubles from probeInterval up to maxProbeInterval.
func probeBackoff(failures int) time.Duration {
	d := probeInterval
	for i := 1; i < failures; i++ {
		d *= 2
		if d >= maxProbeInterval {
			return maxProbeInterval
		}
	}
	return d
}
