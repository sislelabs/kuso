package nodemetrics

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The node's ephemeral-storage capacity/allocatable pair is STATIC: it
// is the kubelet's reservation, identical on every node, and moves not
// at all as the disk fills. Reading it as "used vs free" rendered a
// constant 15GiB/300GiB (5%) on three nodes that were actually at 83%,
// 81% and 62% -- and made the disk-pressure alert unfireable, since it
// derives its percentage from the same pair.
//
// The kubelet Summary API reports the real filesystem, so that is what
// we read.
func TestDiskStatsFromSummary_ParsesRealUsage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"node": map[string]any{
				"nodeName": "kuso-worker",
				"fs": map[string]any{
					"capacityBytes":  int64(322_200_000_000),
					"usedBytes":      int64(256_100_000_000),
					"availableBytes": int64(53_000_000_000),
				},
			},
		})
	}))
	defer srv.Close()

	got, err := parseNodeFS([]byte(fetch(t, srv.URL)))
	if err != nil {
		t.Fatalf("parseNodeFS: %v", err)
	}
	if got.capacityBytes != 322_200_000_000 {
		t.Errorf("capacity = %d", got.capacityBytes)
	}
	if got.availableBytes != 53_000_000_000 {
		t.Errorf("available = %d", got.availableBytes)
	}
	// The whole point: a node at ~80% must not read as 5%.
	usedPct := 100 * float64(got.capacityBytes-got.availableBytes) / float64(got.capacityBytes)
	if usedPct < 75 || usedPct > 90 {
		t.Errorf("used%% = %.1f, want ~83 (the static-capacity bug reported ~5)", usedPct)
	}
}

// A kubelet that doesn't answer must not poison the row with zeros that
// would render as "0% full" or fire a bogus 100%-full alert.
func TestDiskStatsFromSummary_MalformedIsAnError(t *testing.T) {
	if _, err := parseNodeFS([]byte(`{"node":{"nodeName":"n"}}`)); err == nil {
		t.Fatal("missing fs block must be an error, not a zero-valued success")
	}
	if _, err := parseNodeFS([]byte(`not json`)); err == nil {
		t.Fatal("garbage must be an error")
	}
}

func fetch(t *testing.T, url string) string {
	t.Helper()
	resp, err := http.Get(url) //nolint:noctx
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	var sb []byte
	buf := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(buf)
		sb = append(sb, buf[:n]...)
		if err != nil {
			break
		}
	}
	_ = context.Background()
	return string(sb)
}
