package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// waitForConnSecrets blocks until every addon conn Secret (*-conn) the env
// mounts exists. An env group's release hook starts the moment its fresh
// addons are created, before their charts have rendered the conn Secrets;
// the mounts are optional, so the Job ran with no DATABASE_URL and its
// wait-for-addons step had nothing to wait for.
func waitForConnSecrets(ctx context.Context, cs kubernetes.Interface, ns string, envFrom []string, poll time.Duration) error {
	for _, name := range envFrom {
		if !strings.HasSuffix(name, "-conn") {
			continue
		}
		for {
			_, err := cs.CoreV1().Secrets(ns).Get(ctx, name, metav1.GetOptions{})
			if err == nil {
				break
			}
			if !apierrors.IsNotFound(err) {
				return fmt.Errorf("get secret %s/%s: %w", ns, name, err)
			}
			select {
			case <-ctx.Done():
				return fmt.Errorf("secret %s/%s never appeared: %w", ns, name, ctx.Err())
			case <-time.After(poll):
			}
		}
	}
	return nil
}
