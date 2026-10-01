package addons

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
)

func addExternalPSDB(t *testing.T, s *Service) {
	t.Helper()
	if _, err := s.Add(context.Background(), "alpha", CreateAddonRequest{
		Name: "psdb", Kind: "postgres",
		ExternalCredentials: map[string]string{
			"DATABASE_URL":      "postgres://u:p@ext.example.com:5432/db",
			"POSTGRES_USER":     "u",
			"POSTGRES_PASSWORD": "p",
			"POSTGRES_DB":       "db",
		},
	}); err != nil {
		t.Fatalf("Add: %v", err)
	}
}

func connKey(t *testing.T, s *Service, key string) string {
	t.Helper()
	sec, err := s.Kube.Clientset.CoreV1().Secrets("kuso").Get(context.Background(), "alpha-psdb-conn", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return string(sec.Data[key])
}

// POOLER_* keys of an external addon are computed only at mirror time, so
// toggling the pooler without a re-mirror left POOLER_URL empty after
// enable and pointing at the deleted pooler Service after disable.
func TestUpdate_PoolerToggleRemirrorsExternalConn(t *testing.T) {
	t.Parallel()
	s := fakeServiceWithSecrets(t, seedProj("alpha"))
	addExternalPSDB(t, s)
	if got := connKey(t, s, "POOLER_URL"); got != "" {
		t.Fatalf("POOLER_URL before enable = %q", got)
	}
	on, off := true, false
	if _, err := s.Update(context.Background(), "alpha", "psdb", UpdateAddonRequest{Pooler: &AddonPoolerPatch{Enabled: &on}}); err != nil {
		t.Fatal(err)
	}
	if got := connKey(t, s, "POOLER_URL"); !strings.Contains(got, "alpha-psdb-pooler:6432") {
		t.Fatalf("POOLER_URL after enable = %q", got)
	}
	if _, err := s.Update(context.Background(), "alpha", "psdb", UpdateAddonRequest{Pooler: &AddonPoolerPatch{Enabled: &off}}); err != nil {
		t.Fatal(err)
	}
	if got := connKey(t, s, "POOLER_URL"); got != "" {
		t.Fatalf("POOLER_URL after disable = %q, want removed", got)
	}
}

// A credential rotation through resync-external must roll the envs that
// mount the conn Secret; envFrom only resolves at container start.
func TestResyncExternal_RestartsConsumersOnChange(t *testing.T) {
	t.Parallel()
	s := fakeServiceWithSecrets(t, seedProj("alpha"),
		seedEnvWithSecrets("alpha", "web", "production", "alpha-web-production", nil, []string{"alpha-psdb-conn"}),
		seedEnvWithSecrets("alpha", "api", "production", "alpha-api-production", []string{"other"}, []string{"alpha-other-conn"}),
	)
	addExternalPSDB(t, s)
	for _, name := range []string{"alpha-web-production", "alpha-api-production"} {
		if _, err := s.Kube.Clientset.AppsV1().Deployments("kuso").Create(context.Background(),
			&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "kuso"}}, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	restarted := func(name string) bool {
		d, err := s.Kube.Clientset.AppsV1().Deployments("kuso").Get(context.Background(), name, metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		return d.Spec.Template.Annotations["kuso.sislelabs.com/restartedAt"] != ""
	}

	// No change: nothing rolls.
	if err := s.ResyncExternal(context.Background(), "alpha", "psdb", nil); err != nil {
		t.Fatal(err)
	}
	if restarted("alpha-web-production") {
		t.Fatal("a resync that changed nothing restarted pods")
	}

	if err := s.ResyncExternal(context.Background(), "alpha", "psdb", map[string]string{"POSTGRES_PASSWORD": "rotated"}); err != nil {
		t.Fatal(err)
	}
	if !restarted("alpha-web-production") {
		t.Fatal("consumer of the rotated conn secret was not restarted")
	}
	if restarted("alpha-api-production") {
		t.Fatal("env that doesn't mount the conn secret was restarted")
	}
}

// "Revert to rev N" replayed only the fields rev N's patch touched, so a
// later change (here: pooler on) survived the revert.
func TestUpdate_RevisionSnapshotIsFullState(t *testing.T) {
	t.Parallel()
	s := fakeService(t, seedProj("alpha"), seedAddonSpec("alpha", "db", kube.KusoAddonSpec{Kind: "postgres", Project: "alpha"}))
	var snaps []json.RawMessage
	s.RecordRevision = func(_ context.Context, _, _, _, _ string, snap []byte) {
		snaps = append(snaps, snap)
	}
	sched := "0 3 * * *"
	on := true
	if _, err := s.Update(context.Background(), "alpha", "db", UpdateAddonRequest{Backup: &UpdateBackupPatch{Schedule: &sched}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Update(context.Background(), "alpha", "db", UpdateAddonRequest{Pooler: &AddonPoolerPatch{Enabled: &on}}); err != nil {
		t.Fatal(err)
	}
	var rev1 struct {
		Patch json.RawMessage `json:"patch"`
	}
	if err := json.Unmarshal(snaps[0], &rev1); err != nil {
		t.Fatal(err)
	}
	if err := s.RevertAddon(context.Background(), "alpha", "db", rev1.Patch); err != nil {
		t.Fatal(err)
	}
	a, err := s.Kube.GetKusoAddon(context.Background(), "kuso", "alpha-db")
	if err != nil {
		t.Fatal(err)
	}
	if a.Spec.Pooler != nil && a.Spec.Pooler.Enabled {
		t.Error("revert to rev 1 kept the pooler enabled by rev 2")
	}
	if a.Spec.Backup == nil || a.Spec.Backup.Schedule != sched {
		t.Errorf("revert to rev 1 lost its backup schedule: %+v", a.Spec.Backup)
	}
}
