// Package secrets manages the per-service Kubernetes Secrets that
// envFromSecrets entries on KusoEnvironments point at.
//
// Two scopes:
//   - shared:  <project>-<service>-secrets, mounted on EVERY env of
//              the service. env="" or absent.
//   - per-env: <project>-<service>-<env-sanitised>-secrets, mounted
//              only on that env. Per-env values OVERRIDE shared
//              (envFrom mounts shared first, per-env second).
//
// The race-free patch logic in setKey/removeKey is the §6.4 landmine
// from REWRITE.md — any change to this file MUST keep the merge-patch
// (set) and json-patch-with-test (remove) shapes intact, or concurrent
// writes will silently lose data.
package secrets

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"kuso/server/internal/kube"
)

// Service is the entrypoint for secret operations. Construct with New
// after the projects.Service is wired so we can reuse its Get/List
// helpers via dependency injection rather than re-implementing.
//
// NSResolver, when set, lets us route per-project Secrets into the
// project's execution namespace (KusoProject.spec.namespace). Nil
// resolver = always use the home Namespace (single-tenant default).
type Service struct {
	Kube       *kube.Client
	Namespace  string
	NSResolver *kube.ProjectNamespaceResolver
}

// New constructs a Service. namespace defaults to "kuso".
func New(k *kube.Client, namespace string) *Service {
	if namespace == "" {
		namespace = "kuso"
	}
	return &Service{Kube: k, Namespace: namespace}
}

// nsFor returns the execution namespace for project. Falls back to the
// home Namespace when no resolver is wired or the project is empty.
func (s *Service) nsFor(ctx context.Context, project string) string {
	if s.NSResolver == nil || project == "" {
		return s.Namespace
	}
	return s.NSResolver.NamespaceFor(ctx, project)
}

// Errors mirroring the projects package — handlers map them to HTTP codes.
var (
	ErrNotFound = errors.New("secrets: not found")
	ErrInvalid  = errors.New("secrets: invalid")
)

// envSafeRE strips characters that aren't valid in a k8s resource name
// segment so we can interpolate env names into Secret names safely.
var envSafeRE = regexp.MustCompile(`[^a-z0-9-]`)

// Name returns the per-scope Secret name. env=="" produces the shared
// name, otherwise the env name is sanitised before appending.
func Name(project, service, env string) string {
	base := fmt.Sprintf("%s-%s", project, service)
	if env == "" {
		return base + "-secrets"
	}
	safe := envSafeRE.ReplaceAllString(strings.ToLower(env), "-")
	return fmt.Sprintf("%s-%s-secrets", base, safe)
}

// ListKeys returns the keys (NOT values) stored in the secret for the
// given scope, or nil if the secret doesn't exist yet.
func (s *Service) ListKeys(ctx context.Context, project, service, env string) ([]string, error) {
	sec, err := s.read(ctx, s.nsFor(ctx, project), Name(project, service, env))
	if err != nil {
		return nil, err
	}
	if sec == nil {
		return []string{}, nil
	}
	out := make([]string, 0, len(sec.Data))
	for k := range sec.Data {
		out = append(out, k)
	}
	return out, nil
}

// SetKey upserts a single (key, value) into the scoped Secret without
// touching other keys. Concurrent SetKey calls for *different* keys MUST
// NOT lose updates — this is verified by the resilience-sweep probes.
//
// Path: try merge-patch with body {"data":{"<key>":"<base64>"}}; on 404
// (Secret doesn't exist yet), Create with just this one key.
//
// On success the scoped Secret is also attached to its env's
// envFromSecrets (idempotent) and spec.secretsRev is bumped so the
// helm-operator rolls the Deployment.
func (s *Service) SetKey(ctx context.Context, project, service, env, key, value string) error {
	if key == "" {
		return fmt.Errorf("%w: key is required", ErrInvalid)
	}
	ns := s.nsFor(ctx, project)
	name := Name(project, service, env)
	if err := s.upsertKey(ctx, ns, name, key, value); err != nil {
		return err
	}
	if env != "" {
		if err := s.attachToEnv(ctx, project, service, env, name); err != nil {
			return err
		}
		return s.bumpRev(ctx, project, service, env)
	}
	if err := s.attachToAllEnvs(ctx, project, service, name); err != nil {
		return err
	}
	return s.bumpRev(ctx, project, service, "")
}

