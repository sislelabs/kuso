// Env-var detection: kuso scans .env.example + source at build time and
// the log shipper at runtime to surface env-var names a service likely
// needs but may be missing. This GET feeds the dashboard's "detected but
// unset" prompts. Read-only.

package kusoApi

import "github.com/go-resty/resty/v2"

// GetDetectedEnv returns the env-var names kuso detected as referenced
// by a service, plus runtime crash hints. GET .../services/{s}/env/detected.
// Response shape:
//
//	{ "names": ["DATABASE_URL", ...],
//	  "detectedAt": "2026-...",
//	  "hints": [{ "name", "lastLine", "lastSeen" }, ...] }
func (k *KusoClient) GetDetectedEnv(project, service string) (*resty.Response, error) {
	return k.client.Get("/api/projects/" + esc(project) + "/services/" + esc(service) + "/env/detected")
}
