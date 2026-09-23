package handlers

import (
	"context"
	"fmt"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// pagedLister mimics S3: keys in lexical order, at most pageSize per page,
// continuation via an opaque token.
type pagedLister struct {
	keys     []string
	pageSize int
}

func (p *pagedLister) ListObjectsV2(_ context.Context, in *s3.ListObjectsV2Input, _ ...func(*s3.Options)) (*s3.ListObjectsV2Output, error) {
	start := 0
	if in.ContinuationToken != nil {
		fmt.Sscanf(*in.ContinuationToken, "%d", &start)
	}
	end := min(start+p.pageSize, len(p.keys))
	out := &s3.ListObjectsV2Output{}
	for _, k := range p.keys[start:end] {
		out.Contents = append(out.Contents, s3types.Object{Key: aws.String(k), Size: aws.Int64(1)})
	}
	if end < len(p.keys) {
		out.IsTruncated = aws.Bool(true)
		out.NextContinuationToken = aws.String(fmt.Sprint(end))
	}
	return out, nil
}

func TestListNewestBackups_ReturnsNewestAcrossPages(t *testing.T) {
	var keys []string
	for day := 1; day <= 25; day++ {
		ts := fmt.Sprintf("202609%02dT040000Z", day)
		keys = append(keys, "p/p-pg/"+ts+".sql.gz", "p/p-pg/"+ts+".sql.gz.manifest.json")
	}
	lister := &pagedLister{keys: keys, pageSize: 10}

	items, total, err := listNewestBackups(context.Background(), lister, "b", "p/p-pg/", 3)
	if err != nil {
		t.Fatal(err)
	}
	if total != 25 {
		t.Errorf("total=%d want 25 (manifests excluded)", total)
	}
	want := []string{
		"p/p-pg/20260925T040000Z.sql.gz",
		"p/p-pg/20260924T040000Z.sql.gz",
		"p/p-pg/20260923T040000Z.sql.gz",
	}
	if len(items) != len(want) {
		t.Fatalf("got %d items want %d: %+v", len(items), len(want), items)
	}
	for i, w := range want {
		if items[i].Key != w {
			t.Errorf("items[%d]=%s want %s", i, items[i].Key, w)
		}
	}
}
