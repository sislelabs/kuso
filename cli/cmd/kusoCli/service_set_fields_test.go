package kusoCli

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"kuso/pkg/kusoApi"
)

// runSetFields parses argv against a fresh `service set`-shaped flag set
// (fresh registration also resets the package vars to their defaults) and
// returns the PATCH body applyServiceSetFields builds.
func runSetFields(t *testing.T, cur *currentServiceSpec, argv ...string) (string, error) {
	t.Helper()
	cmd := &cobra.Command{Use: "set"}
	addServiceSetFieldFlags(cmd)
	if err := cmd.ParseFlags(argv); err != nil {
		t.Fatalf("parse %v: %v", argv, err)
	}
	fetched := 0
	req := kusoApi.PatchServiceRequest{}
	err := applyServiceSetFields(cmd, &req, func() (*currentServiceSpec, error) {
		fetched++
		if cur == nil {
			return nil, errors.New("no current spec in this test")
		}
		return cur, nil
	})
	if fetched > 1 {
		t.Errorf("current spec fetched %d times, want at most 1", fetched)
	}
	if err != nil {
		return "", err
	}
	b, _ := json.Marshal(req)
	return string(b), nil
}

func specFromJSON(t *testing.T, s string) *currentServiceSpec {
	t.Helper()
	var c currentServiceSpec
	if err := json.Unmarshal([]byte(s), &c); err != nil {
		t.Fatal(err)
	}
	return &c
}

func TestApplyServiceSetFields(t *testing.T) {
	cur := specFromJSON(t, `{"spec":{
		"release":{"command":["./migrate"],"timeoutSeconds":300},
		"static":{"builderImage":"node:22","buildCmd":"npm run build","outputDir":"dist"},
		"buildpacks":{"builderImage":"old","lifecycleImage":"lc:1"},
		"image":{"repository":"ghcr.io/acme/app","tag":"v1"}}}`)
	cases := []struct {
		name string
		argv []string
		want string
	}{
		{"command", []string{"--command", "./worker", "--command", "--queue=a"}, `{"command":["./worker","--queue=a"]}`},
		{"command clear", []string{"--command", ""}, `{"command":[]}`},
		{"dockerfile", []string{"--dockerfile", "Dockerfile.prod"}, `{"dockerfile":"Dockerfile.prod"}`},
		{"release both", []string{"--release-cmd", `sh -c "npm run migrate"`, "--release-timeout", "120"}, `{"release":{"command":["sh","-c","npm run migrate"],"timeoutSeconds":120}}`},
		{"release timeout keeps command", []string{"--release-timeout", "600"}, `{"release":{"command":["./migrate"],"timeoutSeconds":600}}`},
		{"release cmd keeps timeout", []string{"--release-cmd", "./bin/m"}, `{"release":{"command":["./bin/m"],"timeoutSeconds":300}}`},
		{"release clear", []string{"--release-clear"}, `{"release":{"clear":true}}`},
		{"snapshot on", []string{"--snapshot-before-deploy=on"}, `{"snapshotBeforeDeploy":true}`},
		{"snapshot off", []string{"--snapshot-before-deploy=off"}, `{"snapshotBeforeDeploy":false}`},
		{"previews off", []string{"--previews=off"}, `{"previews":{"disabled":true}}`},
		{"previews on", []string{"--previews=on"}, `{"previews":{"disabled":false}}`},
		{"build args", []string{"--build-arg", "A=1", "--build-arg", "B=x=y"}, `{"buildArgs":{"A":"1","B":"x=y"}}`},
		{"build args clear", []string{"--build-args-clear"}, `{"buildArgs":{}}`},
		{"public env", []string{"--public-env", "NEXT_PUBLIC_A", "--public-env", "NEXT_PUBLIC_B"}, `{"publicEnv":["NEXT_PUBLIC_A","NEXT_PUBLIC_B"]}`},
		{"public env clear", []string{"--public-env-clear"}, `{"publicEnv":[]}`},
		{"static keeps siblings", []string{"--static-output-dir", "out"}, `{"static":{"builderImage":"node:22","buildCmd":"npm run build","outputDir":"out"}}`},
		{"buildpacks keeps lifecycle", []string{"--buildpacks-builder", "new"}, `{"buildpacks":{"builderImage":"new","lifecycleImage":"lc:1"}}`},
		{"image tag keeps repo", []string{"--image-tag", "v2"}, `{"image":{"repository":"ghcr.io/acme/app","tag":"v2"}}`},
		{"image repo keeps tag", []string{"--image-repo", "ghcr.io/acme/other"}, `{"image":{"repository":"ghcr.io/acme/other","tag":"v1"}}`},
		{"target cpu", []string{"--target-cpu", "60"}, `{"scale":{"targetCPU":60}}`},
		{"nothing", nil, `{}`},
	}
	for _, c := range cases {
		got, err := runSetFields(t, cur, c.argv...)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if got != c.want {
			t.Errorf("%s:\n got  %s\n want %s", c.name, got, c.want)
		}
	}
}

