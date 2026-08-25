package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"streamnzb/pkg/core/config"
	"streamnzb/pkg/core/health"
	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/indexer"
	"streamnzb/pkg/usenet/nntp"
)

// healthProbeTick is how often the prober looks for work. It is not the retry
// interval — that lives on each record and is measured in tens of minutes —
// only the resolution at which those deadlines are noticed.
const healthProbeTick = time.Minute

// healthProbeTimeout bounds one probe. A provider that neither accepts nor
// refuses us inside this window is answering "still broken" for our purposes.
const healthProbeTimeout = 30 * time.Second

type componentHealthResponse struct {
	Components []health.Record `json:"components"`
}

func (s *Server) handleComponentHealth(w http.ResponseWriter, r *http.Request) {
	// Health names which credentials are failing, which is exactly the sort of
	// thing a device token has no business reading.
	if !s.requireAdmin(w, r, "Only admin can read component health", http.MethodGet) {
		return
	}
	records := health.Global().Snapshot()
	if records == nil {
		records = []health.Record{}
	}
	writeJSON(w, http.StatusOK, componentHealthResponse{Components: records})
}

// handleComponentHealthRetry re-checks one component now instead of waiting for
// its retry window, for the user who just fixed the password and wants to see
// it go green.
func (s *Server) handleComponentHealthRetry(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r, "Only admin can retry components", http.MethodPost) {
		return
	}

	var req struct {
		Kind string `json:"kind"`
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	kind := health.Kind(strings.TrimSpace(req.Kind))
	name := strings.TrimSpace(req.Name)
	if name == "" || (kind != health.KindIndexer && kind != health.KindProvider) {
		writeJSONError(w, http.StatusBadRequest, "A component kind and name are required")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), healthProbeTimeout)
	defer cancel()

	if err := s.probeComponent(ctx, kind, name); err != nil {
		// The probe itself recorded the verdict; the response only reports what
		// the component said, which is not a failure of this request.
		health.Global().NoteProbeFailed(kind, name, err.Error())
		record, _ := health.Global().Lookup(kind, name)
		writeJSON(w, http.StatusOK, map[string]any{
			"success": false,
			"error":   err.Error(),
			"record":  record,
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"success": true})
}

// probeComponent asks one component whether it works, letting the layer that
// owns the credentials record the verdict as a side effect of asking.
func (s *Server) probeComponent(ctx context.Context, kind health.Kind, name string) error {
	switch kind {
	case health.KindProvider:
		s.mu.RLock()
		pool := s.providerPools[name]
		s.mu.RUnlock()
		if pool == nil {
			// A provider whose startup dial failed never made it into the pool
			// map, and that is precisely the provider most worth re-checking:
			// refusing to probe it would leave the one case this feature exists
			// for — credentials that were wrong at boot — unreachable until a
			// restart. A throwaway pool dials with the configured credentials
			// and records the same verdict the real one would.
			return s.probeUnconnectedProvider(ctx, name)
		}
		return pool.Probe(ctx)

	case health.KindIndexer:
		idx := s.findIndexerByName(name)
		if idx == nil {
			return fmt.Errorf("indexer %q is not configured", name)
		}
		err := idx.Ping(ctx)
		// Classify the outcome even when no verdict is stored yet: a probe of a
		// supposedly healthy indexer that finds a dead key must record it, not
		// just report it to the caller — the provider path already behaves this
		// way because its dial records as a side effect.
		indexer.ReportHealth(name, err)
		return err
	}
	return fmt.Errorf("unknown component kind %q", kind)
}

// probeUnconnectedProvider dials a provider that has no live pool, using one
// connection and tearing it down again.
func (s *Server) probeUnconnectedProvider(ctx context.Context, name string) error {
	cfgProvider := s.findProvider(name)
	if cfgProvider == nil {
		return fmt.Errorf("provider %q is not configured", name)
	}
	probePool := nntp.NewClientPool(
		cfgProvider.Host,
		cfgProvider.Port,
		cfgProvider.UseSSL,
		cfgProvider.Username,
		cfgProvider.Password,
		1,
	)
	probePool.SetProviderName(name)
	defer probePool.Shutdown()
	return probePool.Probe(ctx)
}

