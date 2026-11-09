package vulnscan

import "strings"

// Finding origins, so a reader can tell what its repo can fix.
const (
	// OriginApp: a dependency of the app itself (lockfile, go.mod, …).
	OriginApp = "app"
	// OriginOS: a distro package from the base image.
	OriginOS = "os"
	// OriginImage: prebuilt software the image carries outside the app,
	// like a package manager's own deps or a language toolchain.
	OriginImage = "image"
)

// osPkgTypes are the Trivy result types of OS package scans.
var osPkgTypes = map[string]bool{
	"alpine": true, "alma": true, "amazon": true, "azurelinux": true, "bottlerocket": true,
	"cbl-mariner": true, "centos": true, "chainguard": true, "debian": true, "echo": true,
	"fedora": true, "minimos": true, "opensuse": true, "opensuse.leap": true,
	"opensuse.tumbleweed": true, "oracle": true, "photon": true, "redhat": true,
	"rocky": true, "sles": true, "slem": true, "ubuntu": true, "wolfi": true,
}

// imagePathPrefixes hold software an image build leaves behind that is not
// the app's own dependency tree: package-manager caches, global installs,
// nix store paths and language toolchains.
var imagePathPrefixes = []string{
	"root/.",
	"nix/store/",
	"usr/lib/node_modules/",
	"usr/local/lib/node_modules/",
	"usr/local/share/.cache/",
	"opt/yarn",
	"usr/local/go/",
	"usr/lib/go",
	"usr/lib/python",
	"usr/local/lib/python",
	"usr/share/",
}

// FindingOrigin classifies a finding by package type and path. It is a
// heuristic: a binary the app copies to /usr/local/bin reads as app, and
// pkgPath is always shown so a reader can check. Findings stored before
// pkgPath was recorded return "" unless they are OS packages.
func FindingOrigin(pkgType, pkgPath string) string {
	if osPkgTypes[pkgType] {
		return OriginOS
	}
	p := strings.TrimPrefix(pkgPath, "/")
	if p == "" {
		return ""
	}
	if strings.Contains(p, "/.cache/") || strings.HasPrefix(p, "home/") && strings.Contains(p, "/.") {
		return OriginImage
	}
	for _, pre := range imagePathPrefixes {
		if strings.HasPrefix(p, pre) {
			return OriginImage
		}
	}
	return OriginApp
}
