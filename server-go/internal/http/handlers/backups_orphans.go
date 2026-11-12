package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"

	"kuso/server/internal/addons"
	"kuso/server/internal/audit"
)

// Backups of a deleted project or addon, and a live addon's same-name
// predecessor's dumps, stay in the bucket (nothing deletes them on purge,
// on purpose: the bucket is the only off-cluster copy). These two admin
// endpoints make them visible and removable explicitly, with an audit
// entry, instead of retained forever unseen.
//
//	GET  /api/admin/backups/orphans
//	POST /api/admin/backups/orphans/purge {project, addon, confirm}

// BackupOrphan is one prefix holding objects no live addon owns.
type BackupOrphan struct {
	Project string `json:"project"`
	Addon   string `json:"addon"`
	// Reason: "addon-deleted" (no live addon of that name in that
	// project) or "predecessor" (objects older than the live addon that
	// its owner marker doesn't claim).
	Reason  string    `json:"reason"`
	Objects int       `json:"objects"`
	Bytes   int64     `json:"bytes"`
	Newest  time.Time `json:"newest"`
}

type orphanS3 interface {
	s3.ListObjectsV2APIClient
	s3ObjectReader
	DeleteObjects(ctx context.Context, in *s3.DeleteObjectsInput, opts ...func(*s3.Options)) (*s3.DeleteObjectsOutput, error)
}

// orphanScanner resolves whether a project/addon prefix has a live owner.
// Split out so the decision is testable without a cluster.
type orphanScanner struct {
	cli    orphanS3
	bucket string
	// owner returns the ownership rule of the live addon, ok=false when
	// no live addon owns the prefix, skip=true when the prefix must not
	// be touched (the addon writes to its own bucket).
	owner func(ctx context.Context, project, addon string) (own backupOwnership, ok, skip bool, err error)
}

func (s orphanScanner) childPrefixes(ctx context.Context, prefix string) ([]string, error) {
	var out []string
	p := s3.NewListObjectsV2Paginator(s.cli, &s3.ListObjectsV2Input{Bucket: aws.String(s.bucket), Prefix: aws.String(prefix), Delimiter: aws.String("/")})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, cp := range page.CommonPrefixes {
			name := strings.TrimSuffix(strings.TrimPrefix(aws.ToString(cp.Prefix), prefix), "/")
			if name != "" {
				out = append(out, name)
			}
		}
	}
	return out, nil
}

// orphanKeys lists the keys under project/addon/ no live addon owns.
func (s orphanScanner) orphanKeys(ctx context.Context, project, addon string) (keys []string, orphan BackupOrphan, err error) {
	orphan = BackupOrphan{Project: project, Addon: addon}
	own, live, skip, err := s.owner(ctx, project, addon)
	if err != nil || skip {
		return nil, orphan, err
	}
	orphan.Reason = "predecessor"
	if !live {
		orphan.Reason = "addon-deleted"
	}
	prefix := project + "/" + addon + "/"
	p := s3.NewListObjectsV2Paginator(s.cli, &s3.ListObjectsV2Input{Bucket: aws.String(s.bucket), Prefix: aws.String(prefix)})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return nil, orphan, err
		}
		for _, o := range page.Contents {
			key := aws.ToString(o.Key)
			taken := backupObjectTime(prefix, key, aws.ToTime(o.LastModified))
			if live && (isOwnerMarkerKey(key) || own.owns(taken)) {
				continue
			}
			keys = append(keys, key)
			orphan.Objects++
			orphan.Bytes += aws.ToInt64(o.Size)
			if taken.After(orphan.Newest) {
				orphan.Newest = taken
			}
		}
	}
	return keys, orphan, nil
}

// reservedBackupProject: instance-addon, control-plane and kuso-meta
// prefixes are never offered for purge (the kuso- project prefix is
// reserved, so no tenant project can live there).
func reservedBackupProject(p string) bool { return strings.HasPrefix(p, "kuso-") }

