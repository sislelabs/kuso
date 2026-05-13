package main

import (
	"log/slog"
	"os"
	"strings"
)

// readAdminPassword returns the admin password for first-boot seeding.
// Source priority:
//
//  1. KUSO_ADMIN_PASSWORD_FILE — path to a file containing the password
//     (typically a key from a kube Secret mounted via subPath). This is
//     the recommended source: the value never appears in `env`,
//     `kubectl describe pod`, or process snapshots.
//
//  2. KUSO_ADMIN_PASSWORD — env var. Insecure fallback for dev /
//     bootstrap; logs a warning because the value is visible to
//     anyone with kubectl exec / describe rights on the kuso-server
//     pod.
//
// Returns empty string when neither is set (OAuth-only installs). A
// file read error is logged and treated as "not set" so a misconfigured
// volume mount doesn't crash the server — the operator can still log in
// via OAuth, fix the Secret, and restart.
//
// Trailing whitespace + newlines are stripped so `printf 'pw' >` and
// `printf 'pw\n' >` both work; this matches how kube Secrets are
// commonly populated from base64-encoded values.
func readAdminPassword(logger *slog.Logger) string {
	if path := os.Getenv("KUSO_ADMIN_PASSWORD_FILE"); path != "" {
		b, err := os.ReadFile(path)
		if err != nil {
			logger.Warn("admin: read password file failed; ignoring", "path", path, "err", err)
			return ""
		}
		pw := strings.TrimRight(string(b), "\r\n\t ")
		if pw == "" {
			logger.Warn("admin: password file is empty; ignoring", "path", path)
			return ""
		}
		return pw
	}
	if pw := os.Getenv("KUSO_ADMIN_PASSWORD"); pw != "" {
		logger.Warn("admin: KUSO_ADMIN_PASSWORD is set via env var — anyone with `kubectl exec` " +
			"on the kuso-server pod can read it. Move it to a mounted Secret via " +
			"KUSO_ADMIN_PASSWORD_FILE.")
		return pw
	}
	return ""
}
