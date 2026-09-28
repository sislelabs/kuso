package compose

import (
	"strings"

	"github.com/compose-spec/compose-go/v2/types"
)

// kuso drops ALL Linux capabilities from app containers. Stock images
// that start as root and then chown their dirs, switch to an app user,
// or bind :80 crash under that (nginx: `chown(...) failed (1: Operation
// not permitted)`). These are the known offenders and the minimal caps
// each needs — every one is on the server's allowlist, and none is
// escape-equivalent (no SYS_ADMIN, NET_ADMIN, DAC_READ_SEARCH, …).
var stockImageCaps = []struct {
	match func(base, tag string) bool
	caps  []string
	why   string
}{
	{
		match: func(base, _ string) bool { return base == "nginx" },
		caps:  []string{"CHOWN", "SETUID", "SETGID", "NET_BIND_SERVICE"},
		why:   "the master process chowns its cache dirs, drops workers to the nginx user, and listens on :80",
	},
	{
		match: func(base, _ string) bool { return base == "httpd" },
		caps:  []string{"SETUID", "SETGID", "NET_BIND_SERVICE"},
		why:   "Apache binds :80 as root, then drops to the daemon user",
	},
	{
		match: func(base, tag string) bool { return base == "php" && strings.Contains(tag, "apache") },
		caps:  []string{"SETUID", "SETGID", "NET_BIND_SERVICE"},
		why:   "Apache binds :80 as root, then drops to www-data",
	},
	{
		match: func(base, tag string) bool { return base == "wordpress" && !strings.Contains(tag, "fpm") },
		caps:  []string{"CHOWN", "SETUID", "SETGID", "NET_BIND_SERVICE"},
		why:   "the entrypoint copies WordPress in owned by www-data, and Apache binds :80 as root before dropping to www-data",
	},
}

// imageCapabilities returns the caps an image-runtime service needs to
// start, and why. A container port < 1024 adds NET_BIND_SERVICE for any
// image.
func imageCapabilities(svc types.ServiceConfig) (caps []string, why []string) {
	repo, tag := imageParts(svc.Image)
	base := baseImageName(repo)
	seen := map[string]bool{}
	add := func(cs ...string) {
		for _, c := range cs {
			if !seen[c] {
				seen[c] = true
				caps = append(caps, c)
			}
		}
	}
	for _, s := range stockImageCaps {
		if s.match(base, tag) {
			add(s.caps...)
			why = append(why, s.why)
			break
		}
	}
	if !seen["NET_BIND_SERVICE"] {
		for _, p := range svc.Ports {
			if p.Target > 0 && p.Target < 1024 {
				add("NET_BIND_SERVICE")
				why = append(why, "it listens on container port "+itoa(int(p.Target))+" (< 1024)")
				break
			}
		}
	}
	return caps, why
}

func applyImageCapabilities(svc types.ServiceConfig, out *Service, rep *Report) {
	caps, why := imageCapabilities(svc)
	if len(caps) == 0 {
		return
	}
	out.SecurityContext = &SecurityContext{Capabilities: &Capabilities{Add: caps}}
	rep.service(svc.Name, "added capabilities %s because %s needs them: %s (kuso drops all capabilities by default)",
		strings.Join(caps, ", "), svc.Image, strings.Join(why, "; "))
}
