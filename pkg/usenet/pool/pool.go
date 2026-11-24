package pool

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/textproto"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"weak"

	"streamnzb/pkg/core/health"
	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/media/decode"
	"streamnzb/pkg/media/nzb"
	"streamnzb/pkg/usenet/nntp"

	"golang.org/x/sync/singleflight"
)

type countReader struct {
	io.Reader
	n int64
}

func (c *countReader) Read(p []byte) (int, error) {
	n, err := c.Reader.Read(p)
	c.n += int64(n)
	return n, err
}

var (
	ErrNoProvidersConfigured = errors.New("usenet/pool: no providers configured")
	ErrNoProvidersAvailable  = errors.New("usenet/pool: no providers available")

	// ErrArticleMissingEverywhere marks a fetch error as definitive for the
	// caller that received it: every provider in the caller's configuration
	// answered a genuine NNTP 430 (in this fetch, or in the fetch that filled
	// the known-missing cache entry), and the caller's context was still live.
	// A bare 430 proves less — it can come from one provider while others were
	// never asked, or from decoder text that merely contains "430". Test with
	// errors.Is; the 430 stays in the chain, so nntp.IsArticleNotFound keeps
	// matching.
	//
	// The claim is point-in-time. It holds for the configuration the caller's
	// view had when attestation read it, a moment inside the call that returned
	// the error. It does not say that configuration, or the article's
	// availability, is unchanged afterwards: a reload can land at any time after
	// the read, and a consumer must treat the marker as of that instant. When a
	// change is seen while attestation runs, the marker is withheld instead.
	ErrArticleMissingEverywhere = errors.New("usenet/pool: article missing on every provider")
)

// providerStamp names the exact provider configuration a fetch asked: the ID,
// the client pool behind it, and that pool's dial-target generation. ID alone
// misses a same-ID endpoint change (ClientPool.Reconfigure keeps the pointer
// and bumps the generation); the pointer alone misses a replaced pool.
type providerStamp struct {
	id  string
	cp  *nntp.ClientPool
	gen uint64
}

type evidenceStamp []providerStamp

func (s evidenceStamp) equal(o evidenceStamp) bool {
	if len(s) != len(o) {
		return false
	}
	for i := range s {
		if s[i] != o[i] {
			return false
		}
	}
	return true
}

// stamp snapshots this view's provider configuration: its membership, read in
// one critical section under the view's lock, then each client pool's
// generation. The two parts are not read atomically, so a stamp is only
// meaningful compared against a proof. Equality with a proof still means the
// configuration equalled it at the membership read, which the following hold
// for:
//   - provider slices are immutable once published (UpdateProviders, Subset
//     and NewPool replace them wholesale, never edit elements), so the copy
//     taken under the lock cannot change underneath the generation reads;
//   - a ClientPool's generation only ever increases (Reconfigure is its sole
//     writer) and does so in the same critical section that changes the dial
//     target, so a generation equal to the proof's, read later, was equal at
//     the membership read too;
//   - a proof's generations come from the connections that answered, whose
//     origin is fixed at dial (Client.Origin), and no pool's generation is
//     ever below that of its own connections.
//
// No lock is held across the generation reads, and none across network I/O.
func (p *Pool) stamp() evidenceStamp {
	p.mu.RLock()
	providers := p.providers
	p.mu.RUnlock()
	s := make(evidenceStamp, len(providers))
	for i := range providers {
		s[i] = providerStamp{id: providers[i].ID, cp: providers[i].ClientPool, gen: providers[i].ClientPool.Generation()}
	}
	return s
}

// isProtocol430 reports a typed NNTP 430 response. nntp.IsArticleNotFound also
// matches error text, which is right for legacy handling but is not evidence:
// a yEnc size mismatch on a served article reads "expected size 430 but got 431".
func isProtocol430(err error) bool {
	var tpErr *textproto.Error
	return errors.As(err, &tpErr) && tpErr.Code == 430
}

// missingProof carries verified evidence through the shared fetch. It is not
// ErrArticleMissingEverywhere: whether the evidence applies is decided per
// caller by attest, because singleflight hands one result to callers whose
// provider views and contexts differ.
type missingProof struct {
	err   error
	stamp evidenceStamp
}

func (e *missingProof) Error() string { return e.err.Error() }
func (e *missingProof) Unwrap() error { return e.err }

// missingEverywhereError adds ErrArticleMissingEverywhere to an error chain
// without altering its text or hiding what it wraps.
type missingEverywhereError struct{ err error }

func (e *missingEverywhereError) Error() string        { return e.err.Error() }
func (e *missingEverywhereError) Unwrap() error        { return e.err }
func (e *missingEverywhereError) Is(target error) bool { return target == ErrArticleMissingEverywhere }

// attest adds ErrArticleMissingEverywhere to err when err carries verified
// evidence for this caller's provider configuration and the caller's context
// is still live; otherwise it returns err untouched.
//
// The configuration is read twice and both reads must equal the proof. That
// does not make the pair an atomic snapshot, and a reload can still land right
// after it; it only withholds the marker when a membership or configuration
// change is visible during attestation. A mismatch is final: there is no retry,
// so reloads in a loop can only suppress the marker, never delay the caller.
// The context is read last: p.stamp takes locks and can block, and a
// cancellation that lands meanwhile must still win.
func (p *Pool) attest(ctx context.Context, err error) error {
	var proof *missingProof
	if err == nil || !errors.As(err, &proof) ||
		!proof.stamp.equal(p.stamp()) || !proof.stamp.equal(p.stamp()) || ctx.Err() != nil {
		return err
	}
	return &missingEverywhereError{err}
}

// answeredBy is the provider configuration one connection belongs to, read from
// the connection itself: the pool's current generation says nothing about the
// connection a command already ran on.
func answeredBy(conn *nntp.Client, providerID string) providerStamp {
	cp, gen := conn.Origin()
	return providerStamp{id: providerID, cp: cp, gen: gen}
}

// coveredBy returns this view's current configuration when every provider in it
// has a genuine 430 on record from exactly that configuration, nil otherwise.
func (p *Pool) coveredBy(answered evidenceStamp) evidenceStamp {
	cur := p.stamp()
	if len(cur) == 0 {
		return nil
	}
	for _, c := range cur {
		if !slices.Contains(answered, c) {
			return nil
		}
	}
	return cur
}

// cachedMissingError is what a known-missing cache hit returns; proof is the
// entry's, nil when it was not verified.
func cachedMissingError(messageID string, proof evidenceStamp) error {
	err := fmt.Errorf("fetch segment %s: 430 No Such Article (cached)", messageID)
	if proof == nil {
		return err
	}
	return &missingProof{err, proof}
}

// isArticleNotFound reports whether err indicates 430 No Such Article (article missing on server).
func isArticleNotFound(err error) bool {
	return nntp.IsArticleNotFound(err)
}

// IsArticleNotFoundError reports whether err indicates 430 No Such Article.
func IsArticleNotFoundError(err error) bool {
	return nntp.IsArticleNotFound(err)
}

func shouldCacheFetchedSegment(ctx context.Context) bool {
	return ctx == nil || ctx.Err() == nil
}

type ProviderConfig struct {
	ID         string
	Priority   int
	IsBackup   bool
	ClientPool *nntp.ClientPool

	// PipelineDepth is how many article requests this provider may have
	// outstanding on one connection. Zero inherits Config.PipelineDepth; 1 or
	// negative switches pipelining off for this provider alone. The useful
	// depth follows the round-trip time to that particular server, so a local
	// primary and an overseas backup rarely want the same number.
	PipelineDepth int
}

type Config struct {
	Providers                  []ProviderConfig
	SegmentCache               SegmentCache
	PermanentMissingMaxEntries int

	// PipelineDepth is the deployment default for how many BODY commands a
	// read-ahead batch keeps outstanding on one connection, used by every
	// provider that does not set its own. Zero takes DefaultPipelineDepth; 1 or
	// negative disables pipelining. See pipeline.go.
	PipelineDepth int
}

type Pool struct {
	providers            []ProviderConfig
	cache                SegmentCache
	sf                   *singleflight.Group
	missing              *permanentMissingSegments
	providerSig          string
	articleStats         *articleStatsRegistry
	pipelineDepth        int
	consecutive430s      map[string]int
	consecutiveSuccesses map[string]int
	cooloffUntil         map[string]time.Time
	mu                   sync.RWMutex
	activeFetches        atomic.Int64

	// leases is shared with every Subset; limiter is the one this view draws
	// on, nil on the parent and on uncapped subsets.
	leases  *leaseRegistry
	limiter *leaseLimiter

	// streamBytes is shared with every Subset; leaseKey names the stream this
	// view fetches for, empty on the parent and on unattributed subsets.
	streamBytes *streamByteRegistry
	leaseKey    string

	// root is the pool a Subset was cut from, nil on the root itself. selection
	// is the provider IDs the view asked for, nil for "every provider the root
	// has". Together they let UpdateProviders re-resolve a view against the new
	// provider set instead of leaving it on the client pools of the old one.
	root      *Pool
	selection []string
	// views is the root's registry of live Subsets, shared with every view.
	views *viewRegistry
}

