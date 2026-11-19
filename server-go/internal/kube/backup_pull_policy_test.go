package kube

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"kuso/server/internal/backup"
)

// OPS-1: the backup Jobs ran the mutable kuso-backup:latest with
// PullAlways. A release pins the image by digest; a pinned image must not
// be re-pulled, and an unpinned tag still must be (IfNotPresent left two of
// three nodes on a stale pg16 image after a rebuild).
func TestKusoAddonChart_BackupImagePullPolicy(t *testing.T) {
	t.Parallel()
	tag := "ghcr.io/sislelabs/kuso-backup:v1.2.3"
	pinned := tag + "@sha256:" + strings.Repeat("a", 64)
	cases := []struct {
		name, kind, image, wantPolicy string
		sets                          []string
	}{
		{"tag postgres", "postgres", tag, "Always", nil},
		{"tag external postgres", "postgres", tag, "Always", []string{"external.secretName=creds"}},
		{"pinned postgres", "postgres", pinned, "IfNotPresent", nil},
		{"pinned redis", "redis", pinned, "IfNotPresent", nil},
		{"pinned mysql", "mysql", pinned, "IfNotPresent", nil},
		{"pinned mongodb", "mongodb", pinned, "IfNotPresent", nil},
		{"pinned s3", "s3", pinned, "IfNotPresent", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sets := append([]string{"backup.schedule=0 3 * * *"}, tc.sets...)
			if tc.image != "" {
				sets = append(sets, "backup.image="+tc.image)
			}
			out := helmTemplateAddon(t, tc.kind, sets...)
			i := strings.Index(out, "kind: CronJob")
			if i < 0 {
				t.Fatalf("no backup CronJob rendered.\n%s", out)
			}
			job := out[i:]
			if !strings.Contains(job, "imagePullPolicy: "+tc.wantPolicy) {
				t.Errorf("backup CronJob should pull %s", tc.wantPolicy)
			}
			if tc.image != "" && !strings.Contains(job, `image: "`+tc.image+`"`) {
				t.Errorf("backup CronJob does not run the pinned image")
			}
		})
	}
}

// hack/release.sh rewrites exactly these two lines to the release's
// tag@digest; if either shape drifts the pin silently stops landing and
// the Jobs run whatever :latest is. The Go and chart defaults must also
// agree, or server-created Jobs and CronJobs run different images.
func TestBackupImagePinSlots(t *testing.T) {
	t.Parallel()
	goSrc, err := os.ReadFile(filepath.Join("..", "backup", "image.go"))
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?m)^const DefaultImage = "([^"]+)"$`).FindSubmatch(goSrc)
	if m == nil || string(m[1]) != backup.DefaultImage {
		t.Fatalf("image.go has no `const DefaultImage = \"...\"` line for release.sh to pin")
	}
	tpl, err := os.ReadFile(filepath.Join(addonChartDir(t), "templates", "_helpers.tpl"))
	if err != nil {
		t.Fatal(err)
	}
	lines := regexp.MustCompile(`(?m)^ghcr\.io/sislelabs/kuso-backup:[^\s]+$`).FindAll(tpl, -1)
	if len(lines) != 1 || string(lines[0]) != backup.DefaultImage {
		t.Fatalf("chart default %q must be one line equal to backup.DefaultImage %q", lines, backup.DefaultImage)
	}
	if got := backup.ImagePullPolicy(backup.DefaultImage + "@sha256:" + strings.Repeat("c", 64)); got != "IfNotPresent" {
		t.Errorf("pinned pull policy = %s", got)
	}
	if got := backup.ImagePullPolicy("ghcr.io/sislelabs/kuso-backup:latest"); got != "Always" {
		t.Errorf("tag pull policy = %s", got)
	}
}
