package projects

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"

	"kuso/server/internal/kube"
)

// longestEnvSuffix is the longest suffix a service's env CR name ever
// gets: "-production" (11) beats "-pr-NNNNN" (9). The service FQN is
// budgeted against it so the production env and every PR preview fit
// the helm release limit.
const longestEnvSuffix = "-production"

// validateServiceNames checks every helm release name a service will
// derive: the service CR itself, its production/preview env CRs, and
// any extra env CR names supplied (custom envs).
func validateServiceNames(project, service string, envCRNames ...string) error {
	fqn := serviceCRName(project, service)
	if err := kube.ValidateReleaseName(fqn + longestEnvSuffix); err != nil {
		budget := kube.MaxReleaseNameLen - len(longestEnvSuffix) - len(project) - 1
		return fmt.Errorf("%w: project + service name too long: env %q would be %d characters (helm limit %d); keep the service name to at most %d characters in this project",
			ErrInvalid, fqn+longestEnvSuffix, len(fqn+longestEnvSuffix), kube.MaxReleaseNameLen, max(budget, 0))
	}
	for _, n := range envCRNames {
		if err := kube.ValidateReleaseName(n); err != nil {
			return fmt.Errorf("%w: %w", ErrInvalid, err)
		}
	}
	return nil
}

// checkReleaseNameFree reports a conflict when any kuso CR already owns
// the helm release name. Services, envs, addons and crons all install as
// helm releases named after their CR in the same namespace; a clash makes
// the second one fail with "duplicate release name" forever while the API
// returned 201. Service and env CR names also prefix their managed Secrets
// (<name>-secrets), so env "worker" on "api" would share api-worker's.
func (s *Service) checkReleaseNameFree(ctx context.Context, ns string, names ...string) error {
	for _, n := range names {
		if _, err := s.Kube.GetKusoService(ctx, ns, n); err == nil {
			return fmt.Errorf("%w: name %q is already used by a service in this project", ErrConflict, n)
		} else if !apierrors.IsNotFound(err) {
			return fmt.Errorf("check service %s: %w", n, err)
		}
		if _, err := s.Kube.GetKusoEnvironment(ctx, ns, n); err == nil {
			return fmt.Errorf("%w: name %q is already used by an environment in this project", ErrConflict, n)
		} else if !apierrors.IsNotFound(err) {
			return fmt.Errorf("check environment %s: %w", n, err)
		}
		if _, err := s.Kube.GetKusoAddon(ctx, ns, n); err == nil {
			return fmt.Errorf("%w: name %q is already used by an addon in this project", ErrConflict, n)
		} else if !apierrors.IsNotFound(err) {
			return fmt.Errorf("check addon %s: %w", n, err)
		}
		if _, err := s.Kube.GetKusoCron(ctx, ns, n); err == nil {
			return fmt.Errorf("%w: name %q is already used by a cron in this project", ErrConflict, n)
		} else if !apierrors.IsNotFound(err) {
			return fmt.Errorf("check cron %s: %w", n, err)
		}
	}
	return nil
}