// viewRegistry tracks the Subsets cut from one root pool so a provider change
// can reach them. Sessions hold their view for as long as they live, and a view
// that kept the provider list it was born with went on fetching through client
// pools the reload had already shut down. Entries are weak: a view the session
// layer dropped is collected as usual and pruned from here on the next register.
type viewRegistry struct {
	mu      sync.Mutex
	views   []weak.Pointer[Pool]
	pruneAt int
}

func newViewRegistry() *viewRegistry {
	return &viewRegistry{pruneAt: viewRegistryMinPrune}
}

// viewRegistryMinPrune is how many entries the registry may hold before it
// first sweeps out collected views. Sweeps then run each time it doubles past
// the live count, so registering stays amortised O(1) however many playlist
// requests cut a view.
const viewRegistryMinPrune = 64

func (r *viewRegistry) register(v *Pool) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.views = append(r.views, weak.Make(v))
	if len(r.views) < r.pruneAt {
		return
	}
	r.pruneLocked()
	r.pruneAt = max(2*len(r.views), viewRegistryMinPrune)
}

func (r *viewRegistry) pruneLocked() {
	alive := r.views[:0]
	for _, w := range r.views {
		if w.Value() != nil {
			alive = append(alive, w)
		}
	}
	clear(r.views[len(alive):])
	r.views = alive
}

// live returns the views still referenced somewhere, sweeping out the rest.
func (r *viewRegistry) live() []*Pool {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pruneLocked()
	out := make([]*Pool, 0, len(r.views))
	for _, w := range r.views {
		if v := w.Value(); v != nil {
			out = append(out, v)
		}
	}
	return out
}

// streamByteRegistry tracks what each stream pulled off the providers, behind its
// own lock. A parent Pool and every Subset derived from it share one registry, so
// the counters must never be guarded by any individual Pool's mutex.
type streamByteRegistry struct {
	mu      sync.RWMutex
	byLease map[string]*streamByteCounter
}

// streamByteCounter is one stream's byte total plus the meter that turns it into a
// rate. It meters through nntp.SpeedMeter, the same type the provider pools use
// for total speed, so a stream's line and the total carry identical smoothing and
// share one timebase — otherwise the two disagree on every quiet or bursty tick.
type streamByteCounter struct {
	total atomic.Int64
	speed nntp.SpeedMeter
}

func newStreamByteRegistry() *streamByteRegistry {
	return &streamByteRegistry{byLease: make(map[string]*streamByteCounter)}
}

func (r *streamByteRegistry) counter(leaseKey string) *streamByteCounter {
	r.mu.RLock()
	counter := r.byLease[leaseKey]
	r.mu.RUnlock()
	if counter != nil {
		return counter
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if counter = r.byLease[leaseKey]; counter == nil {
		counter = &streamByteCounter{}
		r.byLease[leaseKey] = counter
	}
	return counter
}

func (r *streamByteRegistry) add(leaseKey string, n int64) {
	if r == nil || leaseKey == "" || n <= 0 {
		return
	}
	r.counter(leaseKey).total.Add(n)
}

func (r *streamByteRegistry) snapshot() map[string]int64 {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[string]int64, len(r.byLease))
	for leaseKey, counter := range r.byLease {
		out[leaseKey] = counter.total.Load()
	}
	return out
}

// speeds samples every stream's meter. Stateful, like ClientPool.GetSpeed: each
// call closes the window the next one opens, so only the stats tick may call it.
func (r *streamByteRegistry) speeds() map[string]float64 {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	counters := make(map[string]*streamByteCounter, len(r.byLease))
	for leaseKey, counter := range r.byLease {
		counters[leaseKey] = counter
	}
	r.mu.RUnlock()

	out := make(map[string]float64, len(counters))
	for leaseKey, counter := range counters {
		out[leaseKey] = counter.speed.Rate(counter.total.Load())
	}
	return out
}

const (
	// providerCooloff430Threshold: consecutive 430s after which a provider enters
	// a cooloff window — it is skipped in the parallel first-segment race instead
	// of paying a wasted round-trip on every new volume of a release it does not
	// carry. getConnection's demote-to-pass-2 behavior is unchanged.
	providerCooloff430Threshold = 5
	// providerCooloffDuration: how long the provider is skipped before one probe
	// attempt is allowed again (which re-arms the cooloff if it 430s again).
	providerCooloffDuration = 60 * time.Second
	// statHedgeDelay: how long StatSegment waits on the leading provider before
	// consulting the rest anyway. Short enough that a stalled provider does not
	// hold up validation, long enough that a healthy one answers alone.
	statHedgeDelay = 150 * time.Millisecond
	// statConcurrencyDivisor / bounds: a STAT normally costs one connection on
	// the leading provider, so the per-caller budget is a fraction of that
	// provider's pool. Callers check several files at once, so this is
	// deliberately a small slice of capacity rather than most of it.
	statConcurrencyDivisor = 16
	minStatConcurrency     = 2
	maxStatConcurrency     = 8
)

type providerArticleCounter struct {
	host             string
	availableCount   atomic.Int64
	unavailableCount atomic.Int64
}

// articleStatsRegistry holds per-provider article counters behind its own lock.
// A parent Pool and every Subset derived from it share one registry, so the
// counters must never be guarded by any individual Pool's mutex.
type articleStatsRegistry struct {
	mu       sync.RWMutex
	counters map[string]*providerArticleCounter
}

func newArticleStatsRegistry(providers []ProviderConfig) *articleStatsRegistry {
	counters := make(map[string]*providerArticleCounter, len(providers))
	for i := range providers {
		providerID := strings.TrimSpace(providers[i].ID)
		if providerID == "" {
			continue
		}
		host := ""
		if providers[i].ClientPool != nil {
			host = providers[i].ClientPool.Host()
		}
		counters[providerID] = &providerArticleCounter{host: host}
	}
	return &articleStatsRegistry{counters: counters}
}

