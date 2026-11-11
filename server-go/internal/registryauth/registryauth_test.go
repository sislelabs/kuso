package registryauth

import (
	"context"
	"strings"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"golang.org/x/crypto/bcrypt"

	"kuso/server/internal/kube"
)

const host = "kuso-registry.kuso.svc.cluster.local:5000"

func newStore() (*Store, *fake.Clientset) {
	cs := fake.NewSimpleClientset()
	return &Store{Kube: &kube.Client{Clientset: cs}, Namespace: "kuso", Host: host}, cs
}

// install.sh, the join bootstrap and hack/registry-auth-node.sh compute the
// marker with `printf user:pass | sha256sum | cut -c1-16`; the probe only
// passes if Go agrees byte for byte.
func TestMarker_MatchesShell(t *testing.T) {
	if got := (Creds{Username: "kuso", Password: "abc123"}).Marker(); got != "09ac812a94bc63c3" {
		t.Fatalf("marker = %s", got)
	}
}

// Minting must not switch auth on: the registry flips only when an operator
// sets enabled=true after every node has the credential.
func TestEnsure_MintsDisabledAndIsIdempotent(t *testing.T) {
	ctx := context.Background()
	s, cs := newStore()
	c1, err := s.Ensure(ctx)
	if err != nil {
		t.Fatal(err)
	}
	sec, err := cs.CoreV1().Secrets("kuso").Get(ctx, SecretName, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if string(sec.Data["enabled"]) != "false" {
		t.Fatalf("enabled = %q, want false", sec.Data["enabled"])
	}
	if !strings.HasPrefix(string(sec.Data["htpasswd"]), "kuso:$2") {
		t.Fatalf("htpasswd not bcrypt: %q", sec.Data["htpasswd"])
	}
	if strings.Trim(c1.Password, "0123456789abcdef") != "" {
		t.Fatalf("password must be hex for the bootstrap's sed parser: %q", c1.Password)
	}
	c2, err := s.Ensure(ctx)
	if err != nil || c2.Password != c1.Password {
		t.Fatalf("second Ensure rotated the password (err=%v)", err)
	}
}

func TestEnsurePushSecret_AnonymousUntilMinted(t *testing.T) {
	ctx := context.Background()
	s, cs := newStore()
	name, err := s.EnsurePushSecret(ctx, "kuso-p")
	if err != nil || name != "" {
		t.Fatalf("before mint: name=%q err=%v", name, err)
	}
	if _, err := s.Ensure(ctx); err != nil {
		t.Fatal(err)
	}
	name, err = s.EnsurePushSecret(ctx, "kuso-p")
	if err != nil || name != PushSecretName {
		t.Fatalf("after mint: name=%q err=%v", name, err)
	}
	sec, err := cs.CoreV1().Secrets("kuso-p").Get(ctx, PushSecretName, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if sec.Type != corev1.SecretTypeDockerConfigJson || !strings.Contains(string(sec.Data[".dockerconfigjson"]), host) {
		t.Fatalf("push secret: type=%s data=%s", sec.Type, sec.Data[".dockerconfigjson"])
	}
}

// readyToEnable must stay false until EVERY node's probe succeeded with the
// CURRENT credential.
func TestStatus_ReadyOnlyWhenEveryNodeProbedOK(t *testing.T) {
	ctx := context.Background()
	s, cs := newStore()
	c, err := s.Ensure(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"a", "b"} {
		if _, err := cs.CoreV1().Nodes().Create(ctx, &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: n}}, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.ProbeNodes(ctx); err != nil {
		t.Fatal(err)
	}
	st, err := s.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if st.ReadyToEnable || st.Nodes[0].State != NodePending {
		t.Fatalf("fresh probes: %+v", st)
	}
	setJob := func(node string, mutate func(*batchv1.Job)) {
		j, err := cs.BatchV1().Jobs("kuso").Get(ctx, probeJobName(node), metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		mutate(j)
		if _, err := cs.BatchV1().Jobs("kuso").Update(ctx, j, metav1.UpdateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	setJob("a", func(j *batchv1.Job) { j.Status.Succeeded = 1 })
	setJob("b", func(j *batchv1.Job) { j.Status.Failed = 1 })
	if st, _ = s.Status(ctx); st.ReadyToEnable {
		t.Fatalf("node b missing the credential but ready: %+v", st)
	}
	setJob("b", func(j *batchv1.Job) { j.Status.Failed, j.Status.Succeeded = 0, 1 })
	if st, _ = s.Status(ctx); !st.ReadyToEnable {
		t.Fatalf("all nodes ok but not ready: %+v", st)
	}
	setJob("b", func(j *batchv1.Job) { j.Annotations[annProbeMarker] = "old" + c.Marker()[3:] })
	if st, _ = s.Status(ctx); st.ReadyToEnable {
		t.Fatalf("stale probe counted as ready: %+v", st)
	}
}

// install.sh creates the Secret with username/password/enabled only (no
// bcrypt in shell); Complete must add a matching htpasswd and keep the
// password and the enabled flag as written.
func TestComplete_FillsShellMintedSecret(t *testing.T) {
	ctx := context.Background()
	s, cs := newStore()
	if done, err := s.Complete(ctx); err != nil || !done {
		t.Fatalf("no secret: done=%v err=%v", done, err)
	}
	_, _ = cs.CoreV1().Secrets("kuso").Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: SecretName, Namespace: "kuso"},
		Data:       map[string][]byte{"username": []byte("kuso"), "password": []byte("abc123"), "enabled": []byte("true")},
	}, metav1.CreateOptions{})
	if done, err := s.Complete(ctx); err != nil || !done {
		t.Fatalf("complete: done=%v err=%v", done, err)
	}
	sec, _ := cs.CoreV1().Secrets("kuso").Get(ctx, SecretName, metav1.GetOptions{})
	user, hash, _ := strings.Cut(strings.TrimSpace(string(sec.Data["htpasswd"])), ":")
	if user != "kuso" || bcrypt.CompareHashAndPassword([]byte(hash), []byte("abc123")) != nil {
		t.Fatalf("htpasswd does not match the password: %q", sec.Data["htpasswd"])
	}
	if string(sec.Data["enabled"]) != "true" || string(sec.Data["password"]) != "abc123" {
		t.Fatalf("complete changed enabled/password: %q %q", sec.Data["enabled"], sec.Data["password"])
	}
	if len(sec.Data[".dockerconfigjson"]) == 0 || len(sec.Data["cnb_registry_auth"]) == 0 {
		t.Fatal("derived keys missing")
	}
	before := string(sec.Data["htpasswd"])
	_, _ = s.Complete(ctx)
	sec, _ = cs.CoreV1().Secrets("kuso").Get(ctx, SecretName, metav1.GetOptions{})
	if string(sec.Data["htpasswd"]) != before {
		t.Fatal("second Complete rewrote htpasswd")
	}
}
