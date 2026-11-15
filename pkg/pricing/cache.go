package pricing

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/marshal-org/runlease/pkg/provider"
)

// cacheMu guards the in-process read-modify-write of the cache file so
// concurrent SpotRate calls (e.g. list fetching rates in parallel) can't race
// on the map or clobber each other's writes. Cross-process safety relies on the
// atomic temp-file rename in cacheStore.
var cacheMu sync.Mutex

// cacheFileName is the on-disk cache, stored alongside the registry under the
// runlease home dir.
const cacheFileName = "pricing-cache.json"

type cachedRate struct {
	USDPerHour float64   `json:"usdPerHour"`
	Approx     bool      `json:"approx,omitempty"`
	FetchedAt  time.Time `json:"fetchedAt"`
}

func cacheKey(prov provider.ProviderType, instanceType, region string) string {
	return string(prov) + "/" + instanceType + "/" + region
}

func cachePath(home string) string {
	return filepath.Join(home, cacheFileName)
}

// loadCache reads the cache file. A missing or corrupt file yields an empty
// map rather than an error — the cache is a best-effort optimisation.
func loadCache(home string) map[string]cachedRate {
	out := make(map[string]cachedRate)
	raw, err := os.ReadFile(cachePath(home))
	if err != nil {
		return out
	}
	_ = json.Unmarshal(raw, &out) // corrupt file → start fresh
	return out
}

// cacheLookup returns a cached rate when present and (when maxAge > 0) fresher
// than maxAge. A maxAge of 0 disables the freshness check, used for stale
// fallback after a failed live fetch.
func cacheLookup(home string, prov provider.ProviderType, instanceType, region string, maxAge time.Duration) (Rate, bool) {
	cacheMu.Lock()
	defer cacheMu.Unlock()
	c := loadCache(home)
	cr, ok := c[cacheKey(prov, instanceType, region)]
	if !ok {
		return Rate{}, false
	}
	if maxAge > 0 && time.Since(cr.FetchedAt) > maxAge {
		return Rate{}, false
	}
	return Rate{
		Provider:     prov,
		InstanceType: instanceType,
		Region:       region,
		USDPerHour:   cr.USDPerHour,
		Approx:       cr.Approx,
		FetchedAt:    cr.FetchedAt,
	}, true
}

// cacheStore writes a rate back to the cache. Errors are ignored — failing to
// persist the cache must never break the parent command. The write goes to a
// temp file and is atomically renamed so a concurrent runlease process can
// never observe (or be left with) a half-written cache file.
func cacheStore(home string, r Rate) {
	cacheMu.Lock()
	defer cacheMu.Unlock()
	c := loadCache(home)
	c[cacheKey(r.Provider, r.InstanceType, r.Region)] = cachedRate{
		USDPerHour: r.USDPerHour,
		Approx:     r.Approx,
		FetchedAt:  r.FetchedAt,
	}
	raw, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return
	}
	if err := os.MkdirAll(home, 0700); err != nil {
		return
	}
	tmp, err := os.CreateTemp(home, ".pricing-cache-*.tmp")
	if err != nil {
		return
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		_ = os.Remove(tmpName)
		return
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return
	}
	if err := os.Rename(tmpName, cachePath(home)); err != nil {
		_ = os.Remove(tmpName)
	}
}