// counter returns the counter for providerID, creating it with host from
// resolveHost when absent.
func (r *articleStatsRegistry) counter(providerID string, resolveHost func(string) string) *providerArticleCounter {
	r.mu.RLock()
	c := r.counters[providerID]
	r.mu.RUnlock()
	if c != nil {
		return c
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if c = r.counters[providerID]; c != nil {
		return c
	}
	host := ""
	if resolveHost != nil {
		host = resolveHost(providerID)
	}
	c = &providerArticleCounter{host: host}
	r.counters[providerID] = c
	return c
}

// sync aligns the registry with a new provider set: counters of providers that
// stayed keep their totals (with the host refreshed, since a reload can re-point
// a provider), new providers start at zero, and removed ones are dropped so the
// stats page stops listing a provider that is no longer configured.
func (r *articleStatsRegistry) sync(providers []ProviderConfig) {
	r.mu.Lock()
	defer r.mu.Unlock()
	next := make(map[string]*providerArticleCounter, len(providers))
	for i := range providers {
		providerID := strings.TrimSpace(providers[i].ID)
		if providerID == "" {
			continue
		}
		host := ""
		if providers[i].ClientPool != nil {
			host = providers[i].ClientPool.Host()
		}
		c := r.counters[providerID]
		if c == nil {
			c = &providerArticleCounter{}
		}
		c.host = host
		next[providerID] = c
	}
	r.counters = next
}

func (r *articleStatsRegistry) snapshot() []ProviderArticleStats {
	r.mu.RLock()
	defer r.mu.RUnlock()
	ids := make([]string, 0, len(r.counters))
	for providerID := range r.counters {
		ids = append(ids, providerID)
	}
	sort.Strings(ids)
	out := make([]ProviderArticleStats, 0, len(ids))
	for _, providerID := range ids {
		counter := r.counters[providerID]
		if counter == nil {
			continue
		}
		out = append(out, ProviderArticleStats{
			ProviderID:       providerID,
			Host:             counter.host,
			AvailableCount:   counter.availableCount.Load(),
			UnavailableCount: counter.unavailableCount.Load(),
		})
	}
	return out
}

type ProviderArticleStats struct {
	ProviderID       string
	Host             string
	AvailableCount   int64
	UnavailableCount int64
}

// missingEntry is one negative-cache record. proof is set only when the entry
// came from a fetch that verified genuine 430s across the whole provider
// configuration; legacy and STAT-origin entries leave it nil and never back
// ErrArticleMissingEverywhere.
type missingEntry struct {
	at    time.Time
	proof evidenceStamp
}

type permanentMissingSegments struct {
	mu         sync.RWMutex
	m          map[string]missingEntry
	maxEntries int
}

const defaultPermanentMissingMaxEntries = 50000

func newPermanentMissingSegments(maxEntries int) *permanentMissingSegments {
	if maxEntries <= 0 {
		maxEntries = defaultPermanentMissingMaxEntries
	}
	return &permanentMissingSegments{
		m:          make(map[string]missingEntry),
		maxEntries: maxEntries,
	}
}

func (p *permanentMissingSegments) get(key string) (missingEntry, bool) {
	p.mu.RLock()
	e, ok := p.m[key]
	p.mu.RUnlock()
	return e, ok
}

func (p *permanentMissingSegments) has(key string) bool {
	_, ok := p.get(key)
	return ok
}

func (p *permanentMissingSegments) delete(key string) {
	p.mu.Lock()
	delete(p.m, key)
	p.mu.Unlock()
}

func (p *permanentMissingSegments) add(key string, proof evidenceStamp) {
	now := time.Now()
	p.mu.Lock()
	p.m[key] = missingEntry{at: now, proof: proof}
	for len(p.m) > p.maxEntries {
		var oldestKey string
		var oldest time.Time
		for k, e := range p.m {
			if oldestKey == "" || e.at.Before(oldest) {
				oldestKey = k
				oldest = e.at
			}
		}
		if oldestKey == "" {
			break
		}
		delete(p.m, oldestKey)
	}
	p.mu.Unlock()
}

func providerSignature(providers []ProviderConfig) string {
	ids := make([]string, 0, len(providers))
	for i := range providers {
		if id := strings.TrimSpace(providers[i].ID); id != "" {
			ids = append(ids, id)
		}
	}
	return strings.Join(ids, ",")
}

// missingKey scopes a missing-article marker to the provider set that proved it:
// a provider added later may well carry the article, so a marker recorded
// before UpdateProviders must not answer for the set after it.
func (p *Pool) missingKey(messageID string) string {
	p.mu.RLock()
	sig := p.providerSig
	p.mu.RUnlock()
	return sig + "|" + strings.TrimSpace(messageID)
}

func (p *Pool) isKnownMissing(messageID string) bool {
	known, _ := p.knownMissing(messageID)
	return known
}

// knownMissing is isKnownMissing plus the entry's proof, nil when unverified.
func (p *Pool) knownMissing(messageID string) (bool, evidenceStamp) {
	if p == nil || p.missing == nil {
		return false, nil
	}
	e, ok := p.missing.get(p.missingKey(messageID))
	return ok, e.proof
}

func (p *Pool) markKnownMissing(messageID string) {
	p.markKnownMissingProof(messageID, nil)
}

// markKnownMissingProof records the miss exactly as markKnownMissing does and
// attaches proof for later attestation. The entry is keyed by the provider set
// at publication time; the proof still names the configuration that was
// actually queried, so a reload in between leaves a proof that no longer
// matches rather than one that vouches for the new set.
func (p *Pool) markKnownMissingProof(messageID string, proof evidenceStamp) {
	if p == nil || p.missing == nil {
		return
	}
	p.missing.add(p.missingKey(messageID), proof)
}

func (p *Pool) clearKnownMissing(messageID string) {
	if p == nil || p.missing == nil {
		return
	}
	key := p.missingKey(messageID)
	p.missing.delete(key)
}

type attemptedProvidersError struct {
	err   error
	hosts []string
}

func (e *attemptedProvidersError) Error() string {
	if e == nil || e.err == nil {
		return ""
	}
	return e.err.Error()
}

func (e *attemptedProvidersError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.err
}

// AttemptedProviderHosts returns the provider hosts that were actually attempted before err was returned.
func AttemptedProviderHosts(err error) []string {
	var attemptedErr *attemptedProvidersError
	if !errors.As(err, &attemptedErr) || attemptedErr == nil || len(attemptedErr.hosts) == 0 {
		return nil
	}
	return append([]string(nil), attemptedErr.hosts...)
}

func wrapAttemptedProviders(err error, hosts []string) error {
	if err == nil {
		return nil
	}
	hosts = appendUniqueHosts(nil, hosts...)
	if len(hosts) == 0 {
		return err
	}
	return &attemptedProvidersError{
		err:   err,
		hosts: hosts,
	}
}

func appendUniqueHosts(dst []string, hosts ...string) []string {
	for _, host := range hosts {
		host = strings.TrimSpace(host)
		if host == "" {
			continue
		}
		exists := false
		for _, existing := range dst {
			if existing == host {
				exists = true
				break
			}
		}
		if !exists {
			dst = append(dst, host)
		}
	}
	return dst
}

func (p *Pool) attemptedAllProviderIDs(attemptedIDs []string) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if len(p.providers) == 0 {
		return false
	}
	seen := make(map[string]struct{}, len(attemptedIDs))
	for _, id := range attemptedIDs {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		seen[id] = struct{}{}
	}
	if len(seen) == 0 {
		return false
	}
	for i := range p.providers {
		id := strings.TrimSpace(p.providers[i].ID)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; !ok {
			return false
		}
	}
	return true
}

type PoolProviderTraceSnapshot struct {
	ID     string
	Host   string
	Total  int
	Idle   int
	Active int
}

type PoolTraceSnapshot struct {
	InFlightFetches int64
	Cache           CacheStats
	Providers       []PoolProviderTraceSnapshot
}

func (s PoolTraceSnapshot) CacheSummary() string {
	if s.Cache.BudgetMax > 0 {
		return fmt.Sprintf("entries=%d bytes=%d budget=%d/%d", s.Cache.Entries, s.Cache.Bytes, s.Cache.BudgetCurrent, s.Cache.BudgetMax)
	}
	return fmt.Sprintf("entries=%d bytes=%d", s.Cache.Entries, s.Cache.Bytes)
}

func (s PoolTraceSnapshot) ProviderSummary() string {
	if len(s.Providers) == 0 {
		return "none"
	}
	parts := make([]string, 0, len(s.Providers))
	for _, provider := range s.Providers {
		parts = append(parts, fmt.Sprintf("%s(host=%s total=%d idle=%d active=%d)", provider.ID, provider.Host, provider.Total, provider.Idle, provider.Active))
	}
	return strings.Join(parts, "; ")
}

func cacheStats(cache SegmentCache) CacheStats {
	if statser, ok := cache.(segmentCacheStatser); ok {
		return statser.Stats()
	}
	return CacheStats{}
}

// normalizeBackupTier clears the backup flag when every provider carries it. A
// backup tier with no primary above it has nothing to fall back from, and left
// as-is the view could serve nothing at all: every fetch path asks the primary
// tier first and only reaches for backups once that tier is spent.
func normalizeBackupTier(providers []ProviderConfig) {
	for i := range providers {
		if !providers[i].IsBackup {
			return
		}
	}
	for i := range providers {
		providers[i].IsBackup = false
	}
}

func NewPool(cfg *Config) (*Pool, error) {
	if cfg == nil || len(cfg.Providers) == 0 {
		return nil, ErrNoProvidersConfigured
	}
	providers := orderProviders(cfg.Providers)
	cache := cfg.SegmentCache
	if cache == nil {
		cache = NoopSegmentCache()
	}
	return &Pool{
		providers:            providers,
		cache:                cache,
		sf:                   &singleflight.Group{},
		missing:              newPermanentMissingSegments(cfg.PermanentMissingMaxEntries),
		providerSig:          providerSignature(providers),
		articleStats:         newArticleStatsRegistry(providers),
		pipelineDepth:        pipelineDepthOrDefault(cfg.PipelineDepth),
		consecutive430s:      make(map[string]int),
		consecutiveSuccesses: make(map[string]int),
		leases:               newLeaseRegistry(),
		streamBytes:          newStreamByteRegistry(),
		views:                newViewRegistry(),
	}, nil
}

// orderProviders copies providers into fetch order: primaries ahead of
// backups, then by priority within each tier.
func orderProviders(in []ProviderConfig) []ProviderConfig {
	providers := make([]ProviderConfig, len(in))
	copy(providers, in)
	normalizeBackupTier(providers)
	sort.Slice(providers, func(i, j int) bool {
		// A backup is behind every primary by definition, whatever priority
		// number it carries: the tier decides the order, priority only orders
		// within a tier.
		if providers[i].IsBackup != providers[j].IsBackup {
			return !providers[i].IsBackup
		}
		return providers[i].Priority < providers[j].Priority
	})
	return providers
}