// findIndexerByName digs the named client out of the aggregator.
func (s *Server) findIndexerByName(name string) indexer.Indexer {
	s.mu.RLock()
	root := s.indexer
	s.mu.RUnlock()
	if root == nil {
		return nil
	}
	if strings.EqualFold(root.Name(), name) {
		return root
	}
	agg, ok := root.(*indexer.Aggregator)
	if !ok {
		return nil
	}
	for _, idx := range agg.GetIndexers() {
		if idx != nil && strings.EqualFold(idx.Name(), name) {
			return idx
		}
	}
	return nil
}

// healthProbeLoop re-checks blocked components on their own schedule, so a
// renewed subscription or a password fixed at the provider's end heals without
// anyone opening the UI.
func (s *Server) healthProbeLoop() {
	defer s.bgDone.Done()
	ticker := time.NewTicker(healthProbeTick)
	defer ticker.Stop()
	for {
		select {
		case <-s.stopCh:
			return
		case <-ticker.C:
		}

		for _, record := range health.Global().DueForProbe(time.Now()) {
			select {
			case <-s.stopCh:
				return
			default:
			}
			ctx, cancel := context.WithTimeout(context.Background(), healthProbeTimeout)
			err := s.probeComponent(ctx, record.Kind, record.Name)
			cancel()
			if err != nil {
				logger.Debug("Component still unhealthy", "kind", record.Kind, "name", record.Name, "err", err)
				health.Global().NoteProbeFailed(record.Kind, record.Name, err.Error())
			}
		}
	}
}

// broadcastComponentHealth pushes one changed record to every connected admin
// browser, so a subscription that lapses mid-session shows up where the user is
// already looking instead of on their next refresh.
func (s *Server) broadcastComponentHealth(record health.Record) {
	payload, err := json.Marshal(record)
	if err != nil {
		logger.Error("Failed to encode component health", "err", err)
		return
	}
	s.broadcast(WSMessage{Type: "component_health", Payload: payload})
}

// syncComponentHealth reconciles stored verdicts with a config the user just
// saved.
//
// Changed credentials retire the old verdict outright: the user telling us the
// password is different is better evidence than anything we recorded before
// they did, and making them wait out a retry window to see that would be
// absurd. Components that no longer exist are dropped so a deleted indexer
// cannot leave a warning nobody can act on.
func syncComponentHealth(oldCfg, newCfg *config.Config) {
	reg := health.Global()
	if reg == nil || newCfg == nil {
		return
	}

	oldProviders := make(map[string]config.Provider)
	oldIndexers := make(map[string]config.IndexerConfig)
	if oldCfg != nil {
		for _, p := range oldCfg.Providers {
			oldProviders[p.Name] = p
		}
		for _, i := range oldCfg.Indexers {
			oldIndexers[i.Name] = i
		}
	}
	providerNames := make([]string, 0, len(newCfg.Providers))
	for _, p := range newCfg.Providers {
		providerNames = append(providerNames, p.Name)
		prev, existed := oldProviders[p.Name]
		if !existed || providerCredentialsChanged(prev, p) {
			reg.Forget(health.KindProvider, p.Name)
		}
	}
	reg.Retain(health.KindProvider, providerNames)

	indexerNames := make([]string, 0, len(newCfg.Indexers))
	for _, i := range newCfg.Indexers {
		indexerNames = append(indexerNames, i.Name)
		prev, existed := oldIndexers[i.Name]
		if !existed || indexerCredentialsChanged(prev, i) {
			reg.Forget(health.KindIndexer, i.Name)
		}
	}
	reg.Retain(health.KindIndexer, indexerNames)
}

// providerCredentialsChanged reports whether anything the server authenticates
// us by moved. Host and port count: pointing the same account at a different
// server is a different login as far as any stored rejection is concerned.
func providerCredentialsChanged(before, after config.Provider) bool {
	return before.Username != after.Username ||
		before.Password != after.Password ||
		before.Host != after.Host ||
		before.Port != after.Port
}

func indexerCredentialsChanged(before, after config.IndexerConfig) bool {
	return before.APIKey != after.APIKey ||
		before.Username != after.Username ||
		before.Password != after.Password ||
		before.URL != after.URL
}
