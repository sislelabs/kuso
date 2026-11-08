package kusoApi

import (
	"encoding/json"
	"net/url"
	"strconv"
	"strings"

	"github.com/go-resty/resty/v2"
)

// Wire shapes for the vulnerability-scanning API. docs/VULN_SCANNING.md
// "HTTP API (contract)" is the source of truth.

type VulnCounts struct {
	Critical        int `json:"critical"`
	High            int `json:"high"`
	Medium          int `json:"medium"`
	Low             int `json:"low"`
	Unknown         int `json:"unknown"`
	FixableCritical int `json:"fixableCritical"`
	FixableHigh     int `json:"fixableHigh"`
}

type VulnScanSummary struct {
	ID           int64      `json:"id"`
	Digest       string     `json:"digest"`
	ImageRef     string     `json:"imageRef"`
	Status       string     `json:"status"`
	Error        string     `json:"error,omitempty"`
	Reason       string     `json:"reason"`
	CreatedAt    string     `json:"createdAt"`
	FinishedAt   string     `json:"finishedAt,omitempty"`
	DBUpdatedAt  string     `json:"dbUpdatedAt,omitempty"`
	Counts       VulnCounts `json:"counts"`
	IgnoredCount int        `json:"ignoredCount"`
}

type VulnFinding struct {
	VulnID           string `json:"vulnId"`
	PkgName          string `json:"pkgName"`
	PkgType          string `json:"pkgType"`
	InstalledVersion string `json:"installedVersion"`
	FixedVersion     string `json:"fixedVersion,omitempty"`
	Severity         string `json:"severity"`
	Title            string `json:"title,omitempty"`
	PrimaryURL       string `json:"primaryURL,omitempty"`
	Target           string `json:"target,omitempty"`
	Ignored          bool   `json:"ignored"`
	IgnoreID         *int64 `json:"ignoreId,omitempty"`
}

type VulnIgnore struct {
	ID        int64  `json:"id"`
	Scope     string `json:"scope"`
	Project   string `json:"project,omitempty"`
	Service   string `json:"service,omitempty"`
	VulnID    string `json:"vulnId"`
	Reason    string `json:"reason"`
	ExpiresAt string `json:"expiresAt,omitempty"`
	CreatedBy string `json:"createdBy"`
	CreatedAt string `json:"createdAt"`
	Expired   bool   `json:"expired"`
}

type VulnImageReport struct {
	OwnerKind string           `json:"ownerKind"`
	Project   string           `json:"project,omitempty"`
	Service   string           `json:"service,omitempty"`
	Env       string           `json:"env,omitempty"`
	Addon     string           `json:"addon,omitempty"`
	Namespace string           `json:"namespace,omitempty"`
	Workload  string           `json:"workload,omitempty"`
	ImageRef  string           `json:"imageRef"`
	Digest    string           `json:"digest"`
	Scan      *VulnScanSummary `json:"scan"`
}

// VulnProjectReport is GET /api/projects/{p}/vulns. Mode is the instance mode.
type VulnProjectReport struct {
	Mode   string            `json:"mode"`
	Images []VulnImageReport `json:"images"`
}

// VulnPolicyMode is the service's own policy ("inherit" | "enforce" |
// "report" | "off"). The contract types the service report's `policy`
// field as an unspecified `Policy`, so this accepts either the bare mode
// string or the policy-endpoint object ({"mode": …}).
type VulnPolicyMode string

func (m *VulnPolicyMode) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		*m = VulnPolicyMode(s)
		return nil
	}
	var obj struct {
		Mode string `json:"mode"`
	}
	if err := json.Unmarshal(b, &obj); err != nil {
		return err
	}
	*m = VulnPolicyMode(obj.Mode)
	return nil
}

// VulnReport is the service or addon findings response. Policy and
// EffectiveMode are empty for addons.
type VulnReport struct {
	Policy        VulnPolicyMode   `json:"policy,omitempty"`
	EffectiveMode string           `json:"effectiveMode,omitempty"`
	Image         *VulnImageReport `json:"image"`
	Findings      []VulnFinding    `json:"findings"`
	// ImageUpdate is set for addons only.
	ImageUpdate *AddonImageUpdate `json:"imageUpdate,omitempty"`
}

type VulnPolicy struct {
	Mode          string `json:"mode"`
	EffectiveMode string `json:"effectiveMode,omitempty"`
}

type VulnScanQueued struct {
	ScanID int64 `json:"scanId"`
}

type VulnScanAllResult struct {
	Queued int `json:"queued"`
}

type VulnPlatformReport struct {
	Images []VulnImageReport `json:"images"`
}

// VulnSettings is the instance `vulnscan.*` settings object; PUT takes
// the full object.
type VulnSettings struct {
	Mode              string `json:"mode"`
	ScanAddons        bool   `json:"scanAddons"`
	ScanPlatform      bool   `json:"scanPlatform"`
	NightlyHourUTC    int    `json:"nightlyHourUTC"`
	NotifyMinSeverity string `json:"notifyMinSeverity"`
	AutoUpdateAddons  bool   `json:"autoUpdateAddons"`
}