// UpdateProviders swaps the provider set in place, for the pool and for every
// Subset cut from it.
//
// A reload used to build a fresh Pool instead. Every session already playing
// kept the old one — its own segment cache under its own budget, so each
// provider save added a full cache's worth of memory, and its own provider
// list, so it went on fetching through client pools the reload had just shut
// down. Updating in place keeps one cache, one budget, one set of lease
// limiters, and moves live streams onto the new providers with their next
// fetch. An empty set is allowed: views then report no providers available
// rather than dialing a removed account.
//
// Calling it on a Subset updates the root it was cut from.
func (p *Pool) UpdateProviders(cfgs []ProviderConfig) {
	if p == nil {
		return
	}
	root := p.rootPool()
	providers := orderProviders(cfgs)

	root.mu.Lock()
	root.providers = providers
	root.providerSig = providerSignature(providers)
	root.pruneProviderStateLocked()
	root.mu.Unlock()

	if root.articleStats != nil {
		root.articleStats.sync(providers)
	}

	views := root.views.live()
	for _, v := range views {
		resolved := resolveView(providers, v.selection)
		if len(resolved) == 0 {
			// Everything this view selected is gone. Subset answers that case
			// with nil and the session layer then falls back to the whole pool;
			// a live view gets the same fallback rather than a dead stream.
			resolved = resolveView(providers, nil)
		}
		v.mu.Lock()
		v.providers = resolved
		v.providerSig = providerSignature(resolved)
		v.pruneProviderStateLocked()
		v.mu.Unlock()
	}
	logger.Debug("usenet pool providers updated", "providers", root.providerSig, "views", len(views))
}

// pruneProviderStateLocked drops 430 and cooloff bookkeeping for providers
// this pool no longer holds. It must be called with p.mu held for writing.
func (p *Pool) pruneProviderStateLocked() {
	keep := make(map[string]struct{}, len(p.providers))
	for i := range p.providers {
		keep[p.providers[i].ID] = struct{}{}
	}
	for _, m := range []map[string]int{p.consecutive430s, p.consecutiveSuccesses} {
		for id := range m {
			if _, ok := keep[id]; !ok {
				delete(m, id)
			}
		}
	}
	for id := range p.cooloffUntil {
		if _, ok := keep[id]; !ok {
			delete(p.cooloffUntil, id)
		}
	}
}

// SetSegmentCacheBudget re-caps the segment cache, evicting down to the new
// budget at once when it shrinks. A nil budget falls back to the cache's
// entry-count limit.
func (p *Pool) SetSegmentCacheBudget(budget *SegmentCacheBudget) {
	if p == nil {
		return
	}
	if c, ok := p.cache.(budgetedSegmentCache); ok {
		c.SetBudget(budget)
	}
}

func (p *Pool) rootPool() *Pool {
	if p.root != nil {
		return p.root
	}
	return p
}

// recordStreamBytes attributes n wire bytes to the stream this view fetches for.
// Read-ahead lands here too: it is issued through the same lease-scoped subset as
// the read that triggered it, so a stream is charged for what it prefetches.
func (p *Pool) recordStreamBytes(n int64) {
	if p == nil {
		return
	}
	p.streamBytes.add(p.leaseKey, n)
}

// streamByteTotals totals the wire bytes pulled per stream since the pool was
// built. Unexported: the rate is what leaves the package; this is what the
// attribution tests assert on.
func (p *Pool) streamByteTotals() map[string]int64 {
	if p == nil {
		return nil
	}
	return p.streamBytes.snapshot()
}

// StreamSpeeds reports each stream's current rate in Mbps, metered exactly as
// provider speed is. Sampling advances the meters, so call it once per tick.
func (p *Pool) StreamSpeeds() map[string]float64 {
	if p == nil {
		return nil
	}
	return p.streamBytes.speeds()
}

func (p *Pool) recordArticleResult(providerID string, available bool) {
	providerID = strings.TrimSpace(providerID)
	if p == nil || providerID == "" {
		return
	}
	if p.articleStats == nil {
		return
	}
	counter := p.articleStats.counter(providerID, func(id string) string {
		p.mu.RLock()
		defer p.mu.RUnlock()
		for i := range p.providers {
			if p.providers[i].ID == id && p.providers[i].ClientPool != nil {
				return p.providers[i].ClientPool.Host()
			}
		}
		return ""
	})
	if available {
		counter.availableCount.Add(1)
		return
	}
	counter.unavailableCount.Add(1)
}

// RecordProviderArticleResult records an article operation outcome for a provider.
// available=true increments available count; false increments missing count.
func (p *Pool) RecordProviderArticleResult(providerID string, available bool) {
	p.recordArticleResult(providerID, available)
}

func (p *Pool) record430Error(providerID string) {
	providerID = strings.TrimSpace(providerID)
	if p == nil || providerID == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.consecutive430s == nil {
		p.consecutive430s = make(map[string]int)
	}
	p.consecutive430s[providerID]++
	count := p.consecutive430s[providerID]

	if p.consecutiveSuccesses == nil {
		p.consecutiveSuccesses = make(map[string]int)
	}
	p.consecutiveSuccesses[providerID] = 0

	if count >= providerCooloff430Threshold {
		if p.cooloffUntil == nil {
			p.cooloffUntil = make(map[string]time.Time)
		}
		until := time.Now().Add(providerCooloffDuration)
		if until.After(p.cooloffUntil[providerID]) {
			p.cooloffUntil[providerID] = until
			logger.Debug("provider 430 cooloff armed", "provider", providerID, "consecutive_430s", count, "duration", providerCooloffDuration)
		}
	}

	logger.Trace("provider demotion increment consecutive 430", "provider", providerID, "count", count)
}

func (p *Pool) recordSuccess(providerID string) {
	providerID = strings.TrimSpace(providerID)
	if p == nil || providerID == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.consecutiveSuccesses == nil {
		p.consecutiveSuccesses = make(map[string]int)
	}
	p.consecutiveSuccesses[providerID]++

	if p.consecutiveSuccesses[providerID] >= 10 {
		if p.consecutive430s != nil && p.consecutive430s[providerID] > 0 {
			logger.Debug("provider demotion reset consecutive 430 after 10 consecutive successes", "provider", providerID)
			p.consecutive430s[providerID] = 0
		}
		if p.cooloffUntil != nil {
			delete(p.cooloffUntil, providerID)
		}
	}
}