// checkRenameSafe refuses a rename that would lose data or produce
// unrenderable names. Volume PVCs are bound to the env's helm release
// name and can't be moved to a new release, so a service with volumes
// can't be renamed in place: the old env's disks would be deleted with it.
func (s *Service) checkRenameSafe(ctx context.Context, ns, project, oldName, newName string, old *kube.KusoService, envs []kube.KusoEnvironment) error {
	if len(old.Spec.Volumes) > 0 {
		return fmt.Errorf("%w: service %s/%s has persistent volumes; renaming would delete their data (volumes are bound to the environment name). Create a new service and copy the data, or remove the volumes first",
			ErrInvalid, project, oldName)
	}
	// Crons are named after their service and owned by its CR, so the old
	// service's teardown would GC them. Refuse rather than drop them.
	crons, err := s.Kube.ListKusoCrons(ctx, ns)
	if err != nil {
		return fmt.Errorf("list crons: %w", err)
	}
	oldFQN := serviceCRName(project, oldName)
	var cronNames []string
	for i := range crons {
		c := &crons[i]
		if c.Spec.Project == project && (c.Spec.Service == oldFQN || c.Spec.Service == oldName) {
			cronNames = append(cronNames, c.Name)
		}
	}
	if len(cronNames) > 0 {
		sort.Strings(cronNames)
		return fmt.Errorf("%w: service %s/%s has crons (%s); renaming would delete them. Delete the crons, rename, then recreate them on %s",
			ErrInvalid, project, oldName, strings.Join(cronNames, ", "), newName)
	}
	newEnvNames := make([]string, 0, len(envs))
	for i := range envs {
		if s.Kube.Clientset != nil {
			pvcs, err := s.Kube.Clientset.CoreV1().PersistentVolumeClaims(ns).List(ctx, metav1.ListOptions{
				LabelSelector: "app.kubernetes.io/instance=" + envs[i].Name + ",kuso.sislelabs.com/volume",
			})
			if err != nil {
				return fmt.Errorf("list volume PVCs for %s: %w", envs[i].Name, err)
			}
			if len(pvcs.Items) > 0 && envs[i].Spec.Kind != "preview" {
				return fmt.Errorf("%w: environment %s still has volume data (%d PVCs); renaming would delete it",
					ErrInvalid, envs[i].Name, len(pvcs.Items))
			}
		}
		if envs[i].Spec.Kind == "preview" {
			continue
		}
		newEnvNames = append(newEnvNames, fmt.Sprintf("%s-%s-%s", project, newName, envShortName(envs[i].Name, project, oldName)))
	}
	if err := validateServiceNames(project, newName, newEnvNames...); err != nil {
		return err
	}
	return s.checkReleaseNameFree(ctx, ns, append([]string{serviceCRName(project, newName)}, newEnvNames...)...)
}

// copyManagedSecretsForRename copies the service-level and per-env
// managed Secrets (<p>-<old>-secrets, <p>-<old>-<env>-secrets) to their
// <p>-<new>-… names and returns old→new for every copy made.
func (s *Service) copyManagedSecretsForRename(ctx context.Context, ns, project, oldName, newName string, envs []kube.KusoEnvironment) (map[string]string, error) {
	out := map[string]string{}
	if s.Kube.Clientset == nil {
		return out, nil
	}
	candidates := []string{kube.ServiceSecretName(project, oldName)}
	for i := range envs {
		if envs[i].Spec.Kind == "preview" {
			continue
		}
		candidates = append(candidates, kube.EnvSecretName(project, oldName, envShortName(envs[i].Name, project, oldName)))
		if scope := envs[i].Labels[labelEnv]; scope != "" {
			candidates = append(candidates, kube.EnvSecretName(project, oldName, scope))
		}
		for _, n := range envs[i].Spec.EnvFromSecrets {
			if isManagedServiceSecretName(n, project, oldName) {
				candidates = append(candidates, n)
			}
		}
	}
	oldPrefix := project + "-" + oldName + "-"
	newPrefix := project + "-" + newName + "-"
	secrets := s.Kube.Clientset.CoreV1().Secrets(ns)
	for _, from := range candidates {
		if _, done := out[from]; done || !strings.HasPrefix(from, oldPrefix) {
			continue
		}
		src, err := secrets.Get(ctx, from, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			continue
		}
		if err != nil {
			return out, fmt.Errorf("read secret %s: %w", from, err)
		}
		to := newPrefix + strings.TrimPrefix(from, oldPrefix)
		cp := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: to, Namespace: ns, Labels: src.Labels, Annotations: src.Annotations},
			Type:       src.Type,
			Data:       src.Data,
		}
		if _, err := secrets.Create(ctx, cp, metav1.CreateOptions{}); err != nil {
			if !apierrors.IsAlreadyExists(err) {
				return out, fmt.Errorf("copy secret %s → %s: %w", from, to, err)
			}
			// A leftover at the target name would otherwise be mounted
			// in place of the real values; overwrite it.
			existing, gerr := secrets.Get(ctx, to, metav1.GetOptions{})
			if gerr != nil {
				return out, fmt.Errorf("copy secret %s → %s: %w", from, to, gerr)
			}
			// Secret names are raw concatenations, so <p>-<new>-… can be
			// another project's Secret in a shared namespace ("a"+"b-c" vs
			// "a-b"+"c"). Only a leftover labelled for this project may be
			// overwritten.
			if existing.Labels[kube.LabelProject] != project {
				return out, fmt.Errorf("%w: secret %s already exists and doesn't belong to project %s; delete it or pick another name",
					ErrConflict, to, project)
			}
			existing.Data = src.Data
			if _, uerr := secrets.Update(ctx, existing, metav1.UpdateOptions{}); uerr != nil {
				return out, fmt.Errorf("copy secret %s → %s: %w", from, to, uerr)
			}
		}
		out[from] = to
	}
	return out, nil
}

