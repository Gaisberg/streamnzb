package stremio

import (
	"testing"
	"time"
)

// resetExternalManifestPageCache clears the shared cache so tests don't leak
// state into each other — the cache is a package-level var by design (it is
// keyed by upstream URL, not by profile, so it can be shared across every
// caller of externalManifestCatalogPage).
func resetExternalManifestPageCache(t *testing.T) {
	t.Helper()
	externalManifestPageCache.Lock()
	externalManifestPageCache.pages = make(map[string]cachedExternalManifestPage)
	externalManifestPageCache.Unlock()
}

// TestExternalManifestPageCacheHitReturnsAClone confirms a cache hit returns
// the stored page without letting the caller's mutation of the returned slice
// corrupt what is cached — the same defensive-copy contract loadLetterboxdListPage
// follows for the analogous public-list cache.
func TestExternalManifestPageCacheHitReturnsAClone(t *testing.T) {
	resetExternalManifestPageCache(t)
	key := "https://example.com/manifest/catalog/movie/top.json"
	original := []MetaPreview{{ID: "tt1", Type: "movie", Name: "One"}}
	storeExternalManifestPage(key, original)

	got, ok := loadExternalManifestPage(key)
	if !ok {
		t.Fatal("expected a cache hit")
	}
	if len(got) != 1 || got[0].ID != "tt1" {
		t.Fatalf("got = %+v", got)
	}
	got[0].ID = "corrupted"

	again, ok := loadExternalManifestPage(key)
	if !ok || again[0].ID != "tt1" {
		t.Fatalf("cached entry was mutated by the caller's copy: %+v", again)
	}
}

// TestExternalManifestPageCacheMissForDifferentSkip confirms each distinct
// upstream URL (a different skip value produces a different endpoint URL,
// per externalManifestCatalogPage) gets its own cache entry rather than
// colliding on the base catalog id.
func TestExternalManifestPageCacheMissForDifferentSkip(t *testing.T) {
	resetExternalManifestPageCache(t)
	base := "https://example.com/manifest/catalog/movie/top.json"
	skip20 := "https://example.com/manifest/catalog/movie/top/skip=20.json"
	storeExternalManifestPage(base, []MetaPreview{{ID: "tt1", Type: "movie", Name: "Page 0"}})

	if _, ok := loadExternalManifestPage(skip20); ok {
		t.Fatal("a different skip value must not hit the base page's cache entry")
	}
}

// TestExternalManifestPageCacheExpires confirms an entry past its TTL is
// treated as a miss and removed, so a stale row is never served forever.
func TestExternalManifestPageCacheExpires(t *testing.T) {
	resetExternalManifestPageCache(t)
	key := "https://example.com/manifest/catalog/movie/top.json"
	externalManifestPageCache.Lock()
	externalManifestPageCache.pages[key] = cachedExternalManifestPage{
		metas:     []MetaPreview{{ID: "tt1", Type: "movie", Name: "Stale"}},
		expiresAt: time.Now().Add(-time.Second),
	}
	externalManifestPageCache.Unlock()

	if _, ok := loadExternalManifestPage(key); ok {
		t.Fatal("expected an expired entry to miss")
	}
	externalManifestPageCache.Lock()
	_, stillPresent := externalManifestPageCache.pages[key]
	externalManifestPageCache.Unlock()
	if stillPresent {
		t.Fatal("expected the expired entry to be evicted on read")
	}
}

// TestExternalManifestPageCacheEvictsOldestWhenFull confirms the cache stays
// bounded rather than growing without limit across every pasted manifest and
// skip value a profile ever requests.
func TestExternalManifestPageCacheEvictsOldestWhenFull(t *testing.T) {
	resetExternalManifestPageCache(t)
	externalManifestPageCache.Lock()
	for i := 0; i < externalManifestCacheMax; i++ {
		key := "https://example.com/filler/" + string(rune('a'+i%26)) + string(rune(i))
		externalManifestPageCache.pages[key] = cachedExternalManifestPage{
			metas:     []MetaPreview{{ID: "tt-filler", Type: "movie", Name: "Filler"}},
			expiresAt: time.Now().Add(time.Duration(i) * time.Second),
		}
	}
	externalManifestPageCache.Unlock()

	storeExternalManifestPage("https://example.com/new-entry.json", []MetaPreview{{ID: "tt-new", Type: "movie", Name: "New"}})

	externalManifestPageCache.Lock()
	count := len(externalManifestPageCache.pages)
	_, newEntryPresent := externalManifestPageCache.pages["https://example.com/new-entry.json"]
	externalManifestPageCache.Unlock()

	if count > externalManifestCacheMax {
		t.Fatalf("cache size = %d, want at most %d", count, externalManifestCacheMax)
	}
	if !newEntryPresent {
		t.Fatal("the newly stored entry must survive its own insertion's eviction pass")
	}
}