// providerInCooloff reports whether the provider is inside its 430 cooloff window.
func (p *Pool) providerInCooloff(providerID string) bool {
	if p == nil {
		return false
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	until, ok := p.cooloffUntil[providerID]
	return ok && time.Now().Before(until)
}

// providerBenched reports whether the provider should sit out the first-choice
// pass — either it is inside a 430 cooloff, or the provider rejected our
// credentials the last time we dialled it.
//
// Blocked is deliberately a bench and not an exclusion. A provider whose
// account we cannot log into contributes nothing, but the alternative to trying
// it can be "nobody serves this segment", and a rejected AUTHINFO costs one
// failed connect where a missing segment costs the stream. Last resort still
// beats no resort — and if the account came back, the attempt clears the
// verdict on the spot.
func (p *Pool) providerBenched(providerID string) bool {
	if p == nil {
		return false
	}
	if p.providerInCooloff(providerID) {
		return true
	}
	return health.Global().Blocked(health.KindProvider, providerID)
}

// HasBackupProviders reports whether this view holds a provider kept back for
// failover. Callers use it to decide whether "no provider could serve" is the
// end of the road or only the end of the primary tier.
func (p *Pool) HasBackupProviders() bool {
	if p == nil {
		return false
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	for i := range p.providers {
		if p.providers[i].IsBackup {
			return true
		}
	}
	return false
}

// IsBackupProvider reports whether the named provider is held back for
// failover, so a caller that asks for one connection by name can request the
// tier it actually lives in.
func (p *Pool) IsBackupProvider(providerID string) bool {
	if p == nil {
		return false
	}
	providerID = strings.TrimSpace(providerID)
	p.mu.RLock()
	defer p.mu.RUnlock()
	for i := range p.providers {
		if p.providers[i].ID == providerID {
			return p.providers[i].IsBackup
		}
	}
	return false
}

func (p *Pool) ProviderArticleStats() []ProviderArticleStats {
	if p == nil || p.articleStats == nil {
		return nil
	}
	return p.articleStats.snapshot()
}

// logSegmentDecodeFailure records a yEnc decode failure. A cancelled fetch
// context means we tore the read down ourselves, which is routine during
// parallel races and seeks, so that logs at Trace; anything else is a real
// decode failure and logs at Debug.
func logSegmentDecodeFailure(ctx context.Context, providerID, messageID string, err error, rawBytes int64) {
	ctxErr := ctx.Err()
	attrs := []any{
		"provider", providerID,
		"err", err,
		"message_id", messageID,
		"raw_body_bytes", rawBytes,
		"ctx_err", ctxErr,
	}
	if ctxErr != nil {
		logger.Trace("fetch segment decode aborted", attrs...)
		return
	}
	logger.Debug("fetch segment decode failed", attrs...)
}

func (p *Pool) FetchSegment(ctx context.Context, segment *nzb.Segment, groups []string) (SegmentData, error) {
	messageID := strings.TrimSpace(segment.ID)
	if messageID == "" {
		return SegmentData{}, fmt.Errorf("empty segment message ID")
	}
	if p.sf == nil {
		p.mu.Lock()
		if p.sf == nil {
			p.sf = &singleflight.Group{}
		}
		p.mu.Unlock()
	}

	v, err, _ := p.sf.Do(messageID, func() (interface{}, error) {
		return p.fetchSegmentOnce(ctx, messageID, segment, groups)
	})
	if err != nil {
		// The flight may be shared with callers on other views or contexts, so
		// the marker is decided here, per caller.
		return SegmentData{}, p.attest(ctx, err)
	}
	return v.(SegmentData), nil
}

// FetchSegmentFirst races the primary providers for the first segment (e.g.
// segment 0). It returns as soon as one provider succeeds, or the last error if
// all fail, so an article missing everywhere is settled in one round trip
// instead of a walk down the list.
//
// Backups sit the race out. Every racer downloads the whole article, so racing
// a metered backup would charge it for the first segment of every file of every
// release rather than for the ones the primaries could not deliver. They are
// walked one at a time, in order, only once every primary has failed.
func (p *Pool) FetchSegmentFirst(ctx context.Context, segment *nzb.Segment, groups []string) (SegmentData, error) {
	messageID := strings.TrimSpace(segment.ID)
	if messageID == "" {
		return SegmentData{}, fmt.Errorf("empty segment message ID")
	}
	if known, proof := p.knownMissing(messageID); known {
		return SegmentData{}, p.attest(ctx, cachedMissingError(messageID, proof))
	}
	if data, ok := p.cache.Get(messageID); ok {
		logger.Trace("fetch segment cache hit", "message_id", messageID)
		return data, nil
	}

	fetchCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	p.mu.RLock()
	providers := p.providers
	p.mu.RUnlock()

	// Exclude set for each provider: all other provider IDs so getConnection returns that provider.
	providerIDs := make([]string, len(providers))
	for i := range providers {
		providerIDs[i] = providers[i].ID
	}

	primaries := make([]int, 0, len(providers))
	for i := range providers {
		if providers[i].IsBackup {
			continue
		}
		primaries = append(primaries, i)
	}

	// Skip benched providers: racing a provider that provably does not carry
	// this release — or that will not let us log in — pays a wasted round-trip
	// on every new volume. If every primary is benched, race them all anyway
	// rather than failing.
	eligible := make([]int, 0, len(primaries))
	for _, i := range primaries {
		if p.providerBenched(providers[i].ID) {
			logger.Trace("fetch segment first: skipping benched provider", "provider", providers[i].ID)
			continue
		}
		eligible = append(eligible, i)
	}
	if len(eligible) == 0 {
		eligible = primaries
	}

	type segResult struct {
		data       SegmentData
		err        error
		host       string
		providerID string
	}
	ch := make(chan segResult, len(eligible))

	for _, i := range eligible {
		exclude := make([]string, 0, len(providers)-1)
		for j := range providerIDs {
			if j != i {
				exclude = append(exclude, providerIDs[j])
			}
		}
		go func(exclude []string) {
			conn, release, discard, providerID, err := p.getConnection(fetchCtx, exclude, 999, false)
			if err != nil {
				ch <- segResult{err: err}
				return
			}
			host := p.Host(providerID)

			// Connection leak guard: if fetchCtx is cancelled (e.g. another provider succeeded
			// or the caller gave up), discard the connection to interrupt the blocking read.
			stopWatch := make(chan struct{})
			go func() {
				select {
				case <-fetchCtx.Done():
					discard()
				case <-stopWatch:
				}
			}()
			defer func() {
				close(stopWatch)
				release()
			}()

			data, err := p.fetchArticleBody(fetchCtx, conn, discard, providerID, host, messageID, groups, segment.Bytes)
			ch <- segResult{data: data, err: err, host: host, providerID: providerID}
		}(exclude)
	}

	var lastErr error
	var lastTransientErr error
	var attempted []string
	var articleNotFoundErr error
	sawNonArticleNotFound := false
	for range eligible {
		res := <-ch
		attempted = appendUniqueHosts(attempted, res.host)
		if res.err == nil {
			p.recordArticleResult(res.providerID, true)
			p.recordSuccess(res.providerID)
			if err := p.storeFetched(fetchCtx, messageID, res.data); err != nil {
				cancel()
				return SegmentData{}, err
			}
			cancel()
			logger.Trace("fetch segment ok (parallel)", "message_id", messageID, "size", res.data.Size)
			return res.data, nil
		}
		lastErr = res.err
		if isArticleNotFound(res.err) {
			p.recordArticleResult(res.providerID, false)
			p.record430Error(res.providerID)
			if articleNotFoundErr == nil {
				articleNotFoundErr = res.err
			}
			// FetchSegmentFirst uses fixed one-provider workers; provider IDs are
			// not surfaced in results, so we cannot prove all providers were
			// attempted here. Do not mark permanent-missing from this fast path.
			continue
		}
		sawNonArticleNotFound = true
		lastTransientErr = res.err
	}

	// The primary tier is spent, which is the one thing a backup exists for.
	data, tier, ok := p.fetchFromBackups(fetchCtx, messageID, groups, segment.Bytes)
	attempted = appendUniqueHosts(attempted, tier.hosts...)
	if ok {
		if err := p.storeFetched(fetchCtx, messageID, data); err != nil {
			return SegmentData{}, err
		}
		logger.Trace("fetch segment ok (backup)", "message_id", messageID, "size", data.Size)
		return data, nil
	}
	if tier.notFoundErr != nil && articleNotFoundErr == nil {
		articleNotFoundErr = tier.notFoundErr
	}
	if tier.otherErr {
		sawNonArticleNotFound = true
		if tier.transientErr != nil {
			lastTransientErr = tier.transientErr
		}
	}
	if tier.lastErr != nil {
		lastErr = tier.lastErr
	}

	if articleNotFoundErr != nil && !sawNonArticleNotFound {
		return SegmentData{}, wrapAttemptedProviders(fmt.Errorf("fetch segment %s: %w", messageID, articleNotFoundErr), attempted)
	}
	if lastErr != nil {
		// A mixed outcome — some providers said 430, another failed weaker —
		// is not a proven hole. Surface the transient failure so the caller
		// retries rather than reading the wrapped 430 as missing-everywhere.
		if sawNonArticleNotFound && lastTransientErr != nil && isArticleNotFound(lastErr) {
			lastErr = lastTransientErr
		}
		return SegmentData{}, wrapAttemptedProviders(fmt.Errorf("fetch segment %s: failed after retries: %w", messageID, lastErr), attempted)
	}
	return SegmentData{}, fmt.Errorf("fetch segment %s: failed after retries", messageID)
}

// fetchArticleBody runs GROUP + BODY + yEnc decode on a leased connection and
// charges the wire bytes to the stream whether or not the decode worked. A
// failed decode leaves unread body bytes on the wire, so the connection is
// discarded here rather than handed back; every other outcome leaves the
// caller's release/discard bookkeeping alone.
func (p *Pool) fetchArticleBody(ctx context.Context, conn *nntp.Client, discard func(), providerID, host, messageID string, groups []string, sizeHint int64) (SegmentData, error) {
	p.activeFetches.Add(1)
	defer p.activeFetches.Add(-1)

	if len(groups) > 0 {
		if err := conn.SelectGroupForArticle(groups[0]); err != nil {
			if nntp.IsBenignDisconnect(err) || ctx.Err() != nil {
				logger.Trace("fetch segment group cancelled", "provider", providerID, "err", err)
			} else {
				logger.Debug("fetch segment group failed", "provider", providerID, "err", err)
			}
			return SegmentData{}, err
		}
	}

	r, err := conn.Body(messageID)
	if err != nil {
		switch {
		case isArticleNotFound(err):
			// 430 per-provider is expected during failover; the aggregated
			// "missing on all providers" summary is logged once by the caller.
			logger.Trace("fetch segment body 430", "provider", providerID, "message_id", messageID)
		case nntp.IsBenignDisconnect(err) || ctx.Err() != nil:
			logger.Trace("fetch segment body cancelled", "provider", providerID, "err", err)
		default:
			logger.Debug("fetch segment body failed", "provider", providerID, "err", err)
		}
		return SegmentData{}, err
	}

	cr := &countReader{Reader: r}
	frame, err := decode.DecodeToBytesSized(cr, sizeHint)
	// Close ensures EndResponse is called even if decode stopped before EOF.
	r.Close()
	// Charged whether or not the decode succeeded: the bytes crossed the wire.
	p.recordStreamBytes(cr.n)
	if err != nil {
		discard()
		logSegmentDecodeFailure(ctx, providerID, messageID, err, cr.n)
		return SegmentData{}, err
	}

	return SegmentData{
		Body:           frame.Data,
		Size:           int64(len(frame.Data)),
		ProviderHost:   host,
		FileName:       frame.FileName,
		YencFileSize:   frame.FileSize,
		YencPartOffset: frame.PartOffset,
	}, nil
}

// storeFetched caches a freshly fetched segment and clears any missing marker.
// It reports the context's error when the fetch outlived the caller that wanted
// it, in which case nothing is cached.
func (p *Pool) storeFetched(ctx context.Context, messageID string, data SegmentData) error {
	if !shouldCacheFetchedSegment(ctx) {
		return ctx.Err()
	}
	cached := data
	cached.ProviderHost = ""
	p.cache.Set(messageID, cached)
	p.clearKnownMissing(messageID)
	return nil
}

// tierResult is what one walk of the backup tier saw, so the caller can fold it
// into its own accounting: which providers answered, and whether what came back
// was a definitive 430 or something weaker.
type tierResult struct {
	hosts       []string
	providerIDs []string
	notFoundErr error
	lastErr     error
	otherErr    bool
	// textOnly430 is set when a backup's "not found" was only error text
	// containing 430, not a typed NNTP response.
	textOnly430 bool
	// answered holds the typed 430s with the configuration that gave each.
	answered evidenceStamp
	// transientErr keeps the last non-430 failure so a mixed outcome can be
	// reported as transient rather than as a proven hole.
	transientErr error
}

// fetchFromBackups walks the backup tier one provider at a time, in order, and
// returns the first body it gets. It runs only once the primaries have had
// their turn, so a metered account is charged for the segments they could not
// deliver and for nothing else. A view with no backups returns immediately.
func (p *Pool) fetchFromBackups(ctx context.Context, messageID string, groups []string, sizeHint int64) (SegmentData, tierResult, bool) {
	var tier tierResult
	if !p.HasBackupProviders() {
		return SegmentData{}, tier, false
	}

	var exclude []string
	for {
		conn, release, discard, providerID, err := p.getConnection(ctx, exclude, 999, true)
		if err != nil {
			if !errors.Is(err, ErrNoProvidersAvailable) {
				tier.lastErr = err
				tier.otherErr = true
				tier.transientErr = err
			}
			return SegmentData{}, tier, false
		}
		host := p.Host(providerID)
		tier.hosts = appendUniqueHosts(tier.hosts, host)
		tier.providerIDs = appendUniqueHosts(tier.providerIDs, providerID)
		origin := answeredBy(conn, providerID)
		// Excluded whatever happens next: one turn per backup, no second lap.
		exclude = append(exclude, providerID)

		data, err := func() (SegmentData, error) {
			// Interrupt pending body read if session is closed/cancelled.
			stopWatch := make(chan struct{})
			go func() {
				select {
				case <-ctx.Done():
					discard()
				case <-stopWatch:
				}
			}()
			defer func() {
				close(stopWatch)
				release()
			}()

			return p.fetchArticleBody(ctx, conn, discard, providerID, host, messageID, groups, sizeHint)
		}()
		if err == nil {
			p.recordArticleResult(providerID, true)
			p.recordSuccess(providerID)
			return data, tier, true
		}

		tier.lastErr = err
		if isArticleNotFound(err) {
			p.recordArticleResult(providerID, false)
			p.record430Error(providerID)
			if tier.notFoundErr == nil {
				tier.notFoundErr = err
			}
			if isProtocol430(err) {
				tier.answered = append(tier.answered, origin)
			} else {
				tier.textOnly430 = true
			}
			continue
		}
		tier.otherErr = true
		tier.transientErr = err
	}
}

func (p *Pool) fetchSegmentOnce(ctx context.Context, messageID string, segment *nzb.Segment, groups []string) (SegmentData, error) {
	// The provider configuration this fetch starts under; see the proof check
	// at the end. The cached result is returned unattested: this runs inside the
	// shared flight, and FetchSegment attests per caller.
	if known, proof := p.knownMissing(messageID); known {
		return SegmentData{}, cachedMissingError(messageID, proof)
	}
	if data, ok := p.cache.Get(messageID); ok {
		logger.Trace("fetch segment cache hit", "message_id", messageID)
		return data, nil
	}

	fetchCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	// The retry loop walks primaries only; the backup tier gets a single ordered
	// pass afterwards, so backups must not be counted into the rounds here or
	// the loop keeps re-asking primaries that already answered 430.
	p.mu.RLock()
	primaryCount := 0
	for i := range p.providers {
		if !p.providers[i].IsBackup {
			primaryCount++
		}
	}
	p.mu.RUnlock()

	var exclude []string
	var lastErr error
	var lastTransientErr error
	var attempted []string
	var attemptedIDs []string
	var articleNotFoundErr error
	sawNonArticleNotFound := false
	sawTextOnly430 := false
	var answered evidenceStamp // typed 430s, each tagged with the connection's own configuration
	maxAttempts := primaryCount
	if maxAttempts < 3 {
		maxAttempts = 3
	}
	for attempt := 0; attempt < maxAttempts; attempt++ {
		conn, release, discard, providerID, err := p.getConnection(fetchCtx, exclude, 999, false)
		if err != nil {
			if errors.Is(err, ErrNoProvidersAvailable) && len(exclude) > 0 {
				if articleNotFoundErr != nil && !sawNonArticleNotFound && primaryCount > 0 && len(exclude) >= primaryCount {
					break
				}
				exclude = nil
				continue
			}
			// Nothing in the primary tier can serve. The backup pass below is
			// the last word on whether this segment is reachable at all; carry
			// the reason so a pool that handed out nothing still says so rather
			// than reporting a bare "failed after retries".
			if !errors.Is(err, ErrNoProvidersAvailable) {
				return SegmentData{}, err
			}
			lastErr = err
			break
		}
		host := p.Host(providerID)
		attempted = appendUniqueHosts(attempted, host)
		attemptedIDs = appendUniqueHosts(attemptedIDs, providerID)
		origin := answeredBy(conn, providerID)

		data, err := func() (SegmentData, error) {
			// Interrupt pending body read if session is closed/cancelled.
			stopWatch := make(chan struct{})
			go func() {
				select {
				case <-fetchCtx.Done():
					discard()
				case <-stopWatch:
				}
			}()
			defer func() {
				close(stopWatch)
				release()
			}()

			return p.fetchArticleBody(fetchCtx, conn, discard, providerID, host, messageID, groups, segment.Bytes)
		}()
		if err != nil {
			lastErr = err
			if isArticleNotFound(err) {
				p.recordArticleResult(providerID, false)
				p.record430Error(providerID)
				if articleNotFoundErr == nil {
					articleNotFoundErr = err
				}
				if isProtocol430(err) {
					answered = append(answered, origin)
				} else {
					sawTextOnly430 = true
				}
			} else {
				sawNonArticleNotFound = true
				lastTransientErr = err
			}
			exclude = append(exclude, providerID)
			continue
		}
		p.recordArticleResult(providerID, true)
		p.recordSuccess(providerID)

		if err := p.storeFetched(fetchCtx, messageID, data); err != nil {
			return SegmentData{}, err
		}
		logger.Trace("fetch segment ok", "message_id", messageID, "size", data.Size)
		return data, nil
	}

	// The primary tier is spent, which is the one thing a backup exists for.
	data, tier, ok := p.fetchFromBackups(fetchCtx, messageID, groups, segment.Bytes)
	attempted = appendUniqueHosts(attempted, tier.hosts...)
	attemptedIDs = appendUniqueHosts(attemptedIDs, tier.providerIDs...)
	if ok {
		if err := p.storeFetched(fetchCtx, messageID, data); err != nil {
			return SegmentData{}, err
		}
		logger.Trace("fetch segment ok (backup)", "message_id", messageID, "size", data.Size)
		return data, nil
	}
	if tier.notFoundErr != nil && articleNotFoundErr == nil {
		articleNotFoundErr = tier.notFoundErr
	}
	if tier.otherErr {
		sawNonArticleNotFound = true
		if tier.transientErr != nil {
			lastTransientErr = tier.transientErr
		}
	}
	if tier.textOnly430 {
		sawTextOnly430 = true
	}
	answered = append(answered, tier.answered...)
	if tier.lastErr != nil {
		lastErr = tier.lastErr
	}

	if articleNotFoundErr != nil && !sawNonArticleNotFound {
		missErr := fmt.Errorf("fetch segment %s: %w", messageID, articleNotFoundErr)
		if p.attemptedAllProviderIDs(attemptedIDs) {
			// Cache admission is the pre-existing rule. The proof is new: it
			// exists only if every provider in the configuration current now
			// answered a typed 430 from that very configuration (the connection's
			// own pool and generation), so a pool swapped in, out or back during
			// the fetch cannot lend its answer to another.
			var proof evidenceStamp
			if !sawTextOnly430 {
				if proof = p.coveredBy(answered); proof != nil {
					missErr = &missingProof{missErr, proof}
				}
			}
			p.markKnownMissingProof(messageID, proof)
		}
		// One summary line per genuinely-missing segment, instead of one per provider attempt.
		logger.Debug("fetch segment missing on all providers", "message_id", messageID, "providers", attempted)
		return SegmentData{}, wrapAttemptedProviders(missErr, attempted)
	}
	if lastErr != nil {
		// A mixed outcome — some providers said 430, another failed weaker —
		// is not a proven hole. Surface the transient failure so the caller
		// retries rather than reading the wrapped 430 as missing-everywhere.
		if sawNonArticleNotFound && lastTransientErr != nil && isArticleNotFound(lastErr) {
			lastErr = lastTransientErr
		}
		return SegmentData{}, wrapAttemptedProviders(fmt.Errorf("fetch segment %s: failed after retries: %w", messageID, lastErr), attempted)
	}
	return SegmentData{}, fmt.Errorf("fetch segment %s: failed after retries", messageID)
}

// StatSegment checks whether the article exists on any provider (STAT only, no body).
// Returns (true, nil) if found, (false, nil) if 430 on all providers, (false, err) on other errors.
// Use this before opening a stream to fail fast when the first segment is missing.
func (p *Pool) StatSegment(ctx context.Context, messageID string, groups []string) (exists bool, err error) {
	messageID = strings.TrimSpace(messageID)
	if messageID == "" {
		return false, fmt.Errorf("empty segment message ID")
	}
	if p.isKnownMissing(messageID) {
		return false, nil
	}

	statCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	p.mu.RLock()
	providers := p.providers
	p.mu.RUnlock()

	providerIDs := make([]string, len(providers))
	for i := range providers {
		providerIDs[i] = providers[i].ID
	}

	type statResult struct {
		exists     bool
		err        error
		host       string
		providerID string
	}
	ch := make(chan statResult, len(providers))

	// A pool with nothing in it cannot prove an article is missing. Report it
	// as inconclusive so the caller fails open instead of poisoning a release.
	if len(providers) == 0 {
		return false, ErrNoProvidersAvailable
	}

	probe := func(i int) {
		exclude := make([]string, 0, len(providers)-1)
		for j := range providerIDs {
			if j != i {
				exclude = append(exclude, providerIDs[j])
			}
		}
		// STAT costs a connection and no bytes, so the backup tier answers here
		// on the same terms as any other provider: knowing whether it carries
		// the article is exactly what makes it usable as a fallback, and what
		// lets a unanimous 430 be cached.
		useBackup := providers[i].IsBackup
		go func(exclude []string) {
			conn, release, discard, providerID, getErr := p.getConnection(statCtx, exclude, 999, useBackup)
			if getErr != nil {
				ch <- statResult{err: getErr}
				return
			}
			host := p.Host(providerID)

			// Watchdog: if the context is cancelled while we are waiting for
			// StatArticle (or Group), call discard() so the connection is closed
			// and the pool slot is freed immediately instead of leaking until the
			// 30-second statCtx deadline expires.
			stopWatch := make(chan struct{})
			go func() {
				select {
				case <-statCtx.Done():
					discard()
				case <-stopWatch:
				}
			}()

			var doRelease = true
			defer func() {
				close(stopWatch)
				if doRelease {
					release()
				}
				// discard() is called by the watchdog when context is done;
				// if we're here normally the watchdog exits via stopWatch.
			}()

			if len(groups) > 0 {
				if groupErr := conn.SelectGroupForArticle(groups[0]); groupErr != nil {
					// A sibling STAT winning the race cancels statCtx and discards this
					// connection mid-command; the resulting error is expected noise.
					if nntp.IsBenignDisconnect(groupErr) || statCtx.Err() != nil {
						logger.Trace("stat segment group cancelled", "provider", providerID, "err", groupErr)
					} else {
						logger.Debug("stat segment group failed", "provider", providerID, "err", groupErr)
					}
					doRelease = false
					discard()
					ch <- statResult{err: groupErr, host: host, providerID: providerID}
					return
				}
			}
			exists, statErr := conn.StatArticle(messageID)
			if statErr != nil {
				if nntp.IsBenignDisconnect(statErr) || statCtx.Err() != nil {
					logger.Trace("stat segment cancelled", "provider", providerID, "err", statErr)
				} else {
					logger.Debug("stat segment failed", "provider", providerID, "err", statErr)
				}
				doRelease = false
				discard()
				ch <- statResult{err: statErr, host: host, providerID: providerID}
				return
			}
			ch <- statResult{exists: exists, host: host, providerID: providerID}
		}(exclude)
	}

	// Providers are consulted in order, not all at once. The leading provider
	// answers the overwhelming majority of STATs by itself, so a blanket
	// fan-out multiplies connection demand by the provider count for no gain
	// and queues up the smaller pools first. The rest join as soon as the
	// leader cannot confirm the article, or once it has been slow enough that
	// waiting costs more than the extra connections.
	launched := 0
	launchRemaining := func() {
		for launched < len(providers) {
			probe(launched)
			launched++
		}
	}
	probe(0)
	launched = 1

	hedge := time.NewTimer(statHedgeDelay)
	defer hedge.Stop()

	var lastErr error
	var attempted []string
	var attemptedIDs []string
	sawNotFound := false
	sawError := false
	for collected := 0; collected < launched || launched < len(providers); {
		var res statResult
		select {
		case <-hedge.C:
			launchRemaining()
			continue
		case res = <-ch:
		}
		collected++
		attempted = appendUniqueHosts(attempted, res.host)
		// Use the id the worker reported rather than reverse-mapping from the
		// host: two providers can share a hostname (two accounts on the same
		// backbone), and matching by host collapses them onto whichever comes
		// first. That left attemptedAllProviderIDs permanently unsatisfiable,
		// so unanimous 430s were never cached and dead articles were re-STATed
		// on every pass.
		if res.providerID != "" {
			attemptedIDs = appendUniqueHosts(attemptedIDs, res.providerID)
		}
		if res.err == nil && res.exists {
			p.recordArticleResult(res.providerID, true)
			p.clearKnownMissing(messageID)
			cancel()
			logger.Trace("stat segment ok", "message_id", messageID)
			return true, nil
		}
		// The leader could not settle it. Bring in every remaining provider at
		// once so a genuinely missing article still resolves in one more round
		// trip rather than walking the list one provider at a time.
		launchRemaining()
		if res.err != nil {
			if isArticleNotFound(res.err) {
				p.recordArticleResult(res.providerID, false)
			}
			lastErr = res.err
			sawError = true
			continue
		}
		if !res.exists {
			p.recordArticleResult(res.providerID, false)
			sawNotFound = true
		}
	}
	if lastErr != nil {
		return false, wrapAttemptedProviders(fmt.Errorf("stat segment %s: %w", messageID, lastErr), attempted)
	}
	if p.attemptedAllProviderIDs(attemptedIDs) && sawNotFound && !sawError {
		p.markKnownMissing(messageID)
	}
	logger.Trace("stat segment not found (430)", "message_id", messageID)
	return false, nil
}

// FetchConcurrency reports how many segment fetches this view can keep on the
// wire at once: the connections its non-backup providers offer, capped by the
// per-stream lease limit when one applies. It is the width read-ahead can fan
// out to before extra goroutines only queue up inside getConnection — which is
// what tells read-ahead whether spending depth on a connection is free or
// whether it is competing with a connection it could have had instead.
func (p *Pool) FetchConcurrency() int {
	if p == nil {
		return 0
	}
	p.mu.RLock()
	defer p.mu.RUnlock()

	total := 0
	for _, prov := range p.providers {
		if prov.IsBackup || prov.ClientPool == nil {
			continue
		}
		conns := prov.ClientPool.MaxConn()
		if limit := p.limiter.limitFor(prov.ID); limit > 0 && limit < conns {
			conns = limit
		}
		total += conns
	}
	return total
}

// StatConcurrency reports how many STATs a single caller should run against
// this pool at once. Staged fan-out means a STAT normally occupies one
// connection on the leading provider, so the budget is derived from that
// provider's pool size and kept small: validation checks many files in
// parallel, and each of those calls gets this same budget.
func (p *Pool) StatConcurrency() int {
	p.mu.RLock()
	defer p.mu.RUnlock()

	leading := 0
	for _, prov := range p.providers {
		if prov.IsBackup || prov.ClientPool == nil {
			continue
		}
		if conns := prov.ClientPool.MaxConn(); conns > leading {
			leading = conns
		}
	}
	budget := leading / statConcurrencyDivisor
	if budget < minStatConcurrency {
		return minStatConcurrency
	}
	if budget > maxStatConcurrency {
		return maxStatConcurrency
	}
	return budget
}

func (p *Pool) getConnection(ctx context.Context, exclude []string, maxPriority int, useBackup bool) (client *nntp.Client, release, discard func(), providerID string, err error) {
	p.mu.RLock()
	providers := p.providers
	p.mu.RUnlock()

	excludeSet := make(map[string]bool)
	for _, id := range exclude {
		excludeSet[id] = true
	}

	// Two passes. Pass 1 takes healthy providers — anyone not inside an active
	// 430 cooloff window and not blocked on credentials. Pass 2 falls back to
	// benched providers as a last resort. The cooloff is time-boxed and self-healing: a raw
	// consecutive-430 count skip was PERMANENT for a benched provider (it can
	// only reset via successes, which require being selected), so one
	// bad-release 430 storm funneled all traffic onto a single provider until
	// restart.
	// A provider skipped only because this lease is at its connection cap is
	// remembered rather than failed: if no other provider can serve, waiting for
	// one of our own permits is better than reporting no providers available.
	var cappedOut *ProviderConfig

	for _, wantCooloff := range []bool{false, true} {
		for i := range providers {
			prov := &providers[i]
			if excludeSet[prov.ID] {
				continue
			}
			if prov.Priority > maxPriority {
				continue
			}
			if prov.IsBackup != useBackup {
				continue
			}
			if p.providerBenched(prov.ID) != wantCooloff {
				continue
			}

			releasePermit, permitted := p.tryPermit(prov.ID)
			if !permitted {
				if cappedOut == nil {
					cappedOut = prov
				}
				continue
			}

			c, ok := prov.ClientPool.TryGet(ctx)
			if !ok {
				var getErr error
				c, getErr = prov.ClientPool.Get(ctx)
				if getErr != nil {
					releasePermit()
					if errors.Is(getErr, context.Canceled) {
						return nil, nil, nil, "", getErr
					}
					continue
				}
			}
			return p.leaseConnection(c, prov, releasePermit)
		}
	}

	if cappedOut != nil {
		releasePermit, permitted := p.waitPermit(ctx, cappedOut.ID)
		if !permitted {
			return nil, nil, nil, "", ctx.Err()
		}
		c, err := cappedOut.ClientPool.Get(ctx)
		if err != nil {
			releasePermit()
			return nil, nil, nil, "", err
		}
		return p.leaseConnection(c, cappedOut, releasePermit)
	}

	return nil, nil, nil, "", ErrNoProvidersAvailable
}

// tryPermit takes a per-lease permit without waiting. Uncapped views always
// succeed with a no-op release.
func (p *Pool) tryPermit(providerID string) (func(), bool) {
	if p.limiter == nil {
		return func() {}, true
	}
	return p.limiter.tryAcquire(providerID)
}

// waitPermit blocks for a per-lease permit until ctx ends.
func (p *Pool) waitPermit(ctx context.Context, providerID string) (func(), bool) {
	if p.limiter == nil {
		return func() {}, true
	}
	return p.limiter.acquire(ctx, providerID)
}

// leaseConnection wraps a checked-out client so returning it also returns the
// lease permit, exactly once, whichever way the caller finishes with it.
func (p *Pool) leaseConnection(c *nntp.Client, prov *ProviderConfig, releasePermit func()) (*nntp.Client, func(), func(), string, error) {
	clientPool := prov.ClientPool
	pid := prov.ID
	var once sync.Once
	release := func() {
		once.Do(func() {
			clientPool.Put(c)
			releasePermit()
		})
	}
	discard := func() {
		once.Do(func() {
			clientPool.Discard(c)
			releasePermit()
		})
	}
	return c, release, discard, pid, nil
}

func (p *Pool) GetConnection(ctx context.Context, exclude []string, maxPriority int, useBackup bool) (client *nntp.Client, release, discard func(), providerID string, err error) {
	return p.getConnection(ctx, exclude, maxPriority, useBackup)
}

func (p *Pool) DiscardConnection(client *nntp.Client, pool *nntp.ClientPool) {
	if client != nil && pool != nil {
		pool.Discard(client)
	}
}

// PurgeCache drops all entries from the segment cache and resets budget accounting.
// Call when no sessions are active so the GC can reclaim the segment memory.
func (p *Pool) PurgeCache() {
	p.cache.Purge()
	logger.Trace("pool PurgeCache: segment cache purged")
}

func (p *Pool) TraceSnapshot() PoolTraceSnapshot {
	p.mu.RLock()
	providers := make([]ProviderConfig, len(p.providers))
	copy(providers, p.providers)
	cache := p.cache
	p.mu.RUnlock()

	snapshot := PoolTraceSnapshot{
		InFlightFetches: p.activeFetches.Load(),
		Cache:           cacheStats(cache),
		Providers:       make([]PoolProviderTraceSnapshot, 0, len(providers)),
	}
	for _, provider := range providers {
		clientPool := provider.ClientPool
		if clientPool == nil {
			continue
		}
		conns := clientPool.ConnStats()
		snapshot.Providers = append(snapshot.Providers, PoolProviderTraceSnapshot{
			ID:     provider.ID,
			Host:   clientPool.Host(),
			Total:  conns.Total,
			Idle:   conns.Idle,
			Active: conns.Active,
		})
	}
	return snapshot
}

func (p *Pool) CountProviders() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.providers)
}

