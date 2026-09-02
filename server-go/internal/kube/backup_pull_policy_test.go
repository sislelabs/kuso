package kube

import (
	"strings"
	"testing"
)

// The backup CronJob runs ghcr.io/sislelabs/kuso-backup:latest. With
// IfNotPresent, a node keeps whatever it pulled first — after the image was
// rebuilt with pg_dump 18, two of three nodes still ran the cached pg16 one
// and external-Postgres backups kept failing on them. A moving tag needs
// Always, or a rebuild of the image changes nothing for weeks.
func TestKusoAddonChart_BackupJobPullsLatestAlways(t *testing.T) {
	t.Parallel()
	cases := map[string][]string{
		"in-cluster postgres": {"backup.schedule=0 3 * * *"},
		"external postgres":   {"backup.schedule=0 3 * * *", "external.secretName=creds"},
	}
	for name, sets := range cases {
		t.Run(name, func(t *testing.T) {
			out := helmTemplateAddon(t, "postgres", sets...)
			i := strings.Index(out, "kind: CronJob")
			if i < 0 {
				t.Fatalf("no backup CronJob rendered.\n%s", out)
			}
			job := out[i:]
			if strings.Contains(job, "imagePullPolicy: IfNotPresent") {
				t.Errorf("backup CronJob uses IfNotPresent on a :latest image; a rebuilt image never reaches nodes that cached the old one")
			}
			if !strings.Contains(job, "imagePullPolicy: Always") {
				t.Errorf("backup CronJob should pull Always")
			}
		})
	}
}