// droppedDBSeriesLag: a per-database series whose newest dump trails the
// prefix's newest dump by more than this is no longer being dumped. The
// backup runs nightly, so two missed runs.
const droppedDBSeriesLag = 48 * time.Hour

var dbSeriesRe = regexp.MustCompile(`^\d{8}T\d{6}Z\.db-(.+?)\.sql\.gz(\.manifest\.json)?$`)

// droppedDBSeries reports the per-database series (<ts>.db-<db>.sql.gz)
// under one addon prefix that stopped while the addon kept being dumped:
// the database was dropped (a preview or env clone, seeded from
// production) or its dump keeps failing. Instance prefixes are included,
// since that is where tenant series live. The backup Job's prune ages a
// dropped series out after retention; this makes it visible until then.
// Report-only: purge acts on whole orphaned addon prefixes.
func (s orphanScanner) droppedDBSeries(ctx context.Context, project, addon string) ([]BackupOrphan, error) {
	prefix := project + "/" + addon + "/"
	series := map[string]*BackupOrphan{}
	var newest time.Time
	p := s3.NewListObjectsV2Paginator(s.cli, &s3.ListObjectsV2Input{Bucket: aws.String(s.bucket), Prefix: aws.String(prefix)})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, o := range page.Contents {
			key := aws.ToString(o.Key)
			taken := backupObjectTime(prefix, key, aws.ToTime(o.LastModified))
			if taken.After(newest) {
				newest = taken
			}
			m := dbSeriesRe.FindStringSubmatch(strings.TrimPrefix(key, prefix))
			if m == nil {
				continue
			}
			b := series[m[1]]
			if b == nil {
				b = &BackupOrphan{Project: project, Addon: addon, Reason: "database-not-dumped:" + m[1]}
				series[m[1]] = b
			}
			b.Objects++
			b.Bytes += aws.ToInt64(o.Size)
			if taken.After(b.Newest) {
				b.Newest = taken
			}
		}
	}
	var out []BackupOrphan
	for _, b := range series {
		if newest.Sub(b.Newest) > droppedDBSeriesLag {
			out = append(out, *b)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Reason < out[j].Reason })
	return out, nil
}

func (s orphanScanner) scan(ctx context.Context) ([]BackupOrphan, error) {
	projects, err := s.childPrefixes(ctx, "")
	if err != nil {
		return nil, err
	}
	out := []BackupOrphan{}
	for _, p := range projects {
		addonNames, err := s.childPrefixes(ctx, p+"/")
		if err != nil {
			return nil, err
		}
		for _, a := range addonNames {
			stale, err := s.droppedDBSeries(ctx, p, a)
			if err != nil {
				return nil, fmt.Errorf("%s/%s: %w", p, a, err)
			}
			out = append(out, stale...)
			if reservedBackupProject(p) {
				continue
			}
			_, o, err := s.orphanKeys(ctx, p, a)
			if err != nil {
				return nil, fmt.Errorf("%s/%s: %w", p, a, err)
			}
			if o.Objects > 0 {
				out = append(out, o)
			}
		}
	}
	return out, nil
}

func (s orphanScanner) purge(ctx context.Context, keys []string) (int, error) {
	deleted := 0
	for start := 0; start < len(keys); start += 1000 {
		end := min(start+1000, len(keys))
		ids := make([]s3types.ObjectIdentifier, 0, end-start)
		for _, k := range keys[start:end] {
			ids = append(ids, s3types.ObjectIdentifier{Key: aws.String(k)})
		}
		out, err := s.cli.DeleteObjects(ctx, &s3.DeleteObjectsInput{
			Bucket: aws.String(s.bucket),
			Delete: &s3types.Delete{Objects: ids, Quiet: aws.Bool(true)},
		})
		if err != nil {
			return deleted, err
		}
		if len(out.Errors) > 0 {
			return deleted + (end - start - len(out.Errors)), fmt.Errorf("%d objects failed to delete, first: %s", len(out.Errors), aws.ToString(out.Errors[0].Message))
		}
		deleted += end - start
	}
	return deleted, nil
}