func (p *Pool) ProviderOrder() []string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	ids := make([]string, 0, len(p.providers))
	for i := range p.providers {
		ids = append(ids, p.providers[i].ID)
	}
	return ids
}

func (p *Pool) ProviderHosts() []string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	hosts := make([]string, 0, len(p.providers))
	for i := range p.providers {
		if h := p.providers[i].ClientPool.Host(); h != "" {
			hosts = append(hosts, h)
		}
	}
	return hosts
}

// SubsetForLease is Subset with a per-lease connection cap. leaseKey identifies
// whose budget this is (a stream username); limits maps provider ID to the most
// connections that lease may hold at once. All subsets sharing a leaseKey draw
// on one budget, so a stream cannot sidestep its cap by holding several views.
func (p *Pool) SubsetForLease(leaseKey string, providerIDs []string, limits map[string]int) *Pool {
	subset := p.Subset(providerIDs)
	if subset == nil {
		return nil
	}
	subset.limiter = p.leases.limiter(leaseKey, limits)
	subset.leaseKey = strings.TrimSpace(leaseKey)
	return subset
}

func (p *Pool) Subset(providerIDs []string) *Pool {
	if p == nil {
		return nil
	}
	root := p.rootPool()

	// The view records what it asked for, not just what that resolved to, so
	// UpdateProviders can resolve it again against the next provider set.
	p.mu.RLock()
	selection := p.selection
	parentProviders := p.providers
	p.mu.RUnlock()
	if len(providerIDs) > 0 {
		selection = make([]string, 0, len(providerIDs))
		for _, id := range providerIDs {
			if id = strings.TrimSpace(id); id != "" && providerIn(parentProviders, id) {
				selection = append(selection, id)
			}
		}
		if len(selection) == 0 {
			return nil
		}
	}

	root.mu.RLock()
	rootProviders := root.providers
	root.mu.RUnlock()
	subset := resolveView(rootProviders, selection)
	if len(subset) == 0 {
		return nil
	}

	view := &Pool{
		root:                 root,
		selection:            selection,
		views:                root.views,
		providers:            subset,
		cache:                p.cache,
		sf:                   p.sf,
		missing:              p.missing,
		providerSig:          providerSignature(subset),
		articleStats:         p.articleStats,
		pipelineDepth:        p.pipelineDepth,
		consecutive430s:      make(map[string]int),
		consecutiveSuccesses: make(map[string]int),
		leases:               p.leases,
		streamBytes:          p.streamBytes,
		leaseKey:             p.leaseKey,
	}
	root.views.register(view)
	return view
}

