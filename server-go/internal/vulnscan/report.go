package vulnscan

import (
	"context"
	"sort"
	"time"

	"kuso/server/internal/db"
)

// Wire shapes for the HTTP API (docs/VULN_SCANNING.md, "HTTP API").

// ScanSummary is VulnScanSummary. Counts exclude ignored findings when
// the summary is built for an owner.
type ScanSummary struct {
	ID           int64         `json:"id"`
	Digest       string        `json:"digest"`
	ImageRef     string        `json:"imageRef"`
	Status       string        `json:"status"`
	Error        string        `json:"error,omitempty"`
	Reason       string        `json:"reason"`
	CreatedAt    time.Time     `json:"createdAt"`
	FinishedAt   *time.Time    `json:"finishedAt,omitempty"`
	DBUpdatedAt  *time.Time    `json:"dbUpdatedAt,omitempty"`
	Counts       db.VulnCounts `json:"counts"`
	IgnoredCount int           `json:"ignoredCount"`
}

// FindingJSON is VulnFinding.
type FindingJSON struct {
	VulnID           string `json:"vulnId"`
	PkgName          string `json:"pkgName"`
	PkgType          string `json:"pkgType"`
	InstalledVersion string `json:"installedVersion"`
	FixedVersion     string `json:"fixedVersion,omitempty"`
	Severity         string `json:"severity"`
	Title            string `json:"title,omitempty"`
	PrimaryURL       string `json:"primaryURL,omitempty"`
	Target           string `json:"target,omitempty"`
	PkgPath          string `json:"pkgPath,omitempty"`
	Origin           string `json:"origin,omitempty"`
	Ignored          bool   `json:"ignored"`
	IgnoreID         *int64 `json:"ignoreId,omitempty"`
}

// IgnoreJSON is VulnIgnore.
type IgnoreJSON struct {
	ID        int64      `json:"id"`
	Scope     string     `json:"scope"`
	Project   string     `json:"project,omitempty"`
	Service   string     `json:"service,omitempty"`
	VulnID    string     `json:"vulnId"`
	Reason    string     `json:"reason"`
	ExpiresAt *time.Time `json:"expiresAt,omitempty"`
	CreatedBy string     `json:"createdBy"`
	CreatedAt time.Time  `json:"createdAt"`
	Expired   bool       `json:"expired"`
}

// ImageReport is VulnImageReport.
type ImageReport struct {
	OwnerKind string       `json:"ownerKind"`
	Project   string       `json:"project,omitempty"`
	Service   string       `json:"service,omitempty"`
	Env       string       `json:"env,omitempty"`
	Addon     string       `json:"addon,omitempty"`
	Namespace string       `json:"namespace,omitempty"`
	Workload  string       `json:"workload,omitempty"`
	ImageRef  string       `json:"imageRef"`
	Digest    string       `json:"digest"`
	Scan      *ScanSummary `json:"scan"`
}

func timePtr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

// IgnoreToJSON renders an ignore with its expiry state at now.
func IgnoreToJSON(ig db.VulnIgnore, now time.Time) IgnoreJSON {
	return IgnoreJSON{
		ID: ig.ID, Scope: ig.Scope, Project: ig.Project, Service: ig.Service, VulnID: ig.VulnID,
		Reason: ig.Reason, ExpiresAt: timePtr(ig.ExpiresAt), CreatedBy: ig.CreatedBy, CreatedAt: ig.CreatedAt,
		Expired: ig.Expired(now),
	}
}

// Summarize renders a scan. With findings (the scan's, annotated
// against the owner's ignores) the counts exclude ignored ones; without,
// the stored totals are used.
func Summarize(s db.VulnScan, annotated []Finding) ScanSummary {
	out := ScanSummary{
		ID: s.ID, Digest: s.Digest, ImageRef: s.ImageRef, Status: s.Status, Error: s.Error, Reason: s.Reason,
		CreatedAt: s.CreatedAt, FinishedAt: timePtr(s.FinishedAt), DBUpdatedAt: timePtr(s.DBUpdatedAt), Counts: s.Counts,
	}
	if annotated != nil {
		out.Counts, out.IgnoredCount = CountVisible(annotated)
	}
	return out
}

// FindingsToJSON renders annotated findings, keeping severities in
// severities (all when empty) and dropping ignored ones unless asked.
func FindingsToJSON(fs []Finding, severities map[string]bool, includeIgnored bool) []FindingJSON {
	out := []FindingJSON{}
	for _, f := range fs {
		if len(severities) > 0 && !severities[f.Severity] {
			continue
		}
		if f.Ignored && !includeIgnored {
			continue
		}
		j := FindingJSON{
			VulnID: f.VulnID, PkgName: f.PkgName, PkgType: f.PkgType, InstalledVersion: f.InstalledVersion,
			FixedVersion: f.FixedVersion, Severity: f.Severity, Title: f.Title, PrimaryURL: f.PrimaryURL,
			Target: f.Target, PkgPath: f.PkgPath, Origin: FindingOrigin(f.PkgType, f.PkgPath), Ignored: f.Ignored,
		}
		if f.Ignored {
			id := f.IgnoreID
			j.IgnoreID = &id
		}
		out = append(out, j)
	}
	return out
}