func (h *BackupsHandler) orphanScanner(ctx context.Context) (orphanScanner, error) {
	cli, bucket, err := h.s3Client(ctx)
	if err != nil {
		return orphanScanner{}, err
	}
	return orphanScanner{cli: cli, bucket: bucket, owner: func(ctx context.Context, project, addon string) (backupOwnership, bool, bool, error) {
		cr, _, err := h.ownedAddon(ctx, project, addon)
		if errors.Is(err, addons.ErrNotFound) {
			return backupOwnership{}, false, false, nil
		}
		if err != nil {
			return backupOwnership{}, false, false, err
		}
		if cr.Name != addon || cr.BackupBucket("") != "" {
			// A short-name alias of a live CR, or an addon that backs up
			// elsewhere: leave its objects alone.
			return backupOwnership{}, true, true, nil
		}
		return h.addonBackupOwnership(ctx, cli, bucket, project+"/"+addon+"/", cr), true, false, nil
	}}, nil
}

// RetainedBackups lists the addon prefixes holding backups under a
// project name. Called for a project that doesn't exist (create), so
// every hit is a deleted predecessor's.
func (h *BackupsHandler) RetainedBackups(ctx context.Context, project string) ([]string, error) {
	if reservedBackupProject(project) {
		return nil, nil
	}
	cli, bucket, err := h.s3Client(ctx)
	if errors.Is(err, errBackupNotConfigured) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	names, err := orphanScanner{cli: cli, bucket: bucket}.childPrefixes(ctx, project+"/")
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(names))
	for _, n := range names {
		out = append(out, "backups s3://"+bucket+"/"+project+"/"+n+"/")
	}
	return out, nil
}

func (h *BackupsHandler) ListOrphanBackups(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	sc, err := h.orphanScanner(ctx)
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	out, err := sc.scan(ctx)
	if err != nil {
		h.Logger.Error("backup: scan orphans", "err", err)
		writeErr(w, http.StatusBadGateway, "scan failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// PurgeOrphanRequest names one prefix to purge. Confirm must echo
// "<project>/<addon>".
type PurgeOrphanRequest struct {
	Project string `json:"project"`
	Addon   string `json:"addon"`
	Confirm string `json:"confirm"`
}

func (h *BackupsHandler) PurgeOrphanBackups(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	var req PurgeOrphanRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Project == "" || req.Addon == "" {
		writeErr(w, http.StatusBadRequest, "project and addon required")
		return
	}
	if strings.ContainsAny(req.Project+req.Addon, "/\x00") || strings.Contains(req.Project+req.Addon, "..") || reservedBackupProject(req.Project) {
		writeErr(w, http.StatusBadRequest, "invalid project or addon")
		return
	}
	if req.Confirm != req.Project+"/"+req.Addon {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("purge permanently deletes backups — set \"confirm\":%q to acknowledge", req.Project+"/"+req.Addon))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	sc, err := h.orphanScanner(ctx)
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	keys, o, err := sc.orphanKeys(ctx, req.Project, req.Addon)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "scan failed: "+err.Error())
		return
	}
	deleted, perr := sc.purge(ctx, keys)
	if h.Audit != nil {
		msg := fmt.Sprintf("purged %d of %d orphaned backup objects (%s, %d bytes) under %s/%s/", deleted, len(keys), o.Reason, o.Bytes, req.Project, req.Addon)
		if perr != nil {
			msg += ": " + perr.Error()
		}
		h.Audit.Log(ctx, audit.Entry{
			User: auditUser(ctx), Severity: "critical", Action: "backup.purge",
			Pipeline: req.Project, App: req.Addon, Resource: "backup", Message: msg,
		})
	}
	if perr != nil {
		writeErr(w, http.StatusBadGateway, fmt.Sprintf("deleted %d of %d objects: %v", deleted, len(keys), perr))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": deleted, "reason": o.Reason, "bytes": o.Bytes})
}
