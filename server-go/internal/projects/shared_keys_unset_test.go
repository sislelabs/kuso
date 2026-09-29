package projects

import (
	"context"
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sfake "k8s.io/client-go/kubernetes/fake"

	"kuso/server/internal/kube"
)

// Unset sharedEnvKeys mounts every shared key, so the list endpoint must
// report every key as subscribed. It reported [], so `env unshare K`
// dropped all keys and `env share K` narrowed a mount-all service to K.
func TestListSubscribableSharedKeys_UnsetMeansAll(t *testing.T) {
	s := fakeService(t,
		seedProject("p", kube.KusoProjectSpec{}),
		seedService("p", "api", kube.KusoServiceSpec{}),
	)
	names := kube.SharedSecretNames("p")
	s.Kube.Clientset = k8sfake.NewSimpleClientset(
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: names[0], Namespace: "kuso"}, Data: map[string][]byte{"B": nil, "A": nil}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: names[1], Namespace: "kuso"}, Data: map[string][]byte{"C": nil}},
	)
	got, err := s.ListSubscribableSharedKeys(context.Background(), "p", "api")
	if err != nil {
		t.Fatalf("ListSubscribableSharedKeys: %v", err)
	}
	if want := []string{"A", "B", "C"}; !reflect.DeepEqual(got.Subscribed, want) {
		t.Errorf("unset subscription reported %v, want %v", got.Subscribed, want)
	}
}

// The pod gets project-shared over instance-shared on a key in both, so the
// revealed value must follow the same precedence or the eye shows a value the
// pod doesn't have.
func TestSharedEnvValues_ProjectWinsOverInstance(t *testing.T) {
	s := fakeService(t, seedProject("p", kube.KusoProjectSpec{}))
	names := kube.SharedSecretNames("p")
	s.Kube.Clientset = k8sfake.NewSimpleClientset(
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: names[0], Namespace: "kuso"}, Data: map[string][]byte{"K": []byte("project")}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: names[1], Namespace: "kuso"}, Data: map[string][]byte{"K": []byte("instance"), "I": []byte("only-instance")}},
	)
	got, err := s.SharedEnvValues(context.Background(), "p", []string{"K", "I", "MISSING"})
	if err != nil {
		t.Fatalf("SharedEnvValues: %v", err)
	}
	want := map[string]string{"K": "project", "I": "only-instance"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}
