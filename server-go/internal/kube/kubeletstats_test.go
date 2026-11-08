package kube

import (
	"context"
	"encoding/pem"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
)

func fakeKubelet(t *testing.T, h http.HandlerFunc) (*rest.Config, *corev1.Node) {
	t.Helper()
	srv := httptest.NewTLSServer(h)
	t.Cleanup(srv.Close)
	host, port, _ := net.SplitHostPort(srv.Listener.Addr().String())
	p, _ := strconv.Atoi(port)
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "kuso-worker"}}
	node.Status.Addresses = []corev1.NodeAddress{{Type: corev1.NodeInternalIP, Address: host}}
	node.Status.DaemonEndpoints.KubeletEndpoint.Port = int32(p)
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	return &rest.Config{BearerToken: "sa-token", TLSClientConfig: rest.TLSClientConfig{CAData: ca}}, node
}

// The node object's ephemeral-storage pair is a static reservation and
// rendered "15GiB / 300GiB (5%)" on a node that was really ~83% full.
func TestNodeFilesystems_ReadsKubeletSummaryWithSAToken(t *testing.T) {
	cfg, node := fakeKubelet(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/stats/summary" || r.Header.Get("Authorization") != "Bearer sa-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"node":{"nodeName":"kuso-worker","fs":{"capacityBytes":322200000000,"availableBytes":53000000000,"usedBytes":256100000000}}}`))
	})
	got, errs := (&Client{Config: cfg}).NodeFilesystems(context.Background(), []*corev1.Node{node})
	if len(errs) != 0 {
		t.Fatalf("errs: %v", errs)
	}
	fs := got["kuso-worker"]
	if fs.CapacityBytes != 322_200_000_000 || fs.AvailableBytes != 53_000_000_000 {
		t.Fatalf("got %+v", fs)
	}
}

// RBAC without nodes/stats is a 403 on every node, every poll. It must
// park the calls instead of dialling each kubelet on every request.
func TestNodeFilesystems_ForbiddenParksCalls(t *testing.T) {
	var calls atomic.Int32
	cfg, node := fakeKubelet(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`Forbidden (user=system:serviceaccount:kuso:kuso-server, verb=get, resource=nodes, subresource(s)=[stats])`))
	})
	c := &Client{Config: cfg}
	for i := 0; i < 3; i++ {
		got, errs := c.NodeFilesystems(context.Background(), []*corev1.Node{node})
		if len(got) != 0 || len(errs) != 1 || !errors.Is(errs[0], ErrKubeletForbidden) {
			t.Fatalf("call %d: got %v errs %v", i, got, errs)
		}
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("kubelet dialled %d times across 3 calls, want 1", n)
	}
}

func TestParseSummaryFS_MissingFSIsAnError(t *testing.T) {
	for _, body := range []string{`{"node":{"nodeName":"n"}}`, `not json`, `{"node":{"fs":{"capacityBytes":10}}}`} {
		if _, err := ParseSummaryFS([]byte(body)); err == nil {
			t.Errorf("%s: want error, got nil", body)
		}
	}
}

func TestParseSummaryUsage_NodeTotalsAndPods(t *testing.T) {
	body := `{"node":{"cpu":{"usageNanoCores":958262427},"memory":{"usageBytes":9180737536,"workingSetBytes":3885408256},
	"fs":{"capacityBytes":322239832064,"usedBytes":240540450816},
	"runtime":{"imageFs":{"capacityBytes":322239832064,"usedBytes":172454354944}}},
	"pods":[{"podRef":{"name":"web-1","namespace":"kuso"},"cpu":{"usageNanoCores":714776},
	"memory":{"workingSetBytes":13348864},"ephemeral-storage":{"usedBytes":24576},"volume":null},
	{"podRef":{"name":"starting","namespace":"kuso"}}]}`
	got, err := ParseSummaryUsage([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if got.CPUNanoCores != 958262427 || got.MemWorkingSetBytes != 3885408256 || got.FSUsedBytes != 240540450816 ||
		got.ImageFSUsedBytes != 172454354944 || !got.ImageFSShared {
		t.Errorf("node totals = %+v", got)
	}
	if len(got.Pods) != 2 || got.Pods[0] != (PodUsage{Namespace: "kuso", Name: "web-1", CPUNanoCores: 714776, MemWorkingSetBytes: 13348864, EphemeralBytes: 24576}) {
		t.Errorf("pods = %+v", got.Pods)
	}
	if got.Pods[1].Name != "starting" || got.Pods[1].CPUNanoCores != 0 {
		t.Errorf("a pod with no stats yet should still parse: %+v", got.Pods[1])
	}
}

func TestParseSummaryUsage_HostProcesses(t *testing.T) {
	body := `{"node":{"fs":{"capacityBytes":10,"usedBytes":5},"systemContainers":[
	{"name":"kubelet","cpu":{"usageNanoCores":636000000},"memory":{"workingSetBytes":1089}},
	{"name":"pods","cpu":{"usageNanoCores":381000000},"memory":{"workingSetBytes":3141}}]}}`
	got, err := ParseSummaryUsage([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Host) != 1 || got.Host[0] != (PodUsage{Name: "kubelet", CPUNanoCores: 636000000, MemWorkingSetBytes: 1089}) {
		t.Errorf("host = %+v (the pods cgroup must not double-count)", got.Host)
	}
}

func TestNodesPodUsage_KeysPodsByNode(t *testing.T) {
	cfg, node := fakeKubelet(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"node":{"fs":{"capacityBytes":10,"usedBytes":5}},
		"pods":[{"podRef":{"name":"web-1","namespace":"kuso"},"cpu":{"usageNanoCores":250000000},"memory":{"workingSetBytes":4096}}]}`))
	})
	got, errs := (&Client{Config: cfg}).NodesPodUsage(context.Background(), []*corev1.Node{node})
	if len(errs) != 0 {
		t.Fatalf("errs: %v", errs)
	}
	want := PodUsage{Namespace: "kuso", Name: "web-1", CPUNanoCores: 250_000_000, MemWorkingSetBytes: 4096}
	if pods := got["kuso-worker"]; len(pods) != 1 || pods[0] != want {
		t.Fatalf("got %+v", got)
	}
}
