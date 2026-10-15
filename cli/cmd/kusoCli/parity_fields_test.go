package kusoCli

import (
	"encoding/json"
	"testing"

	"github.com/spf13/cobra"

	"kuso/pkg/kusoApi"
)

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestAddRemoveVolume(t *testing.T) {
	cur := []kusoApi.VolumePatch{{Name: "data", MountPath: "/data", SizeGi: 5, StorageClass: "local-path", AccessMode: "ReadWriteOnce"}}
	got, err := addVolume(cur, kusoApi.VolumePatch{Name: "cache", MountPath: "/cache", SizeGi: 1})
	if err != nil {
		t.Fatal(err)
	}
	// The existing entry must be resent intact: the server replaces the
	// whole list, so dropping it would unmount the volume.
	want := `[{"name":"data","mountPath":"/data","sizeGi":5,"storageClass":"local-path","accessMode":"ReadWriteOnce"},{"name":"cache","mountPath":"/cache","sizeGi":1}]`
	if s := mustJSON(t, got); s != want {
		t.Errorf("add:\n got  %s\n want %s", s, want)
	}
	if len(cur) != 1 {
		t.Errorf("addVolume mutated its input")
	}
	if _, err := addVolume(cur, kusoApi.VolumePatch{Name: "data", MountPath: "/other"}); err == nil {
		t.Error("duplicate name should fail")
	}
	if _, err := addVolume(cur, kusoApi.VolumePatch{Name: "other", MountPath: "/data"}); err == nil {
		t.Error("duplicate mount path should fail")
	}
	left, err := removeVolume(got, "data")
	if err != nil {
		t.Fatal(err)
	}
	if s := mustJSON(t, left); s != `[{"name":"cache","mountPath":"/cache","sizeGi":1}]` {
		t.Errorf("rm: got %s", s)
	}
	if _, err := removeVolume(got, "nope"); err == nil {
		t.Error("removing a missing volume should fail")
	}
	// rm of the last volume must send an empty list, not null (null = leave alone).
	none, _ := removeVolume(cur, "data")
	if s := mustJSON(t, kusoApi.PatchServiceRequest{Volumes: &none}); s != `{"volumes":[]}` {
		t.Errorf("rm last: got %s", s)
	}
}

func TestBuildServicePlacement(t *testing.T) {
	p, err := buildServicePlacement([]string{"region=eu", "disk=ssd"}, []string{"worker-3"})
	if err != nil {
		t.Fatal(err)
	}
	if s := mustJSON(t, kusoApi.PatchServiceRequest{Placement: p}); s != `{"placement":{"labels":{"disk":"ssd","region":"eu"},"nodes":["worker-3"]}}` {
		t.Errorf("got %s", s)
	}
	if _, err := buildServicePlacement(nil, nil); err == nil {
		t.Error("empty set should fail")
	}
	if _, err := buildServicePlacement([]string{"novalue"}, nil); err == nil {
		t.Error("label without = should fail")
	}
}

func poolerCmd(t *testing.T, argv ...string) *cobra.Command {
	t.Helper()
	c := &cobra.Command{Use: "pooler"}
	addPoolerSettingFlags(c)
	if err := c.ParseFlags(argv); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestAddonPoolerPatch(t *testing.T) {
	p, err := addonPoolerPatch(poolerCmd(t), kusoApi.BoolPtr(true))
	if err != nil {
		t.Fatal(err)
	}
	if s := mustJSON(t, kusoApi.UpdateAddonRequest{Pooler: p}); s != `{"pooler":{"enabled":true}}` {
		t.Errorf("enable: got %s", s)
	}
	p, err = addonPoolerPatch(poolerCmd(t, "--external-backend=on", "--host", "h.psdb.cloud", "--port", "5432", "--pool-size", "6"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if s := mustJSON(t, kusoApi.UpdateAddonRequest{Pooler: p}); s != `{"pooler":{"externalBackend":true,"host":"h.psdb.cloud","port":5432,"poolSize":6}}` {
		t.Errorf("set: got %s", s)
	}
	// --pool-size 0 is a real value (back to the default), not "unset".
	p, _ = addonPoolerPatch(poolerCmd(t, "--pool-size", "0"), nil)
	if s := mustJSON(t, kusoApi.UpdateAddonRequest{Pooler: p}); s != `{"pooler":{"poolSize":0}}` {
		t.Errorf("pool-size 0: got %s", s)
	}
	if _, err := addonPoolerPatch(poolerCmd(t), nil); err == nil {
		t.Error("set with no flags should fail")
	}
	if _, err := addonPoolerPatch(poolerCmd(t, "--external-backend", "maybe"), nil); err == nil {
		t.Error("bad on/off should fail")
	}
}

func cronEditFlags(t *testing.T, argv ...string) *cobra.Command {
	t.Helper()
	c := &cobra.Command{Use: "edit"}
	c.Flags().StringVar(&pCronOnFailureWebhook, "on-failure-webhook", "", "")
	c.Flags().StringVar(&pCronOnFailureSecret, "on-failure-secret", "", "")
	c.Flags().BoolVar(&pCronOnFailureClear, "on-failure-clear", false, "")
	if err := c.ParseFlags(argv); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestCronOnFailureUpdate(t *testing.T) {
	cases := []struct {
		name    string
		argv    []string
		want    string
		wantErr bool
	}{
		{"none", nil, `null`, false},
		{"url", []string{"--on-failure-webhook", "https://h/x"}, `{"webhookURL":"https://h/x"}`, false},
		{"url+secret", []string{"--on-failure-webhook", "https://h/x", "--on-failure-secret", "hook:token"}, `{"webhookURL":"https://h/x","secretRef":{"name":"hook","key":"token"}}`, false},
		{"clear", []string{"--on-failure-clear"}, `{"clear":true}`, false},
		{"secret alone", []string{"--on-failure-secret", "hook:token"}, "", true},
		{"bad secret", []string{"--on-failure-webhook", "https://h/x", "--on-failure-secret", "hook"}, "", true},
		{"clear plus url", []string{"--on-failure-clear", "--on-failure-webhook", "https://h/x"}, "", true},
	}
	for _, c := range cases {
		got, err := cronOnFailureUpdate(cronEditFlags(t, c.argv...))
		if c.wantErr {
			if err == nil {
				t.Errorf("%s: want error", c.name)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if s := mustJSON(t, got); s != c.want {
			t.Errorf("%s: got %s want %s", c.name, s, c.want)
		}
	}
}
