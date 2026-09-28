package handlers

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/go-chi/chi/v5"
	corev1 "k8s.io/api/core/v1"
	kubefake "k8s.io/client-go/kubernetes/fake"

	"kuso/server/internal/kube"
)

func lbService(ingress ...corev1.LoadBalancerIngress) corev1.Service {
	var s corev1.Service
	s.Status.LoadBalancer.Ingress = ingress
	return s
}

func testNode(ready bool, addrs ...corev1.NodeAddress) corev1.Node {
	st := corev1.ConditionFalse
	if ready {
		st = corev1.ConditionTrue
	}
	var n corev1.Node
	n.Status.Conditions = []corev1.NodeCondition{{Type: corev1.NodeReady, Status: st}}
	n.Status.Addresses = addrs
	return n
}

func ext(ip string) corev1.NodeAddress {
	return corev1.NodeAddress{Type: corev1.NodeExternalIP, Address: ip}
}
func internalIP(ip string) corev1.NodeAddress {
	return corev1.NodeAddress{Type: corev1.NodeInternalIP, Address: ip}
}

func TestDeriveIngressTargets(t *testing.T) {
	cases := []struct {
		name  string
		svcs  []corev1.Service
		nodes []corev1.Node
		want  IngressTargets
	}{
		{
			name:  "load balancer wins over nodes",
			svcs:  []corev1.Service{lbService(corev1.LoadBalancerIngress{IP: "203.0.113.7"}, corev1.LoadBalancerIngress{Hostname: "lb.example.com"})},
			nodes: []corev1.Node{testNode(true, ext("198.51.100.1"))},
			want:  IngressTargets{IPs: []string{"203.0.113.7"}, Hostnames: []string{"lb.example.com"}, Source: "loadbalancer"},
		},
		{
			name: "klipper-lb node IPs: private dropped when a public one exists",
			svcs: []corev1.Service{lbService(corev1.LoadBalancerIngress{IP: "10.0.0.5"}, corev1.LoadBalancerIngress{IP: "203.0.113.7"})},
			want: IngressTargets{IPs: []string{"203.0.113.7"}, Hostnames: []string{}, Source: "loadbalancer"},
		},
		{
			name: "no LB: Ready nodes' ExternalIPs only, NotReady skipped",
			svcs: []corev1.Service{lbService()},
			nodes: []corev1.Node{
				testNode(true, ext("198.51.100.2"), internalIP("10.0.0.2")),
				testNode(true, ext("198.51.100.1"), internalIP("10.0.0.1")),
				testNode(false, ext("198.51.100.9")),
			},
			want: IngressTargets{IPs: []string{"198.51.100.1", "198.51.100.2"}, Hostnames: []string{}, Source: "nodes"},
		},
		{
			name:  "no ExternalIP anywhere: InternalIPs, public preferred",
			nodes: []corev1.Node{testNode(true, internalIP("10.0.0.1")), testNode(true, internalIP("5.75.1.2"))},
			want:  IngressTargets{IPs: []string{"5.75.1.2"}, Hostnames: []string{}, Source: "nodes"},
		},
		{
			name:  "only private InternalIPs: keep them",
			nodes: []corev1.Node{testNode(true, internalIP("192.168.1.10"))},
			want:  IngressTargets{IPs: []string{"192.168.1.10"}, Hostnames: []string{}, Source: "nodes"},
		},
		{
			name:  "nothing: source none with empty lists",
			nodes: []corev1.Node{testNode(false, ext("198.51.100.9"))},
			want:  IngressTargets{IPs: []string{}, Hostnames: []string{}, Source: "none"},
		},
	}
	for _, tc := range cases {
		got := deriveIngressTargets(tc.svcs, tc.nodes)
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: got %+v, want %+v", tc.name, got, tc.want)
		}
	}
}

// TestIngressTargetsJSONShape pins the wire contract the CLI and web read.
func TestIngressTargetsJSONShape(t *testing.T) {
	b, _ := json.Marshal(deriveIngressTargets(nil, nil))
	if string(b) != `{"ips":[],"hostnames":[],"source":"none"}` {
		t.Errorf("shape = %s", b)
	}
}

func TestIngressTargetsRoute(t *testing.T) {
	svc := lbService(corev1.LoadBalancerIngress{IP: "203.0.113.7"})
	svc.Namespace, svc.Name = "traefik", "traefik"
	h := &KubernetesHandler{Kube: &kube.Client{Clientset: kubefake.NewSimpleClientset(&svc)}, Logger: slog.Default()}
	r := chi.NewRouter()
	h.Mount(r)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/config/ingress", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
	}
	var got IngressTargets
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Source != "loadbalancer" || !reflect.DeepEqual(got.IPs, []string{"203.0.113.7"}) {
		t.Errorf("got %+v", got)
	}
}