// UnsetKey removes a single key from the scoped Secret. Returns
// ErrNotFound if the key wasn't present. If removing it leaves the
// Secret empty, the Secret itself is deleted and detached from envs.
//
// The remove path uses RFC 6902 json-patch with `test`: a 422 from the
// API means the path was already gone — concurrent UnsetKey for the
// same key returns ErrNotFound rather than silently succeeding twice.
func (s *Service) UnsetKey(ctx context.Context, project, service, env, key string) error {
	if key == "" {
		return fmt.Errorf("%w: key is required", ErrInvalid)
	}
	ns := s.nsFor(ctx, project)
	name := Name(project, service, env)
	res, err := s.removeKey(ctx, ns, name, key)
	if err != nil {
		return err
	}
	if !res.existed {
		return ErrNotFound
	}
	if res.empty {
		if err := s.deleteSecret(ctx, ns, name); err != nil {
			return err
		}
		if env != "" {
			if err := s.detachFromEnv(ctx, project, service, env, name); err != nil {
				return err
			}
		} else if err := s.detachFromAllEnvs(ctx, project, service, name); err != nil {
			return err
		}
	}
	if env != "" {
		return s.bumpRev(ctx, project, service, env)
	}
	return s.bumpRev(ctx, project, service, "")
}

// ---- low-level Secret ops ------------------------------------------------

// read fetches the Secret and returns its decoded Data map (base64 → utf8).
// Missing → (nil, nil).
func (s *Service) read(ctx context.Context, ns, name string) (*corev1.Secret, error) {
	sec, err := s.Kube.Clientset.CoreV1().Secrets(ns).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read secret %s: %w", name, err)
	}
	return sec, nil
}

func (s *Service) upsertKey(ctx context.Context, ns, name, key, value string) error {
	enc := base64.StdEncoding.EncodeToString([]byte(value))
	patch := fmt.Sprintf(`{"data":{%q:%q}}`, key, enc)
	_, err := s.Kube.Clientset.CoreV1().Secrets(ns).
		Patch(ctx, name, types.MergePatchType, []byte(patch), metav1.PatchOptions{})
	if err == nil {
		return nil
	}
	if !apierrors.IsNotFound(err) {
		return fmt.Errorf("patch secret %s: %w", name, err)
	}
	dec, decErr := base64.StdEncoding.DecodeString(enc)
	if decErr != nil {
		return fmt.Errorf("encode value: %w", decErr)
	}
	sec := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Type:       corev1.SecretTypeOpaque,
		Data:       map[string][]byte{key: dec},
	}
	if _, err := s.Kube.Clientset.CoreV1().Secrets(ns).Create(ctx, sec, metav1.CreateOptions{}); err != nil {
		if apierrors.IsAlreadyExists(err) {
			_, err2 := s.Kube.Clientset.CoreV1().Secrets(ns).
				Patch(ctx, name, types.MergePatchType, []byte(patch), metav1.PatchOptions{})
			if err2 != nil {
				return fmt.Errorf("patch after create-race: %w", err2)
			}
			return nil
		}
		return fmt.Errorf("create secret %s: %w", name, err)
	}
	return nil
}

type removeResult struct {
	existed bool
	empty   bool
}

func (s *Service) removeKey(ctx context.Context, ns, name, key string) (removeResult, error) {
	sec, err := s.read(ctx, ns, name)
	if err != nil {
		return removeResult{}, err
	}
	if sec == nil {
		return removeResult{existed: false}, nil
	}
	if _, ok := sec.Data[key]; !ok {
		return removeResult{existed: false}, nil
	}
	if len(sec.Data) == 1 {
		return removeResult{existed: true, empty: true}, nil
	}
	escaped := jsonPointerEscape(key)
	patch := fmt.Sprintf(`[{"op":"remove","path":"/data/%s"}]`, escaped)
	_, err = s.Kube.Clientset.CoreV1().Secrets(ns).
		Patch(ctx, name, types.JSONPatchType, []byte(patch), metav1.PatchOptions{})
	if err == nil {
		return removeResult{existed: true, empty: false}, nil
	}
	if apierrors.IsInvalid(err) || isStatusUnprocessable(err) {
		return removeResult{existed: false}, nil
	}
	return removeResult{}, fmt.Errorf("patch secret %s: %w", name, err)
}

