package kusoCli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestRenderBackupHealth(t *testing.T) {
	body := `{"backup":{"healthy":false,"schedule":"0 3 * * *","detail":"CronJob suspended"},
	"registryGC":{"healthy":true,"schedule":"0 4 * * 0","lastSuccessAt":"2026-09-27T04:00:00Z"},
	"addonBackups":[{"addon":"shop-db","project":"shop","kind":"postgres","schedule":"0 2 * * *","covered":true,"healthy":true,"lastSuccessAt":"2026-09-28T02:00:00Z"}],
	"addonBackupsComplete":true,
	"serviceVolumes":[{"service":"shop-web","project":"shop","volumes":["data"],"covered":false}],
	"serviceVolumesComplete":true}`
	var h backupHealthView
	if err := json.Unmarshal([]byte(body), &h); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	renderBackupHealth(&buf, h)
	out := buf.String()
	for _, want := range []string{
		"control-plane DB backup: FAILING  schedule=0 3 * * *",
		"CronJob suspended",
		"registry GC: ok",
		"shop/db", "postgres",
		"shop/web", "data",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "incomplete") {
		t.Errorf("complete sweep should not warn:\n%s", out)
	}
}

func TestRenderBackupSettingsHidesSecret(t *testing.T) {
	var buf bytes.Buffer
	renderBackupSettings(&buf, map[string]any{"bucket": "b", "endpoint": "https://s3", "hasSecret": true})
	out := buf.String()
	if !strings.Contains(out, "bucket:            b") || !strings.Contains(out, "set (not shown)") || !strings.Contains(out, "region:            -") {
		t.Errorf("unexpected:\n%s", out)
	}
}
