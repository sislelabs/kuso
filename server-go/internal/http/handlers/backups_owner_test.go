package handlers

import (
	"bytes"
	"context"
	"io"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"kuso/server/internal/kube"
)

// memS3 is a single-bucket in-memory S3 with prefix + delimiter listing.
type memS3 struct {
	objs    map[string][]byte
	mod     map[string]time.Time
	deleted []string
}

func newMemS3() *memS3 { return &memS3{objs: map[string][]byte{}, mod: map[string]time.Time{}} }

func (m *memS3) put(key string, at time.Time) { m.objs[key] = []byte("x"); m.mod[key] = at }

func (m *memS3) ListObjectsV2(_ context.Context, in *s3.ListObjectsV2Input, _ ...func(*s3.Options)) (*s3.ListObjectsV2Output, error) {
	prefix, delim := aws.ToString(in.Prefix), aws.ToString(in.Delimiter)
	var keys []string
	for k := range m.objs {
		if strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	out := &s3.ListObjectsV2Output{}
	seen := map[string]bool{}
	for _, k := range keys {
		if delim != "" {
			if i := strings.Index(k[len(prefix):], delim); i >= 0 {
				cp := k[:len(prefix)+i+1]
				if !seen[cp] {
					seen[cp] = true
					out.CommonPrefixes = append(out.CommonPrefixes, s3types.CommonPrefix{Prefix: aws.String(cp)})
				}
				continue
			}
		}
		if in.MaxKeys != nil && int32(len(out.Contents)) >= *in.MaxKeys {
			break
		}
		out.Contents = append(out.Contents, s3types.Object{Key: aws.String(k), Size: aws.Int64(int64(len(m.objs[k]))), LastModified: aws.Time(m.mod[k])})
	}
	return out, nil
}

func (m *memS3) GetObject(_ context.Context, in *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	b, ok := m.objs[aws.ToString(in.Key)]
	if !ok {
		return nil, &s3types.NoSuchKey{}
	}
	return &s3.GetObjectOutput{Body: io.NopCloser(bytes.NewReader(b))}, nil
}

func (m *memS3) HeadObject(_ context.Context, in *s3.HeadObjectInput, _ ...func(*s3.Options)) (*s3.HeadObjectOutput, error) {
	if _, ok := m.objs[aws.ToString(in.Key)]; !ok {
		return nil, &s3types.NotFound{}
	}
	return &s3.HeadObjectOutput{LastModified: aws.Time(m.mod[aws.ToString(in.Key)])}, nil
}

func (m *memS3) PutObject(_ context.Context, in *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	b, _ := io.ReadAll(in.Body)
	m.objs[aws.ToString(in.Key)] = b
	m.mod[aws.ToString(in.Key)] = time.Now()
	return &s3.PutObjectOutput{}, nil
}

func (m *memS3) DeleteObjects(_ context.Context, in *s3.DeleteObjectsInput, _ ...func(*s3.Options)) (*s3.DeleteObjectsOutput, error) {
	for _, o := range in.Delete.Objects {
		delete(m.objs, aws.ToString(o.Key))
		m.deleted = append(m.deleted, aws.ToString(o.Key))
	}
	return &s3.DeleteObjectsOutput{}, nil
}

func addonCR(project, name, uid string, created time.Time) kube.KusoAddon {
	return kube.KusoAddon{
		ObjectMeta: metav1.ObjectMeta{Name: name, UID: types.UID(uid), CreationTimestamp: metav1.NewTime(created)},
		Spec:       kube.KusoAddonSpec{Project: project, Kind: "postgres"},
	}
}

var (
	day1 = time.Date(2026, 9, 1, 4, 0, 0, 0, time.UTC)
	day5 = time.Date(2026, 9, 5, 4, 0, 0, 0, time.UTC)
	day9 = time.Date(2026, 9, 9, 4, 0, 0, 0, time.UTC)
)

func stampKey(prefix string, t time.Time) string {
	return prefix + t.Format("20060102T150405Z") + ".sql.gz"
}

// BLOB-3/4: a re-created same-name addon must not see the deleted one's
// dumps, while its own dumps stay visible.
func TestBackupOwnership_PredecessorDumpsHidden(t *testing.T) {
	prefix := "acme/acme-db/"
	cli := newMemS3()
	old := stampKey(prefix, day1)
	mine := stampKey(prefix, day9)
	cli.put(old, day1)
	cli.put(old+".manifest.json", day1)
	cli.put(mine, day9)

	recreated := addonCR("acme", "acme-db", "uid-new", day5)
	own := ownershipFor(recreated.CreationTimestamp.Time, string(recreated.UID), nil)
	items, total, err := listOwnedBackups(context.Background(), cli, "b", prefix, 10, func(o BackupObject) bool {
		return own.owns(backupObjectTime(prefix, o.Key, o.When))
	})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || items[0].Key != mine {
		t.Fatalf("visible = %+v, want only %s", items, mine)
	}
}

// Legitimate owners keep their history: the CR predates its dumps, or
// the upgrade-time marker claims them for its UID.
func TestBackupOwnership_LegitimateOwnerSeesHistory(t *testing.T) {
	if !ownershipFor(day1.Add(-time.Hour), "u", nil).owns(day1) {
		t.Error("dump taken after the CR was created must be owned")
	}
	if ownershipFor(day5, "u", nil).owns(day1) {
		t.Error("dump older than the CR without a marker must not be owned")
	}
	if !ownershipFor(day5, "u", &backupOwnerRecord{AddonUID: "u"}).owns(day1) {
		t.Error("marker naming the CR's UID must claim older dumps")
	}
	if ownershipFor(day5, "u2", &backupOwnerRecord{AddonUID: "u"}).owns(day1) {
		t.Error("a marker for a different UID must not claim")
	}
	if got := backupObjectTime("p/a/", "p/a/20260901T040000Z/file.txt", time.Time{}); !got.Equal(day1) {
		t.Errorf("s3 snapshot prefix stamp = %v", got)
	}
	if got := backupObjectTime("p/a/", "p/a/legacy.sql.gz", day9); !got.Equal(day9) {
		t.Errorf("unstamped key should fall back to LastModified, got %v", got)
	}
}

func TestClaimLegacyBackupOwners(t *testing.T) {
	ctx := context.Background()
	cli := newMemS3()
	cli.put(stampKey("acme/acme-db/", day1), day1)
	cli.put(stampKey("acme/acme-cache/", day1), day1)
	cli.objs["acme/acme-cache/"+backupOwnerMarker] = []byte(`{"addonUID":"someone-else"}`)
	// acme-db's CR was recreated (new UID, newer than its dumps) but it is
	// the owner at upgrade time.
	crs := []kube.KusoAddon{
		addonCR("acme", "acme-db", "uid-db", day5),
		addonCR("acme", "acme-cache", "uid-cache", day5),
		addonCR("acme", "acme-empty", "uid-empty", day5),
	}
	if err := claimLegacyBackupOwners(ctx, cli, "b", crs, day9); err != nil {
		t.Fatal(err)
	}
	rec, _ := readBackupOwner(ctx, cli, "b", "acme/acme-db/")
	if rec == nil || rec.AddonUID != "uid-db" {
		t.Fatalf("acme-db marker = %+v", rec)
	}
	rec, _ = readBackupOwner(ctx, cli, "b", "acme/acme-cache/")
	if rec == nil || rec.AddonUID != "someone-else" {
		t.Errorf("an existing marker was overwritten: %+v", rec)
	}
	if _, ok := cli.objs["acme/acme-empty/"+backupOwnerMarker]; ok {
		t.Error("marker written for an addon with no backups")
	}
	if _, ok := cli.objs[backupOwnerClaimsDone]; !ok {
		t.Fatal("done marker missing")
	}
	// After the first pass a re-created addon never claims anything.
	cli.put(stampKey("acme/acme-new/", day1), day1)
	if err := claimLegacyBackupOwners(ctx, cli, "b", []kube.KusoAddon{addonCR("acme", "acme-new", "uid-x", day9)}, day9); err != nil {
		t.Fatal(err)
	}
	if _, ok := cli.objs["acme/acme-new/"+backupOwnerMarker]; ok {
		t.Error("claim ran again after the done marker")
	}
}

func TestOrphanScannerAndPurge(t *testing.T) {
	ctx := context.Background()
	cli := newMemS3()
	gone := stampKey("old/old-db/", day1)
	pred := stampKey("acme/acme-db/", day1)
	mine := stampKey("acme/acme-db/", day9)
	for _, k := range []string{gone, gone + ".manifest.json", pred, pred + ".manifest.json", mine} {
		cli.put(k, day1)
	}
	cli.put("kuso-instance/kuso-instance-pg/"+day1.Format("20060102T150405Z")+".sql.gz", day1)

	live := addonCR("acme", "acme-db", "uid", day5)
	sc := orphanScanner{cli: cli, bucket: "b", owner: func(_ context.Context, project, addon string) (backupOwnership, bool, bool, error) {
		if project == "acme" && addon == "acme-db" {
			return ownershipFor(live.CreationTimestamp.Time, string(live.UID), nil), true, false, nil
		}
		return backupOwnership{}, false, false, nil
	}}
	got, err := sc.scan(ctx)
	if err != nil {
		t.Fatal(err)
	}
	byPrefix := map[string]BackupOrphan{}
	for _, o := range got {
		byPrefix[o.Project+"/"+o.Addon] = o
	}
	if len(got) != 2 || byPrefix["old/old-db"].Reason != "addon-deleted" || byPrefix["old/old-db"].Objects != 2 ||
		byPrefix["acme/acme-db"].Reason != "predecessor" || byPrefix["acme/acme-db"].Objects != 2 {
		t.Fatalf("orphans = %+v", got)
	}

	keys, _, err := sc.orphanKeys(ctx, "acme", "acme-db")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sc.purge(ctx, keys); err != nil {
		t.Fatal(err)
	}
	if _, ok := cli.objs[mine]; !ok {
		t.Fatal("purge deleted the live addon's own dump")
	}
	if _, ok := cli.objs[pred]; ok {
		t.Error("predecessor dump survived purge")
	}
	if _, ok := cli.objs[gone]; !ok {
		t.Error("purging acme/acme-db touched another prefix")
	}
}

// DB-5: a dropped tenant or clone database's dumps stop while the instance
// keeps being backed up. Its series must show up in the orphan scan, even
// under the reserved kuso- prefix the addon-level scan skips.
func TestOrphanScanner_ReportsDroppedDatabaseSeries(t *testing.T) {
	ctx := context.Background()
	cli := newMemS3()
	prefix := "kuso-instance/kuso-instance-pg/"
	ts := func(d time.Time) string { return d.Format("20060102T150405Z") }
	for _, d := range []time.Time{day1, day5, day9} {
		cli.put(prefix+ts(d)+".sql.gz", d)
		cli.put(prefix+ts(d)+".db-live_db.sql.gz", d)
	}
	cli.put(prefix+ts(day1)+".db-kuso-preview-pr-7.sql.gz", day1)
	cli.put(prefix+ts(day1)+".db-kuso-preview-pr-7.sql.gz.manifest.json", day1)

	sc := orphanScanner{cli: cli, bucket: "b", owner: func(context.Context, string, string) (backupOwnership, bool, bool, error) {
		return backupOwnership{}, false, false, nil
	}}
	got, err := sc.scan(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Reason != "database-not-dumped:kuso-preview-pr-7" || got[0].Objects != 2 ||
		got[0].Project != "kuso-instance" || !got[0].Newest.Equal(day1) {
		t.Fatalf("orphans = %+v, want only the dropped preview DB's series", got)
	}
}