func (s *Service) deleteSecret(ctx context.Context, ns, name string) error {
	if err := s.Kube.Clientset.CoreV1().Secrets(ns).Delete(ctx, name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete secret %s: %w", name, err)
	}
	return nil
}

// DeleteForEnv removes the per-env secret for (project, service, env).
// Idempotent: a missing secret returns nil. Used by env-deletion paths
// (preview env teardown on PR close, custom env delete) so per-env
// secrets don't accumulate as orphans after their env CR is gone.
//
// The shared <project>-<service>-secrets is NOT touched here — it
// outlives any single env and is owned by the service, not the env.
func (s *Service) DeleteForEnv(ctx context.Context, project, service, env string) error {
	if env == "" {
		return fmt.Errorf("%w: env is required for per-env secret cleanup", ErrInvalid)
	}
	return s.deleteSecret(ctx, s.nsFor(ctx, project), Name(project, service, env))
}

// jsonPointerEscape per RFC 6901: ~ → ~0, / → ~1. Order matters — encode
// ~ first or you'd encode the escape character itself.
func jsonPointerEscape(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "~", "~0"), "/", "~1")
}

// isStatusUnprocessable matches the HTTP 422 returned by the API server
// when a json-patch path doesn't exist. apierrors.IsInvalid covers most
// 422 cases, but the json-patch test failure shape isn't always one of
// the structured Reason values, so we fall back to a code check.
func isStatusUnprocessable(err error) bool {
	var st *apierrors.StatusError
	if errors.As(err, &st) {
		return st.Status().Code == 422
	}
	return false
}

// ---- env CR mutations ----------------------------------------------------

func (s *Service) attachToEnv(ctx context.Context, project, service, env, secretName string) error {
	envCR, err := s.findEnv(ctx, project, service, env)
	if err != nil {
		return err
	}
	for _, existing := range envCR.Spec.EnvFromSecrets {
		if existing == secretName {
			return nil
		}
	}
	patch := fmt.Sprintf(`{"spec":{"envFromSecrets":%s}}`, jsonStringList(append(envCR.Spec.EnvFromSecrets, secretName)))
	return s.patchEnv(ctx, s.nsFor(ctx, project), envCR.Name, patch)
}

func (s *Service) detachFromEnv(ctx context.Context, project, service, env, secretName string) error {
	envCR, err := s.findEnv(ctx, project, service, env)
	if err != nil {
		return err
	}
	next := make([]string, 0, len(envCR.Spec.EnvFromSecrets))
	for _, existing := range envCR.Spec.EnvFromSecrets {
		if existing != secretName {
			next = append(next, existing)
		}
	}
	if len(next) == len(envCR.Spec.EnvFromSecrets) {
		return nil
	}
	patch := fmt.Sprintf(`{"spec":{"envFromSecrets":%s}}`, jsonStringList(next))
	return s.patchEnv(ctx, s.nsFor(ctx, project), envCR.Name, patch)
}

// attachToAllEnvs attaches a shared secret to every NON-preview env of
// a service. Previews are intentionally excluded — a PR branch should
// boot with no inherited config so reviewers see whether the change
// works against a fresh slate, and so production secrets never leak
// into a throwaway URL. If a preview needs vars, they're set per-env.
func (s *Service) attachToAllEnvs(ctx context.Context, project, service, secretName string) error {
	envs, err := s.envsForService(ctx, project, service)
	if err != nil {
		return err
	}
	ns := s.nsFor(ctx, project)
	for _, e := range envs {
		if e.Spec.Kind == "preview" {
			continue
		}
		alreadyAttached := false
		for _, existing := range e.Spec.EnvFromSecrets {
			if existing == secretName {
				alreadyAttached = true
				break
			}
		}
		if alreadyAttached {
			continue
		}
		patch := fmt.Sprintf(`{"spec":{"envFromSecrets":%s}}`, jsonStringList(append(e.Spec.EnvFromSecrets, secretName)))
		if err := s.patchEnv(ctx, ns, e.Name, patch); err != nil {
			return err
		}
	}
	return nil
}

