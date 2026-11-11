package kusoCli

import (
	"fmt"
	"io"
	"strings"

	"github.com/olekukonko/tablewriter"
)

// Per-command output vars: the shared outputFormat must only ever be
// bound with a "table" default (see output_format_test.go), and these
// commands used to be JSON-only.
var (
	backupHealthOutput   string
	backupSettingsOutput string
)

type backupJobStatus struct {
	Configured     bool   `json:"configured"`
	CronJobPresent bool   `json:"cronJobPresent"`
	Suspended      bool   `json:"suspended"`
	Schedule       string `json:"schedule"`
	LastSuccessAt  string `json:"lastSuccessAt"`
	LastFailureAt  string `json:"lastFailureAt"`
	Healthy        bool   `json:"healthy"`
	Detail         string `json:"detail"`
}

type backupHealthView struct {
	Backup       backupJobStatus `json:"backup"`
	RegistryGC   backupJobStatus `json:"registryGC"`
	AddonBackups []struct {
		Addon         string `json:"addon"`
		Project       string `json:"project"`
		Kind          string `json:"kind"`
		Schedule      string `json:"schedule"`
		Covered       bool   `json:"covered"`
		LastSuccessAt string `json:"lastSuccessAt"`
		Healthy       bool   `json:"healthy"`
		Detail        string `json:"detail"`
	} `json:"addonBackups"`
	AddonBackupsComplete bool `json:"addonBackupsComplete"`
	ServiceVolumes       []struct {
		Service string   `json:"service"`
		Project string   `json:"project"`
		Volumes []string `json:"volumes"`
		Covered bool     `json:"covered"`
		Detail  string   `json:"detail"`
	} `json:"serviceVolumes"`
	ServiceVolumesComplete bool `json:"serviceVolumesComplete"`
	Bucket                 *struct {
		Bucket           string   `json:"bucket"`
		InsecureEndpoint bool     `json:"insecureEndpoint"`
		Versioning       string   `json:"versioning"`
		ObjectLock       string   `json:"objectLock"`
		ScopedProjects   []string `json:"scopedProjects"`
		Warnings         []string `json:"warnings"`
	} `json:"bucket"`
}

func healthWord(ok bool) string {
	if ok {
		return "ok"
	}
	return "FAILING"
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func jobLine(w io.Writer, label string, s backupJobStatus) {
	fmt.Fprintf(w, "%s: %s  schedule=%s  last success=%s\n", label, healthWord(s.Healthy), orDash(s.Schedule), orDash(s.LastSuccessAt))
	if s.Detail != "" {
		fmt.Fprintf(w, "  %s\n", s.Detail)
	}
}

// renderBackupHealth prints the backup-health response as two status
// lines plus per-addon and per-volume tables.
func renderBackupHealth(w io.Writer, h backupHealthView) {
	jobLine(w, "control-plane DB backup", h.Backup)
	jobLine(w, "registry GC", h.RegistryGC)
	if b := h.Bucket; b != nil {
		scoped := "none"
		if len(b.ScopedProjects) > 0 {
			scoped = strings.Join(b.ScopedProjects, ", ")
		}
		fmt.Fprintf(w, "backup bucket %s: versioning=%s  object lock=%s  plain http=%s  own-key projects=%s\n",
			b.Bucket, orDash(b.Versioning), orDash(b.ObjectLock), boolText(b.InsecureEndpoint), scoped)
		for _, warn := range b.Warnings {
			fmt.Fprintf(w, "  WARNING: %s\n", warn)
		}
	}

	fmt.Fprintln(w, "\naddon backups:")
	if len(h.AddonBackups) == 0 {
		fmt.Fprintln(w, "  none")
	} else {
		t := tablewriter.NewWriter(w)
		t.SetHeader([]string{"ADDON", "KIND", "SCHEDULE", "COVERED", "LAST SUCCESS", "HEALTH", "DETAIL"})
		for _, a := range h.AddonBackups {
			name := a.Addon
			if a.Project != "" {
				name = a.Project + "/" + strings.TrimPrefix(a.Addon, a.Project+"-")
			}
			t.Append([]string{name, a.Kind, orDash(a.Schedule), boolText(a.Covered), orDash(a.LastSuccessAt), healthWord(a.Healthy), a.Detail})
		}
		t.Render()
	}
	if !h.AddonBackupsComplete {
		fmt.Fprintln(w, "  (incomplete: a lookup failed, rows may be missing)")
	}

	if len(h.ServiceVolumes) > 0 {
		fmt.Fprintln(w, "\nservice volumes (not backed up by kuso):")
		t := tablewriter.NewWriter(w)
		t.SetHeader([]string{"SERVICE", "VOLUMES", "COVERED"})
		for _, v := range h.ServiceVolumes {
			name := v.Service
			if v.Project != "" {
				name = v.Project + "/" + strings.TrimPrefix(v.Service, v.Project+"-")
			}
			t.Append([]string{name, strings.Join(v.Volumes, ", "), boolText(v.Covered)})
		}
		t.Render()
	}
	if !h.ServiceVolumesComplete {
		fmt.Fprintln(w, "  (service volume list incomplete: a lookup failed)")
	}
}

// renderBackupSettings prints the S3 settings as key: value lines.
func renderBackupSettings(w io.Writer, s map[string]any) {
	for _, k := range []string{"bucket", "endpoint", "region", "accessKeyId"} {
		fmt.Fprintf(w, "%-18s %s\n", k+":", orDash(asString(s[k])))
	}
	secret := "not set"
	if b, _ := s["hasSecret"].(bool); b {
		secret = "set (not shown)"
	}
	fmt.Fprintf(w, "%-18s %s\n", "secretAccessKey:", secret)
	if b, _ := s["allowInsecureHttp"].(bool); b {
		fmt.Fprintf(w, "%-18s %s\n", "plain http:", "allowed (backups travel unencrypted)")
	}
}