// resolveView builds a view's provider list from the root's: every provider
// when selection is nil, otherwise the selected ones in the order given, with
// priorities renumbered to that order.
func resolveView(rootProviders []ProviderConfig, selection []string) []ProviderConfig {
	var subset []ProviderConfig
	if selection == nil {
		subset = make([]ProviderConfig, len(rootProviders))
		copy(subset, rootProviders)
	} else {
		byID := make(map[string]ProviderConfig, len(rootProviders))
		for i := range rootProviders {
			byID[rootProviders[i].ID] = rootProviders[i]
		}
		subset = make([]ProviderConfig, 0, len(selection))
		for _, id := range selection {
			if cfg, ok := byID[id]; ok {
				cfg.Priority = len(subset)
				subset = append(subset, cfg)
			}
		}
	}
	// A stream may select only what the parent holds back for failover. That
	// view has no tier above it, so the flag is dropped rather than leaving the
	// stream with nothing it is allowed to fetch from.
	normalizeBackupTier(subset)
	return subset
}

func providerIn(providers []ProviderConfig, id string) bool {
	for i := range providers {
		if providers[i].ID == id {
			return true
		}
	}
	return false
}

func (p *Pool) Host(providerID string) string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	for i := range p.providers {
		if p.providers[i].ID == providerID {
			return p.providers[i].ClientPool.Host()
		}
	}
	return ""
}
