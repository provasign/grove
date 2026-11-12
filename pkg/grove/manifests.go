package grove

import "github.com/provasign/grove/internal/manifest"

type (
	// ManifestPackage is a package a repository declares in its manifests.
	ManifestPackage = manifest.Package
	// ManifestRequire is a dependency a repository declares.
	ManifestRequire = manifest.Require
	// Manifests is what ScanManifests found.
	Manifests = manifest.Result
)

// ScanManifests reads the build manifests at a repository root: what the
// repository declares and what it requires. It needs no index and runs no
// package manager.
func ScanManifests(root string) Manifests { return manifest.Scan(root) }
