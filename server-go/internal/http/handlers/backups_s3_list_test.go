package handlers

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// BLOB-3/8: an s3 addon's list is one row per complete snapshot, not one
// per object, so older snapshots stay reachable however many files the
// newest holds; a snapshot without its manifest (a failed run) is hidden.
func TestListS3Snapshots_OneRowPerCompleteSnapshot(t *testing.T) {
	prefix := "acme/acme-s3/"
	cli := newMemS3()
	snaps := []string{"20260901T030000Z/", "20260902T030000Z/", "20260902T030000Z.bucket-avatars/", "20260903T030000Z/"}
	for _, s := range snaps {
		for i := 0; i < 300; i++ {
			cli.put(fmt.Sprintf("%s%sfile-%03d", prefix, s, i), day1)
		}
		cli.put(prefix+s+s3SnapshotManifest, day1)
	}
	// A failed run: files, no manifest.
	cli.put(prefix+"20260904T030000Z/file-000", day1)
	cli.put(prefix+backupOwnerMarker, day1)

	items, total, err := listS3Snapshots(context.Background(), cli, "b", prefix, 200, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		prefix + "20260903T030000Z/",
		prefix + "20260902T030000Z/",
		prefix + "20260902T030000Z.bucket-avatars/",
		prefix + "20260901T030000Z/",
	}
	if total != len(want) || len(items) != len(want) {
		t.Fatalf("total=%d items=%+v, want %v", total, items, want)
	}
	for i, w := range want {
		if items[i].Key != w {
			t.Errorf("items[%d]=%s want %s", i, items[i].Key, w)
		}
	}
	if !items[3].When.Equal(day1.Add(-time.Hour)) {
		t.Errorf("snapshot time not parsed: %v", items[3].When)
	}
}