func renameSecretList(in []string, rename map[string]string) []string {
	if in == nil {
		return nil
	}
	out := make([]string, len(in))
	for i, n := range in {
		if to, ok := rename[n]; ok {
			n = to
		}
		out[i] = n
	}
	return out
}

// renameSecretRefs returns a copy of vars with secretKeyRef names
// rewritten per rename. ValueFrom maps are copied, never mutated, since
// the input aliases the old CR's spec.
func renameSecretRefs(vars []kube.KusoEnvVar, rename map[string]string) []kube.KusoEnvVar {
	if vars == nil {
		return nil
	}
	out := make([]kube.KusoEnvVar, len(vars))
	for i, v := range vars {
		out[i] = v
		ref, ok := v.ValueFrom["secretKeyRef"].(map[string]any)
		if !ok {
			continue
		}
		name, _ := ref["name"].(string)
		to, ok := rename[name]
		if !ok {
			continue
		}
		newRef := make(map[string]any, len(ref))
		for k, val := range ref {
			newRef[k] = val
		}
		newRef["name"] = to
		vf := make(map[string]any, len(v.ValueFrom))
		for k, val := range v.ValueFrom {
			vf[k] = val
		}
		vf["secretKeyRef"] = newRef
		out[i].ValueFrom = vf
	}
	return out
}

// validatePort rejects a container port the Service/Ingress can't use.
// 0 is allowed on create (chart default) but not as an explicit patch.
func validatePort(p int32, allowZero bool) error {
	if p == 0 && allowZero {
		return nil
	}
	if p < 1 || p > 65535 {
		return fmt.Errorf("%w: port %d out of range (1-65535)", ErrInvalid, p)
	}
	return nil
}

func validateScalePatch(sc *PatchScaleRequest) error {
	if sc == nil {
		return nil
	}
	if sc.Min != nil && *sc.Min < 0 {
		return fmt.Errorf("%w: scale.min must be >= 0", ErrInvalid)
	}
	if sc.Max != nil && *sc.Max < 0 {
		return fmt.Errorf("%w: scale.max must be >= 0", ErrInvalid)
	}
	if sc.Min != nil && sc.Max != nil && *sc.Max > 0 && *sc.Max < *sc.Min {
		return fmt.Errorf("%w: scale.max (%d) must be >= scale.min (%d)", ErrInvalid, *sc.Max, *sc.Min)
	}
	if sc.TargetCPU != nil && (*sc.TargetCPU < 0 || *sc.TargetCPU > 100) {
		return fmt.Errorf("%w: scale.targetCPU must be 0-100", ErrInvalid)
	}
	// -1 on a window is the patch-only "clear the override" value.
	for name, v := range map[string]*int{
		"scaleUpStabilizationSeconds":   sc.ScaleUpStabilizationSeconds,
		"scaleDownStabilizationSeconds": sc.ScaleDownStabilizationSeconds,
	} {
		if v != nil && (*v < -1 || *v > maxScaleStabilizationSeconds) {
			return fmt.Errorf("%w: scale.%s must be 0-%d (or -1 to reset)", ErrInvalid, name, maxScaleStabilizationSeconds)
		}
	}
	pods, pct := 0, 0
	if sc.ScaleUpPods != nil {
		pods = *sc.ScaleUpPods
	}
	if sc.ScaleUpPercent != nil {
		pct = *sc.ScaleUpPercent
	}
	return validateScaleSpeed(nil, pods, pct, nil)
}

