package marketplace

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"errors"
	"io"
	"slices"
	"testing"
)

// lettermarkApps have no brand mark in Simple Icons; the UI draws a
// tinted lettermark tile for them. Adding an icon.svg for one of these
// means removing it here.
var lettermarkApps = []string{
	"ackee", "cyberchef", "filebrowser", "gotify", "healthchecks", "homer",
	"it-tools", "kroki", "langfuse", "linkding", "miniflux", "navidrome",
	"nocodb", "open-webui", "openspeedtest", "pgadmin", "privatebin",
	"shlink", "thelounge", "typesense",
}

func TestCatalog_EveryAppHasIconOrIsLettermark(t *testing.T) {
	apps, err := Catalog()
	if err != nil {
		t.Fatalf("Catalog: %v", err)
	}
	names := map[string]bool{}
	for _, m := range apps {
		names[m.Name] = true
		e, err := GetEntry(m.Name)
		if err != nil {
			t.Fatalf("%s: %v", m.Name, err)
		}
		listed := slices.Contains(lettermarkApps, m.Name)
		switch {
		case len(e.Icon) == 0 && !listed:
			t.Errorf("%s: no icon.svg and not in lettermarkApps", m.Name)
		case len(e.Icon) > 0 && listed:
			t.Errorf("%s: has icon.svg, drop it from lettermarkApps", m.Name)
		case len(e.Icon) > 0:
			if err := checkSVG(e.Icon); err != nil {
				t.Errorf("%s: icon.svg: %v", m.Name, err)
			}
		}
	}
	for _, n := range lettermarkApps {
		if !names[n] {
			t.Errorf("lettermarkApps lists %q, which is not in the catalog", n)
		}
	}
}

// checkSVG requires well-formed XML whose root is an <svg> element.
func checkSVG(raw []byte) error {
	dec := xml.NewDecoder(bytes.NewReader(raw))
	root := ""
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		if se, ok := tok.(xml.StartElement); ok && root == "" {
			root = se.Name.Local
		}
	}
	if root != "svg" {
		return errors.New("root element is " + root + ", want svg")
	}
	return nil
}

func TestCheckSVG_RejectsBrokenMarkup(t *testing.T) {
	for _, bad := range []string{`<svg><path></svg>`, `<html></html>`, ``} {
		if checkSVG([]byte(bad)) == nil {
			t.Errorf("checkSVG(%q) accepted invalid input", bad)
		}
	}
}

func TestCatalog_SetupRiskFlags(t *testing.T) {
	want := map[string]struct {
		firstRun bool
		creds    string
	}{
		"umami":       {creds: "admin/umami"},
		"n8n":         {firstRun: true},
		"metabase":    {firstRun: true},
		"uptime-kuma": {firstRun: true},
		"plausible":   {firstRun: true},
	}
	for name, w := range want {
		e, err := GetEntry(name)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if e.Manifest.FirstRunSetup != w.firstRun || e.Manifest.DefaultCredentials != w.creds {
			t.Errorf("%s: firstRunSetup=%v defaultCredentials=%q, want %v %q",
				name, e.Manifest.FirstRunSetup, e.Manifest.DefaultCredentials, w.firstRun, w.creds)
		}
	}

	// The flags are additive wire fields: present when set, absent otherwise.
	umami, _ := GetEntry("umami")
	n8n, _ := GetEntry("n8n")
	grafana, _ := GetEntry("grafana")
	for _, c := range []struct {
		m        *Manifest
		has, not string
	}{
		{umami.Manifest, `"defaultCredentials":"admin/umami"`, `"firstRunSetup"`},
		{n8n.Manifest, `"firstRunSetup":true`, `"defaultCredentials"`},
		{grafana.Manifest, ``, `"firstRunSetup"`},
	} {
		b, err := json.Marshal(c.m)
		if err != nil {
			t.Fatal(err)
		}
		if c.has != "" && !bytes.Contains(b, []byte(c.has)) {
			t.Errorf("%s: JSON missing %s: %s", c.m.Name, c.has, b)
		}
		if bytes.Contains(b, []byte(c.not)) {
			t.Errorf("%s: JSON should omit %s: %s", c.m.Name, c.not, b)
		}
	}
}
