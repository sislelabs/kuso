package marketplace

import (
	"errors"
	"testing"
)

func TestParseManifest_Valid(t *testing.T) {
	raw := []byte(`name: umami
title: Umami
description: Privacy-friendly analytics.
category: analytics
website: https://umami.is
appVersion: "2.13"
prompts:
  - key: admin_email
    title: Admin email
    kind: string
    required: true
`)
	m, err := ParseManifest(raw)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if m.Name != "umami" || m.Title != "Umami" || m.Category != "analytics" {
		t.Fatalf("bad manifest: %+v", m)
	}
	if len(m.Prompts) != 1 || m.Prompts[0].Key != "admin_email" || !m.Prompts[0].Required {
		t.Fatalf("bad prompts: %+v", m.Prompts)
	}
}

func TestParseManifest_Errors(t *testing.T) {
	cases := map[string]string{
		"missing name":      "title: X\ndescription: d\ncategory: data\n",
		"bad slug":          "name: Umami!\ntitle: X\ndescription: d\ncategory: data\n",
		"bad category":      "name: umami\ntitle: X\ndescription: d\ncategory: nope\n",
		"missing title":     "name: umami\ndescription: d\ncategory: data\n",
		"bad prompt key":    "name: umami\ntitle: X\ndescription: d\ncategory: data\nprompts:\n  - key: Bad-Key\n    title: T\n    kind: string\n",
		"bad prompt kind":   "name: umami\ntitle: X\ndescription: d\ncategory: data\nprompts:\n  - key: k\n    title: T\n    kind: secret\n",
		"dup prompt key":    "name: umami\ntitle: X\ndescription: d\ncategory: data\nprompts:\n  - key: k\n    title: A\n    kind: string\n  - key: k\n    title: B\n    kind: string\n",
		"unknown field":     "name: umami\ntitle: X\ndescription: d\ncategory: data\nbogus: 1\n",
		"bad size":          "name: umami\ntitle: X\ndescription: d\ncategory: data\nsize: Huge!\n",
	}
	for label, raw := range cases {
		if _, err := ParseManifest([]byte(raw)); !errors.Is(err, ErrInvalidManifest) {
			t.Errorf("%s: want ErrInvalidManifest, got %v", label, err)
		}
	}
}

func TestParseManifest_Size(t *testing.T) {
	m, err := ParseManifest([]byte("name: metabase\ntitle: M\ndescription: d\ncategory: data\nsize: large\n"))
	if err != nil {
		t.Fatal(err)
	}
	if m.Size != "large" {
		t.Fatalf("size = %q, want large", m.Size)
	}
}

// Memory-heavy catalog apps must not deploy at the default pod size.
func TestCatalog_HeavyAppsDeclareSize(t *testing.T) {
	want := map[string]string{"metabase": "large", "n8n": "large", "gitea": "medium", "plausible": "medium"}
	for app, size := range want {
		e, err := GetEntry(app)
		if err != nil {
			t.Fatalf("%s: %v", app, err)
		}
		if e.Manifest.Size != size {
			t.Errorf("%s size = %q, want %q", app, e.Manifest.Size, size)
		}
	}
}
