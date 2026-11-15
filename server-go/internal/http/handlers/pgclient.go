package handlers

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// pgClientRoot is where the kuso-server image (alpine) installs its
// side-by-side Postgres clients: <root>/postgresql<major>/<tool>. A var so
// tests can point it at stubs.
var pgClientRoot = "/usr/libexec"

// pgClientMajors are the client majors server-go/Dockerfile installs,
// newest first.
var pgClientMajors = []int{18, 17, 16}

// pgClientBin picks the client binary for a server major, the same way the
// backup CronJob's pgbin does. pg_dump refuses a server newer than itself,
// and a newer client writes settings an older server can't restore (an 18
// pg_dump emits `SET transaction_timeout`, which PG16 rejects), so the
// match matters in both directions. A server newer than anything installed
// falls back to the newest client; with none installed under the root the
// bare tool name resolves on PATH.
func pgClientBin(major int, tool string) string {
	installed := func(m int) (string, bool) {
		p := filepath.Join(pgClientRoot, "postgresql"+strconv.Itoa(m), tool)
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0 {
			return p, true
		}
		return "", false
	}
	if major > 0 {
		if p, ok := installed(major); ok {
			return p
		}
	}
	for _, m := range pgClientMajors {
		if p, ok := installed(m); ok {
			return p
		}
	}
	return tool
}

// pgServerMajor reads the server's major version (server_version_num
// 170004 → 17) so pgClientBin can match it. A failed probe is an error:
// dumping with a guessed client can produce a dump that won't restore.
func pgServerMajor(ctx context.Context, dsn string) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, pgClientBin(0, "psql"), "-X", "-tA", "-c", "SHOW server_version_num", dsn)
	var stderr boundedBuffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return 0, fmt.Errorf("read server version: %s", msg)
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil || n < 10000 {
		return 0, fmt.Errorf("read server version: unparseable server_version_num %q", strings.TrimSpace(string(out)))
	}
	return n / 10000, nil
}

// pgDumpFor resolves the pg_dump matching the server at dsn.
func pgDumpFor(ctx context.Context, dsn string) (string, error) {
	major, err := pgServerMajor(ctx, dsn)
	if err != nil {
		return "", err
	}
	return pgClientBin(major, "pg_dump"), nil
}
