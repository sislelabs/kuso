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
