package addons

import (
	"context"
	"fmt"
	"sort"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// SecretKeys returns the list of keys present in the addon's
// connection secret. Used by the frontend's variable-reference
// autocomplete (`${{ <addon>.<KEY> }}`). Values are NEVER returned.
//
// Errors:
//   - ErrNotFound when the addon doesn't exist or its connection
//     secret hasn't been generated yet.
func (s *Service) SecretKeys(ctx context.Context, project, addon string) ([]string, error) {
	ns := s.nsFor(ctx, project)
	fqn := addonCRName(project, addon)
	a, err := s.Kube.GetKusoAddon(ctx, ns, fqn)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("%w: addon %s/%s", ErrNotFound, project, addon)
		}
		return nil, fmt.Errorf("get addon: %w", err)
	}

	secretName := connSecretName(a.Name)
	if a.Status != nil {
		if v, ok := a.Status["connectionSecret"].(string); ok && v != "" {
			secretName = v
		}
	}

	sec, err := s.Kube.Clientset.CoreV1().Secrets(ns).Get(ctx, secretName, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("%w: secret %s/%s not yet generated", ErrNotFound, ns, secretName)
		}
		return nil, fmt.Errorf("get secret: %w", err)
	}

	keys := make([]string, 0, len(sec.Data))
	for k := range sec.Data {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys, nil
}

// SecretValues returns the addon's connection secret as a map. THIS
// EXPOSES PLAINTEXT VALUES — gate it behind secrets:read at the HTTP
// boundary. Used by the addon overview to show DATABASE_URL and the
// rest so the user can copy them and connect from local tools.
//
// Errors mirror SecretKeys: ErrNotFound when the addon or its secret
// hasn't been provisioned yet.
func (s *Service) SecretValues(ctx context.Context, project, addon string) (map[string]string, error) {
	ns := s.nsFor(ctx, project)
	fqn := addonCRName(project, addon)
	a, err := s.Kube.GetKusoAddon(ctx, ns, fqn)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("%w: addon %s/%s", ErrNotFound, project, addon)
		}
		return nil, fmt.Errorf("get addon: %w", err)
	}
	secretName := connSecretName(a.Name)
	if a.Status != nil {
		if v, ok := a.Status["connectionSecret"].(string); ok && v != "" {
			secretName = v
		}
	}
	sec, err := s.Kube.Clientset.CoreV1().Secrets(ns).Get(ctx, secretName, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("%w: secret %s/%s not yet generated", ErrNotFound, ns, secretName)
		}
		return nil, fmt.Errorf("get secret: %w", err)
	}
	out := make(map[string]string, len(sec.Data))
	for k, v := range sec.Data {
		out[k] = string(v)
	}
	return out, nil
}
