package kube

import (
	"errors"
	"io"
	"os"
	"regexp"
	"slices"
	"testing"

	"gopkg.in/yaml.v3"
)

// The audit admission policy re-states allowedAddCapabilities in CEL. If
// the two drift, the policy flags pods kuso itself rendered (or misses
// ones it rejects), so this fails on any difference.
func TestAdmissionPolicyCapAllowlistInSync(t *testing.T) {
	f, err := os.Open("../../../deploy/kuso-admission-policy.yaml")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	type policy struct {
		Kind     string `yaml:"kind"`
		Metadata struct {
			Name string `yaml:"name"`
		} `yaml:"metadata"`
		Spec struct {
			Variables []struct {
				Name       string `yaml:"name"`
				Expression string `yaml:"expression"`
			} `yaml:"variables"`
		} `yaml:"spec"`
	}
	var expr string
	dec := yaml.NewDecoder(f)
	for {
		var p policy
		if err := dec.Decode(&p); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		if p.Kind != "ValidatingAdmissionPolicy" || p.Metadata.Name != "kuso-pod-hardening-audit" {
			continue
		}
		for _, v := range p.Spec.Variables {
			if v.Name == "allowedCapabilities" {
				expr = v.Expression
			}
		}
	}
	if expr == "" {
		t.Fatal("kuso-pod-hardening-audit policy or its allowedCapabilities variable not found")
	}

	var cel []string
	for _, m := range regexp.MustCompile(`'([^']*)'`).FindAllStringSubmatch(expr, -1) {
		cel = append(cel, m[1])
	}
	var goCaps []string
	for c := range allowedAddCapabilities {
		goCaps = append(goCaps, c)
	}
	slices.Sort(cel)
	slices.Sort(goCaps)
	if !slices.Equal(cel, goCaps) {
		t.Fatalf("CEL allowlist %v != allowedAddCapabilities %v", cel, goCaps)
	}
}
