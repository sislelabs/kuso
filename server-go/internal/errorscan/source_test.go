package errorscan

import (
	"context"
	"testing"
	"time"
)

func TestRuntimeWorkload(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		labels map[string]string
		want   bool
	}{
		{"env pod", map[string]string{"app.kubernetes.io/name": "kusoenvironment", "kuso.sislelabs.com/service": "p-api"}, true},
		{"cron pod", map[string]string{"app.kubernetes.io/name": "kusocron"}, true},
		{"unknown kind", map[string]string{"kuso.sislelabs.com/project": "p"}, true},
		{"build pod", map[string]string{"app.kubernetes.io/component": "kusobuild", "app.kubernetes.io/name": "kusobuild"}, false},
		{"release hook", map[string]string{"kuso.sislelabs.com/role": "release"}, false},
		{"preview seed", map[string]string{"kuso.sislelabs.com/role": "preview-seed"}, false},
		{"addon backup", map[string]string{"kuso.sislelabs.com/role": "backup"}, false},
	}
	for _, c := range cases {
		if got := RuntimeWorkload(c.labels); got != c.want {
			t.Errorf("%s: RuntimeWorkload = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestRuntimeLine(t *testing.T) {
	t.Parallel()
	kinds := NewPodKinds()
	kinds.Observe("p-api-main-x1y2z", map[string]string{"app.kubernetes.io/component": "kusobuild"})
	kinds.Observe("p-api-production-6d8f-abcde", map[string]string{"app.kubernetes.io/name": "kusoenvironment"})
	s := &Scanner{Pods: kinds}
	cases := []struct {
		name, pod, line string
		want            bool
	}{
		// Known pods: the labels decide, whatever the line looks like.
		{"known build pod", "p-api-main-x1y2z", "npm ERR! code ERESOLVE", false},
		{"known runtime pod", "p-api-production-6d8f-abcde", "#1 ERROR in handler", true},
		// Unknown pods (scanner on another replica, backlog after restart).
		{"buildkit step", "p-api-c850e9b0ef35-mw7zzky6-gt2nl", "#5 ERROR: failed to configure registry cache importer", false},
		{"release hook pod", "p-api-production-release-c850e9b0ef35-1a2b3c4d-x7k2p", "Error: P1001 can't reach database", false},
		{"runtime error", "p-api-production-776c769745-sct2l", "ERROR db down", true},
		{"service named release", "p-release-production-776c769745-sct2l", "ERROR db down", true},
	}
	for _, c := range cases {
		if got := s.runtimeLine(c.pod, c.line); got != c.want {
			t.Errorf("%s: runtimeLine = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestPodKindsExpire(t *testing.T) {
	t.Parallel()
	k := NewPodKinds()
	now := time.Unix(1_700_000_000, 0)
	k.now = func() time.Time { return now }
	k.Observe("build-pod", map[string]string{"kuso.sislelabs.com/role": "release"})
	if rt, known := k.Lookup("build-pod"); !known || rt {
		t.Fatalf("Lookup = (%v,%v); want (false,true)", rt, known)
	}
	now = now.Add(podKindTTL + time.Minute)
	if _, known := k.Lookup("build-pod"); known {
		t.Fatal("entry should expire after podKindTTL")
	}
	k.Sweep()
	if len(k.seen) != 0 {
		t.Fatalf("Sweep left %d entries", len(k.seen))
	}
	var nilKinds *PodKinds
	nilKinds.Observe("x", nil)
	if rt, known := nilKinds.Lookup("x"); !rt || known {
		t.Fatal("nil registry must report unknown/runtime")
	}
}

// A build pod's error line in LogLine must not become an ErrorEvent.
func TestTick_SkipsNonRuntimePods(t *testing.T) {
	d := openTestDB(t)
	for _, row := range []struct{ pod, line string }{
		{"proj-svc-production-776c769745-sct2l", "ERROR runtime boom"},
		{"proj-svc-c850e9b0ef35-mw7zzky6-gt2nl", "#5 ERROR: failed to configure registry cache importer"},
		{"proj-svc-main-q8w7e", "npm ERR! ERROR could not resolve"},
	} {
		if _, err := d.DB.Exec(`INSERT INTO "LogLine" (ts, pod, project, service, env, line) VALUES ($1, $2, 'proj', 'proj-svc', '', $3)`,
			time.Now(), row.pod, row.line); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	kinds := NewPodKinds()
	kinds.Observe("proj-svc-main-q8w7e", map[string]string{"app.kubernetes.io/component": "kusobuild"})
	s := &Scanner{DB: d, Pods: kinds}
	s.tick(context.Background())
	var pods []string
	rows, err := d.DB.Query(`SELECT pod FROM "ErrorEvent"`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			t.Fatal(err)
		}
		pods = append(pods, p)
	}
	if len(pods) != 1 || pods[0] != "proj-svc-production-776c769745-sct2l" {
		t.Fatalf("ErrorEvent pods = %v; want only the runtime pod", pods)
	}
}