// Reports assembles API responses from the store.
type Reports struct {
	DB  *db.DB
	Now func() time.Time
}

func (r *Reports) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// OwnerIgnores loads the ignores that apply to an image's owner.
func (r *Reports) OwnerIgnores(ctx context.Context, im db.VulnImage) ([]db.VulnIgnore, error) {
	scope, project, name := IgnoreScope(im)
	return r.DB.ListVulnIgnores(ctx, scope, project, name)
}

// ImageReports renders inventory rows with each digest's latest scan.
func (r *Reports) ImageReports(ctx context.Context, imgs []db.VulnImage) ([]ImageReport, error) {
	digests := make([]string, 0, len(imgs))
	for _, im := range imgs {
		digests = append(digests, im.Digest)
	}
	latest, err := r.DB.LatestVulnScans(ctx, digests)
	if err != nil {
		return nil, err
	}
	ignoresByOwner := map[string][]db.VulnIgnore{}
	out := make([]ImageReport, 0, len(imgs))
	for _, im := range imgs {
		rep := imageReport(im)
		if sc, ok := latest[im.Digest]; ok {
			k := OwnerKey(im)
			igs, cached := ignoresByOwner[k]
			if !cached {
				if igs, err = r.OwnerIgnores(ctx, im); err != nil {
					return nil, err
				}
				ignoresByOwner[k] = igs
			}
			sum, err := r.summary(ctx, sc, igs)
			if err != nil {
				return nil, err
			}
			rep.Scan = &sum
		}
		out = append(out, rep)
	}
	return out, nil
}

// summary renders a scan for an owner, recounting only when an active
// ignore could change the numbers.
func (r *Reports) summary(ctx context.Context, sc db.VulnScan, ignores []db.VulnIgnore) (ScanSummary, error) {
	if sc.Status != db.VulnStatusSucceeded || len(activeIgnores(ignores, r.now())) == 0 {
		return Summarize(sc, nil), nil
	}
	fs, err := r.DB.ListVulnFindings(ctx, sc.ID)
	if err != nil {
		return ScanSummary{}, err
	}
	return Summarize(sc, Annotate(fs, ignores, r.now())), nil
}

func imageReport(im db.VulnImage) ImageReport {
	return ImageReport{
		OwnerKind: im.OwnerKind, Project: im.Project, Service: im.Service, Env: im.Env, Addon: im.Addon,
		Namespace: im.Namespace, Workload: im.Workload, ImageRef: im.ImageRef, Digest: im.Digest,
	}
}

// ImageDetail is one image's report plus its findings.
type ImageDetail struct {
	Image    ImageReport
	Findings []Finding
}

// Detail renders one image: the latest scan (any status) in the report,
// and the findings of the latest successful scan, annotated against
// the owner's ignores.
func (r *Reports) Detail(ctx context.Context, im db.VulnImage) (ImageDetail, error) {
	out := ImageDetail{Image: imageReport(im), Findings: []Finding{}}
	ignores, err := r.OwnerIgnores(ctx, im)
	if err != nil {
		return out, err
	}
	latest, err := r.DB.LatestVulnScans(ctx, []string{im.Digest})
	if err != nil {
		return out, err
	}
	sc, ok := latest[im.Digest]
	if !ok {
		return out, nil
	}
	sum, err := r.summary(ctx, sc, ignores)
	if err != nil {
		return out, err
	}
	out.Image.Scan = &sum
	if sc.Status != db.VulnStatusSucceeded {
		prev, err := r.DB.LatestSucceededVulnScans(ctx, []string{im.Digest})
		if err != nil {
			return out, err
		}
		if sc, ok = prev[im.Digest]; !ok {
			return out, nil
		}
	}
	fs, err := r.DB.ListVulnFindings(ctx, sc.ID)
	if err != nil {
		return out, err
	}
	out.Findings = Annotate(fs, ignores, r.now())
	return out, nil
}

// LatestPerOwner keeps, for each owner + env + workload, the rows of
// the most recently seen digest: the inventory remembers a week of old
// deploys, the API shows what runs now.
func LatestPerOwner(rows []db.VulnImage) []db.VulnImage {
	type slot struct{ kind, project, service, env, addon, ns, workload string }
	newest := map[slot]time.Time{}
	for _, im := range rows {
		k := slot{im.OwnerKind, im.Project, im.Service, im.Env, im.Addon, im.Namespace, im.Workload}
		if im.LastSeenAt.After(newest[k]) {
			newest[k] = im.LastSeenAt
		}
	}
	var out []db.VulnImage
	for _, im := range rows {
		k := slot{im.OwnerKind, im.Project, im.Service, im.Env, im.Addon, im.Namespace, im.Workload}
		// Rows from one sweep share a timestamp; anything older than the
		// slot's newest sighting by more than a sweep is a replaced image.
		if newest[k].Sub(im.LastSeenAt) < time.Minute {
			out = append(out, im)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.OwnerKind != b.OwnerKind {
			return a.OwnerKind > b.OwnerKind // service, then platform, then addon
		}
		if a.Service+a.Addon != b.Service+b.Addon {
			return a.Service+a.Addon < b.Service+b.Addon
		}
		return a.Env < b.Env
	})
	return out
}