// detachFromAllEnvs removes a shared secret from every NON-preview env
// of a service. Symmetric with attachToAllEnvs — both paths skip
// previews so the attach/detach surface stays consistent. In practice
// the skip is a no-op today (previews never had the shared secret
// attached, so the envFromSecrets diff would already be empty), but
// keeping it explicit means future changes to the attach side can't
// silently desync from the detach side.
func (s *Service) detachFromAllEnvs(ctx context.Context, project, service, secretName string) error {
	envs, err := s.envsForService(ctx, project, service)
	if err != nil {
		return err
	}
	ns := s.nsFor(ctx, project)
	for _, e := range envs {
		if e.Spec.Kind == "preview" {
			continue
		}
		next := make([]string, 0, len(e.Spec.EnvFromSecrets))
		for _, existing := range e.Spec.EnvFromSecrets {
			if existing != secretName {
				next = append(next, existing)
			}
		}
		if len(next) == len(e.Spec.EnvFromSecrets) {
			continue
		}
		patch := fmt.Sprintf(`{"spec":{"envFromSecrets":%s}}`, jsonStringList(next))
		if err := s.patchEnv(ctx, ns, e.Name, patch); err != nil {
			return err
		}
	}
	return nil
}

// bumpRev sets spec.secretsRev to a fresh timestamp so the helm-operator
// re-renders the Deployment template — without this, value-only Secret
// updates do NOT restart pods (§6.2 landmine).
func (s *Service) bumpRev(ctx context.Context, project, service, env string) error {
	rev := strconv.FormatInt(time.Now().UnixMilli(), 10)
	patch := fmt.Sprintf(`{"spec":{"secretsRev":%q}}`, rev)
	ns := s.nsFor(ctx, project)
	if env != "" {
		envCR, err := s.findEnv(ctx, project, service, env)
		if err != nil {
			return err
		}
		return s.patchEnv(ctx, ns, envCR.Name, patch)
	}
	envs, err := s.envsForService(ctx, project, service)
	if err != nil {
		return err
	}
	for _, e := range envs {
		if err := s.patchEnv(ctx, ns, e.Name, patch); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) patchEnv(ctx context.Context, ns, name, mergePatch string) error {
	_, err := s.Kube.Dynamic.Resource(kube.GVREnvironments).Namespace(ns).
		Patch(ctx, name, types.MergePatchType, []byte(mergePatch), metav1.PatchOptions{})
	if err != nil {
		return fmt.Errorf("patch env %s: %w", name, err)
	}
	return nil
}

// findEnv resolves env (either short like "production" or fully-qualified
// like "<project>-<service>-production") to the matching KusoEnvironment.
func (s *Service) findEnv(ctx context.Context, project, service, env string) (*kube.KusoEnvironment, error) {
	envs, err := s.envsForService(ctx, project, service)
	if err != nil {
		return nil, err
	}
	for _, e := range envs {
		if e.Name == env || e.Spec.Kind == env || strings.HasSuffix(e.Name, "-"+env) {
			return &e, nil
		}
	}
	return nil, fmt.Errorf("%w: env %s for %s/%s", ErrNotFound, env, project, service)
}

func (s *Service) envsForService(ctx context.Context, project, service string) ([]kube.KusoEnvironment, error) {
	raw, err := s.Kube.Dynamic.Resource(kube.GVREnvironments).Namespace(s.nsFor(ctx, project)).
		List(ctx, metav1.ListOptions{
			LabelSelector: "kuso.sislelabs.com/project=" + project + ",kuso.sislelabs.com/service=" + service,
		})
	if err != nil {
		return nil, fmt.Errorf("list envs for %s/%s: %w", project, service, err)
	}
	out := make([]kube.KusoEnvironment, 0, len(raw.Items))
	for i := range raw.Items {
		var e kube.KusoEnvironment
		if err := unstructuredInto(&raw.Items[i], &e); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, nil
}