// Bounds for the HPA speed overrides. The window cap is the Kubernetes
// API's own limit for stabilizationWindowSeconds.
const (
	maxScaleStabilizationSeconds = 3600
	maxScaleUpPods               = 100
	maxScaleUpPercent            = 1000
)

func validateScaleSpeed(upWindow *int, upPods, upPercent int, downWindow *int) error {
	for name, v := range map[string]*int{
		"scaleUpStabilizationSeconds":   upWindow,
		"scaleDownStabilizationSeconds": downWindow,
	} {
		if v != nil && (*v < 0 || *v > maxScaleStabilizationSeconds) {
			return fmt.Errorf("%w: scale.%s must be 0-%d", ErrInvalid, name, maxScaleStabilizationSeconds)
		}
	}
	if upPods < 0 || upPods > maxScaleUpPods {
		return fmt.Errorf("%w: scale.scaleUpPods must be 0-%d", ErrInvalid, maxScaleUpPods)
	}
	if upPercent < 0 || upPercent > maxScaleUpPercent {
		return fmt.Errorf("%w: scale.scaleUpPercent must be 0-%d", ErrInvalid, maxScaleUpPercent)
	}
	return nil
}

// normalizeServiceDomains lowercases + validates every host with the
// same rules AddDomain applies, and rejects duplicates.
func normalizeServiceDomains(in []ServiceDomain) ([]ServiceDomain, error) {
	out := make([]ServiceDomain, 0, len(in))
	seen := make(map[string]bool, len(in))
	for _, d := range in {
		host := strings.ToLower(strings.TrimSpace(d.Host))
		if host == "" {
			return nil, fmt.Errorf("%w: domain host required", ErrInvalid)
		}
		if seen[host] {
			return nil, fmt.Errorf("%w: domain %q listed twice", ErrInvalid, host)
		}
		seen[host] = true
		if strings.HasPrefix(host, "*.") {
			if !validHostname(strings.TrimPrefix(host, "*.")) {
				return nil, fmt.Errorf("%w: %q is not a valid wildcard hostname", ErrInvalid, host)
			}
			if strings.TrimSpace(d.TLSSecret) == "" {
				return nil, fmt.Errorf("%w: wildcard host %q needs tlsSecret — the name of a pre-provisioned wildcard cert Secret", ErrInvalid, host)
			}
		} else if !validHostname(host) {
			return nil, fmt.Errorf("%w: %q is not a valid hostname", ErrInvalid, host)
		}
		d.Host = host
		out = append(out, d)
	}
	return out, nil
}

// unmaskBuildArgs resolves the revision/read mask back to the stored
// value ("masked means keep existing"). A masked key with no stored value
// is refused: writing the mask would hand the next build a literal
// "••••••••" as e.g. NPM_TOKEN.
func unmaskBuildArgs(in, current map[string]string) (map[string]string, error) {
	out := make(map[string]string, len(in))
	var missing []string
	for k, v := range in {
		if v == EnvMaskSentinel {
			cur, ok := current[k]
			if !ok || cur == EnvMaskSentinel {
				missing = append(missing, k)
				continue
			}
			v = cur
		}
		out[k] = v
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return nil, fmt.Errorf("%w: buildArgs %s hold a masked value with no stored value to keep; set them explicitly", ErrInvalid, strings.Join(missing, ", "))
	}
	return out, nil
}

