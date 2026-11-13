package grove

import (
	"path/filepath"
	"sync"
	"time"

	"github.com/provasign/grove/internal/manifest"
)

type (
	// ManifestPackage is a package a repository declares in its manifests.
	ManifestPackage = manifest.Package
	// ManifestRequire is a dependency a repository declares.
	ManifestRequire = manifest.Require
	// Manifests is what ScanManifests found.
	Manifests = manifest.Result
)

var (
	manifestMu    sync.Mutex
	manifestCache = map[string]manifestEntry{}
)

type manifestEntry struct {
	at     time.Time
	result Manifests
}

// manifestTTL: one answer consults the same repository's manifests several
// times (which version it pins, what it declares, whether to skip it), and
// reading them walks a few directory levels. Long enough to cover one call
// and a burst of calls; short enough that an edited manifest is seen.
const manifestTTL = 10 * time.Second

// ScanManifests reads the build manifests of a repository, at its root and
// in the package directories below it: what the repository declares and
// what it requires. It needs no index and runs no package manager.
func ScanManifests(root string) Manifests {
	root = filepath.Clean(root)
	manifestMu.Lock()
	if e, ok := manifestCache[root]; ok && time.Since(e.at) < manifestTTL {
		manifestMu.Unlock()
		return e.result
	}
	manifestMu.Unlock()
	result := manifest.Scan(root)
	manifestMu.Lock()
	manifestCache[root] = manifestEntry{at: time.Now(), result: result}
	manifestMu.Unlock()
	return result
}