func TestApplyServiceSetFieldsErrors(t *testing.T) {
	noRelease := specFromJSON(t, `{"spec":{}}`)
	cases := []struct {
		name string
		argv []string
	}{
		{"timeout without hook", []string{"--release-timeout", "60"}},
		{"bad timeout", []string{"--release-cmd", "x", "--release-timeout", "0"}},
		{"clear plus cmd", []string{"--release-clear", "--release-cmd", "x"}},
		{"empty release cmd", []string{"--release-cmd", ""}},
		{"bad on/off", []string{"--snapshot-before-deploy", "maybe"}},
		{"bad previews", []string{"--previews", "inherit"}},
		{"build arg no =", []string{"--build-arg", "A"}},
		{"build arg dup", []string{"--build-arg", "A=1", "--build-arg", "A=2"}},
		{"build args clear plus arg", []string{"--build-args-clear", "--build-arg", "A=1"}},
		{"empty public env", []string{"--public-env", " "}},
		{"image tag without image", []string{"--image-tag", "v2"}},
	}
	for _, c := range cases {
		if got, err := runSetFields(t, noRelease, c.argv...); err == nil {
			t.Errorf("%s: want error, got %s", c.name, got)
		}
	}
}

// A field that needs no current spec must not fetch it.
func TestApplyServiceSetFieldsNoFetchForPlainFields(t *testing.T) {
	if _, err := runSetFields(t, nil, "--dockerfile", "D", "--release-cmd", "x", "--release-timeout", "5", "--build-arg", "A=1"); err != nil {
		t.Fatal(err)
	}
}

func TestApplyServiceAddFields(t *testing.T) {
	cmd := &cobra.Command{Use: "add"}
	addServiceAddFieldFlags(cmd)
	if err := cmd.ParseFlags([]string{
		"--release-cmd", "./migrate up", "--release-timeout", "60", "--snapshot-before-deploy=on",
		"--build-arg", "A=1", "--public-env", "NEXT_PUBLIC_X",
		"--static-build-cmd", "npm run build", "--static-output-dir", "dist",
		"--buildpacks-builder", "b",
	}); err != nil {
		t.Fatal(err)
	}
	req := kusoApi.CreateServiceRequest{Name: "web"}
	if err := applyServiceAddFields(cmd, &req); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(req)
	want := `{"name":"web","static":{"buildCmd":"npm run build","outputDir":"dist"},"buildpacks":{"builderImage":"b"},"release":{"command":["./migrate","up"],"timeoutSeconds":60},"snapshotBeforeDeploy":true,"buildArgs":{"A":"1"},"publicEnv":["NEXT_PUBLIC_X"]}`
	if string(b) != want {
		t.Errorf("\n got  %s\n want %s", b, want)
	}

	cmd = &cobra.Command{Use: "add"}
	addServiceAddFieldFlags(cmd)
	_ = cmd.ParseFlags([]string{"--release-timeout", "60"})
	if err := applyServiceAddFields(cmd, &kusoApi.CreateServiceRequest{}); err == nil {
		t.Error("--release-timeout without --release-cmd should fail")
	}
}

// Both `service set` shells must register every field flag, or the shared
// RunE silently never sees it as Changed() on one of them.
func TestServiceSetShellsRegisterFieldFlags(t *testing.T) {
	probe := &cobra.Command{Use: "probe"}
	addServiceSetFieldFlags(probe)
	probe.Flags().VisitAll(func(f *pflag.Flag) {
		for _, c := range []*cobra.Command{serviceSetCmd, serviceSetTopCmd} {
			if c.Flags().Lookup(f.Name) == nil {
				t.Errorf("%s is missing --%s", c.CommandPath(), f.Name)
			}
		}
	})
	addProbe := &cobra.Command{Use: "probe"}
	addServiceAddFieldFlags(addProbe)
	addProbe.Flags().VisitAll(func(f *pflag.Flag) {
		for _, c := range []*cobra.Command{serviceAddCmd, serviceAddTopCmd} {
			if c.Flags().Lookup(f.Name) == nil {
				t.Errorf("%s is missing --%s", c.CommandPath(), f.Name)
			}
		}
	})
}