// checkDomainsFree refuses newly-added hosts that another env in the
// project already serves, before anything is written — otherwise the
// service spec would record a host the production mirror then rejects.
func (s *Service) checkDomainsFree(ctx context.Context, project, service string, before []kube.KusoDomain, want []ServiceDomain) error {
	had := make(map[string]bool, len(before))
	for _, d := range before {
		had[strings.ToLower(d.Host)] = true
	}
	var added []string
	for _, d := range want {
		if !had[d.Host] {
			added = append(added, d.Host)
		}
	}
	if len(added) == 0 {
		return nil
	}
	ns, err := s.namespaceFor(ctx, project)
	if err != nil {
		return err
	}
	envs, err := s.Kube.ListKusoEnvironmentsByLabels(ctx, ns, map[string]string{labelProject: project})
	if err != nil {
		return fmt.Errorf("list envs for conflict check: %w", err)
	}
	prod := envCRNameFor(project, service, "production")
	for _, h := range added {
		if err := hostConflict(envs, h, prod); err != nil {
			return err
		}
	}
	return nil
}

// hostConflict reports which env other than skipEnv already serves host.
func hostConflict(envs []kube.KusoEnvironment, host, skipEnv string) error {
	for i := range envs {
		if envs[i].Name == skipEnv {
			continue
		}
		if strings.EqualFold(envs[i].Spec.Host, host) {
			return fmt.Errorf("%w: %q is the primary host of env %q", ErrConflict, host, envs[i].Name)
		}
		for _, x := range envs[i].Spec.AdditionalHosts {
			if strings.EqualFold(x, host) {
				return fmt.Errorf("%w: %q already on env %q", ErrConflict, host, envs[i].Name)
			}
		}
		for _, w := range envs[i].Spec.WildcardDomains {
			if strings.EqualFold(w.Host, host) {
				return fmt.Errorf("%w: %q already on env %q", ErrConflict, host, envs[i].Name)
			}
		}
	}
	return nil
}

// validateResources checks a ResourceRequirements-shaped map
// ({"requests":{"cpu":"100m"},"limits":{...}}). The env chart drops
// malformed quantities silently, so the user would never see why their
// limit didn't apply.
func validateResources(res map[string]any) error {
	for section, raw := range res {
		if section != "requests" && section != "limits" {
			return fmt.Errorf("%w: resources.%s: only requests and limits are supported", ErrInvalid, section)
		}
		m, ok := raw.(map[string]any)
		if !ok {
			if raw == nil {
				continue
			}
			return fmt.Errorf("%w: resources.%s must be an object", ErrInvalid, section)
		}
		for name, v := range m {
			var q string
			switch t := v.(type) {
			case string:
				q = t
			case float64:
				q = strconv.FormatFloat(t, 'f', -1, 64)
			case int:
				q = strconv.Itoa(t)
			case int64:
				q = strconv.FormatInt(t, 10)
			default:
				return fmt.Errorf("%w: resources.%s.%s must be a quantity string", ErrInvalid, section, name)
			}
			if _, err := resource.ParseQuantity(q); err != nil {
				return fmt.Errorf("%w: resources.%s.%s %q is not a valid quantity (e.g. 250m, 512Mi)", ErrInvalid, section, name, q)
			}
		}
	}
	return nil
}

// validateVolumes rejects what the env chart would otherwise drop
// silently: non-DNS-1123 names, relative or empty mount paths, and
// duplicate names or mount paths.
func validateVolumes(vols []VolumePatch) error {
	names := map[string]bool{}
	paths := map[string]bool{}
	for _, v := range vols {
		if errs := validation.IsDNS1123Label(v.Name); len(errs) > 0 {
			return fmt.Errorf("%w: volume name %q: must be lowercase letters, digits and dashes (max 63)", ErrInvalid, v.Name)
		}
		if !strings.HasPrefix(v.MountPath, "/") {
			return fmt.Errorf("%w: volume %q: mountPath must be an absolute path", ErrInvalid, v.Name)
		}
		if names[v.Name] {
			return fmt.Errorf("%w: volume name %q listed twice", ErrInvalid, v.Name)
		}
		if paths[v.MountPath] {
			return fmt.Errorf("%w: mountPath %q used by two volumes", ErrInvalid, v.MountPath)
		}
		if v.SizeGi < 0 {
			return fmt.Errorf("%w: volume %q: sizeGi must be >= 0", ErrInvalid, v.Name)
		}
		names[v.Name], paths[v.MountPath] = true, true
	}
	return nil
}
