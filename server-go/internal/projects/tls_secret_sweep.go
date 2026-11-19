package projects

import (
	"context"
	"strings"
	"sync"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// secretNameList lists a namespace's Secret names at most once per delete
// cascade. Finding "<env>-tls-extra-*" needs a prefix match, and a full
// Secret LIST (data included) per env was N lists of the busiest namespace
// for an N-env project or env group.
type secretNameList struct {
	once  sync.Once
	names []string
	err   error
}

type secretNameListKey struct{}

func withSecretNameList(ctx context.Context) context.Context {
	if _, ok := ctx.Value(secretNameListKey{}).(*secretNameList); ok {
		return ctx
	}
	return context.WithValue(ctx, secretNameListKey{}, &secretNameList{})
}

func (s *Service) secretNamesIn(ctx context.Context, ns string) ([]string, error) {
	l, ok := ctx.Value(secretNameListKey{}).(*secretNameList)
	if !ok {
		l = &secretNameList{}
	}
	l.once.Do(func() {
		secs, err := s.Kube.Clientset.CoreV1().Secrets(ns).List(ctx, metav1.ListOptions{})
		if err != nil {
			l.err = err
			return
		}
		l.names = make([]string, 0, len(secs.Items))
		for i := range secs.Items {
			l.names = append(l.names, secs.Items[i].Name)
		}
	})
	return l.names, l.err
}

// deleteEnvTLSSecrets reclaims the cert-manager "<env>-tls" and
// "<env>-tls-extra-<host>" Secrets, which carry no ownerReference and are not
// in the helm release, so nothing else removes them.
func (s *Service) deleteEnvTLSSecrets(ctx context.Context, ns, env string, fail func(kind, name string, err error)) {
	secrets := s.Kube.Clientset.CoreV1().Secrets(ns)
	if derr := secrets.Delete(ctx, env+"-tls", metav1.DeleteOptions{}); derr != nil && !apierrors.IsNotFound(derr) {
		fail("Secret", env+"-tls", derr)
	}
	prefix := env + "-tls-extra-"
	names, lerr := s.secretNamesIn(ctx, ns)
	if lerr != nil {
		fail("SecretList", prefix+"*", lerr)
		return
	}
	for _, n := range names {
		if !strings.HasPrefix(n, prefix) {
			continue
		}
		if derr := secrets.Delete(ctx, n, metav1.DeleteOptions{}); derr != nil && !apierrors.IsNotFound(derr) {
			fail("Secret", n, derr)
		}
	}
}