// AddonImageUpdateState is the addon image updater's view of one addon.
type AddonImageUpdateState struct {
	Image          string `json:"image"`
	Running        string `json:"running,omitempty"`
	Latest         string `json:"latest,omitempty"`
	CheckedAt      string `json:"checkedAt,omitempty"`
	Status         string `json:"status"`
	Message        string `json:"message,omitempty"`
	FixableRunning int    `json:"fixableRunning"`
	FixableLatest  int    `json:"fixableLatest"`
	AppliedAt      string `json:"appliedAt,omitempty"`
}

// AddonImageUpdate is the imageUpdate block of an addon's vulns report.
type AddonImageUpdate struct {
	Auto          bool                   `json:"auto"`
	WindowHourUTC int                    `json:"windowHourUTC"`
	Available     bool                   `json:"available"`
	State         *AddonImageUpdateState `json:"state"`
}

type VulnIgnoreRequest struct {
	VulnID        string `json:"vulnId"`
	Reason        string `json:"reason"`
	ExpiresInDays *int   `json:"expiresInDays,omitempty"`
}

// VulnTarget names the owner of a per-image vulns endpoint: a service
// (Service set) or an addon (Addon set).
type VulnTarget struct {
	Project string
	Service string
	Addon   string
}

func (t VulnTarget) base() string {
	p := "/api/projects/" + esc(t.Project)
	if t.Addon != "" {
		return p + "/addons/" + esc(t.Addon) + "/vulns"
	}
	return p + "/services/" + esc(t.Service) + "/vulns"
}

// VulnQuery is the findings filter. Env is service-only; nil
// IncludeIgnored leaves the server default (true).
type VulnQuery struct {
	Env            string
	Severities     []string
	IncludeIgnored *bool
}

func (q VulnQuery) encode() string {
	v := url.Values{}
	if q.Env != "" {
		v.Set("env", q.Env)
	}
	if len(q.Severities) > 0 {
		v.Set("severity", strings.Join(q.Severities, ","))
	}
	if q.IncludeIgnored != nil {
		v.Set("includeIgnored", strconv.FormatBool(*q.IncludeIgnored))
	}
	if len(v) == 0 {
		return ""
	}
	return "?" + v.Encode()
}

func (k *KusoClient) GetProjectVulns(project string) (*resty.Response, error) {
	return k.client.Get("/api/projects/" + esc(project) + "/vulns")
}

func (k *KusoClient) GetVulns(t VulnTarget, q VulnQuery) (*resty.Response, error) {
	return k.client.Get(t.base() + q.encode())
}

// ScanVulns forces a rescan. env is only sent for services; the addon
// endpoint takes no body.
func (k *KusoClient) ScanVulns(t VulnTarget, env string) (*resty.Response, error) {
	if t.Addon == "" {
		body := map[string]string{}
		if env != "" {
			body["env"] = env
		}
		k.client.SetBody(body)
	}
	return k.client.Post(t.base() + "/scan")
}

func (k *KusoClient) GetVulnPolicy(project, service string) (*resty.Response, error) {
	return k.client.Get(VulnTarget{Project: project, Service: service}.base() + "/policy")
}

func (k *KusoClient) SetVulnPolicy(project, service, mode string) (*resty.Response, error) {
	k.client.SetBody(map[string]string{"mode": mode})
	return k.client.Put(VulnTarget{Project: project, Service: service}.base() + "/policy")
}

func (k *KusoClient) ListVulnIgnores(t VulnTarget) (*resty.Response, error) {
	return k.client.Get(t.base() + "/ignores")
}

func (k *KusoClient) AddVulnIgnore(t VulnTarget, req VulnIgnoreRequest) (*resty.Response, error) {
	k.client.SetBody(req)
	return k.client.Post(t.base() + "/ignores")
}

func (k *KusoClient) DeleteVulnIgnore(t VulnTarget, id string) (*resty.Response, error) {
	return k.client.Delete(t.base() + "/ignores/" + esc(id))
}

func (k *KusoClient) GetPlatformVulns() (*resty.Response, error) {
	return k.client.Get("/api/admin/vulns/platform")
}

func (k *KusoClient) GetPlatformVulnFindings(digest string) (*resty.Response, error) {
	return k.client.Get("/api/admin/vulns/platform/findings?digest=" + url.QueryEscape(digest))
}

func (k *KusoClient) ScanAllVulns() (*resty.Response, error) {
	return k.client.Post("/api/admin/vulns/scan-all")
}

func (k *KusoClient) GetVulnScan(id string) (*resty.Response, error) {
	return k.client.Get("/api/admin/vulns/scans/" + esc(id))
}

// UpdateAddonImage pins an addon to its tag's newest image now; the
// addon restarts.
func (k *KusoClient) UpdateAddonImage(project, addon string) (*resty.Response, error) {
	return k.client.Post(VulnTarget{Project: project, Addon: addon}.base() + "/update-image")
}

func (k *KusoClient) GetVulnSettings() (*resty.Response, error) {
	return k.client.Get("/api/admin/settings/vulnscan")
}

func (k *KusoClient) PutVulnSettings(s VulnSettings) (*resty.Response, error) {
	k.client.SetBody(s)
	return k.client.Put("/api/admin/settings/vulnscan")
}
