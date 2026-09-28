package addons

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
)

// envGroupSourceAddonKey records a clone's source addon FQN. Stamped at
// creation by env-group cloning and (since the fix) previewdb's named-env
// clones; read by cloneSourceConnFor and projects.envCloneConnByOrigin.
const envGroupSourceAddonKey = "kuso.sislelabs.com/env-group-source-addon"

// HealCloneSourceAnnotations backfills the source annotation on named-env
// clones created before previewdb stamped it. Without it, clone→source
// pairing falls back to name derivation, which a renamed or replaced
// source defeats. A clone is stamped only when the source is unambiguous:
// the clone name is exactly "<source>-<envScope>", that source exists in
// the same namespace + project with the same kind, and it isn't itself an
// env-scoped addon. Anything else is logged and skipped — never guessed.
// Idempotent; returns the number of clones stamped.
func (s *Service) HealCloneSourceAnnotations(ctx context.Context, logger *slog.Logger) (int, error) {
	all, err := s.Kube.ListKusoAddons(ctx, metav1.NamespaceAll)
	if err != nil {
		return 0, fmt.Errorf("list addons: %w", err)
	}
	byKey := make(map[string]*kube.KusoAddon, len(all))
	for i := range all {
		byKey[all[i].Namespace+"/"+all[i].Name] = &all[i]
	}
	stamped := 0
	for i := range all {
		a := &all[i]
		scope := a.Labels[kube.LabelEnv]
		if scope == "" || a.Annotations[envGroupSourceAddonKey] != "" ||
			a.Labels["kuso.sislelabs.com/preview-pr"] != "" ||
			a.Labels["kuso.sislelabs.com/preview-source"] != "" ||
			strings.HasPrefix(scope, "preview-") {
			continue
		}
		project := a.Spec.Project
		if project == "" {
			project = a.Labels[kube.LabelProject]
		}
		skip := func(reason string) {
			logger.Info("clone source backfill: skipped, source not unambiguous",
				"clone", a.Name, "ns", a.Namespace, "scope", scope, "reason", reason)
		}
		if project == "" {
			skip("no project")
			continue
		}
		srcFQN := strings.TrimSuffix(a.Name, "-"+scope)
		if srcFQN == a.Name || !strings.HasPrefix(srcFQN, project+"-") || srcFQN == project {
			skip("name does not encode <source>-<scope>")
			continue
		}
		src, ok := byKey[a.Namespace+"/"+srcFQN]
		switch {
		case !ok:
			skip("derived source " + srcFQN + " does not exist")
			continue
		case !addonOwnedByProject(src, project):
			skip("derived source " + srcFQN + " belongs to another project")
			continue
		case src.Labels[kube.LabelEnv] != "":
			skip("derived source " + srcFQN + " is itself env-scoped")
			continue
		case src.Spec.Kind != a.Spec.Kind:
			skip("derived source " + srcFQN + " is a different kind")
			continue
		}
		if _, err := s.Kube.UpdateKusoAddonWithRetry(ctx, a.Namespace, a.Name, func(cur *kube.KusoAddon) error {
			if cur.Annotations == nil {
				cur.Annotations = map[string]string{}
			}
			if cur.Annotations[envGroupSourceAddonKey] == "" {
				cur.Annotations[envGroupSourceAddonKey] = srcFQN
			}
			return nil
		}); err != nil {
			logger.Warn("clone source backfill: stamp failed", "clone", a.Name, "ns", a.Namespace, "err", err)
			continue
		}
		stamped++
	}
	return stamped, nil
}

// HealInstanceConnHosts rewrites bare instance-server hosts in the conn
// Secrets of instance-backed addons that live outside the home namespace.
// Those Secrets were written before writeInstanceAddonConnSecret qualified
// hosts, so pods in a custom project namespace can't resolve e.g.
// "kuso-instance-pg". The rewrite reuses instanceAddonConnData with the
// Secret's own DSN + password — the role password is NOT rotated (unlike
// ResyncInstanceAddon). Only the managed keys change; other keys and
// labels are kept. Consuming pods pick the new hosts up on their next
// restart; nothing is restarted here. Idempotent; returns the number of
// Secrets rewritten.
func (s *Service) HealInstanceConnHosts(ctx context.Context, logger *slog.Logger) (int, error) {
	if s.Kube.Clientset == nil {
		return 0, errors.New("no typed client")
	}
	all, err := s.Kube.ListKusoAddons(ctx, metav1.NamespaceAll)
	if err != nil {
		return 0, fmt.Errorf("list addons: %w", err)
	}
	healed := 0
	for i := range all {
		a := &all[i]
		if a.Spec.UseInstanceAddon == "" || a.Namespace == s.Namespace {
			continue
		}
		connName := connSecretName(a.Name)
		secrets := s.Kube.Clientset.CoreV1().Secrets(a.Namespace)
		sec, err := secrets.Get(ctx, connName, metav1.GetOptions{})
		if err != nil {
			logger.Warn("instance conn host heal: get secret", "secret", connName, "ns", a.Namespace, "err", err)
			continue
		}
		if h := string(sec.Data["POSTGRES_HOST"]); h == "" || strings.Contains(h, ".") {
			continue
		}
		poolerExists := len(sec.Data["POOLER_HOST"]) > 0
		dsn := string(sec.Data["DIRECT_URL"])
		if dsn == "" && !poolerExists {
			// Without a pooler DATABASE_URL is the direct DSN.
			dsn = string(sec.Data["DATABASE_URL"])
		}
		u, perr := url.Parse(dsn)
		if dsn == "" || perr != nil || strings.Contains(u.Hostname(), ".") {
			logger.Warn("instance conn host heal: skipped, no bare direct DSN to rebuild from",
				"secret", connName, "ns", a.Namespace)
			continue
		}
		data, err := instanceAddonConnData(dsn, string(sec.Data["POSTGRES_PASSWORD"]), poolerExists, s.Namespace)
		if err != nil {
			logger.Warn("instance conn host heal: rebuild", "secret", connName, "ns", a.Namespace, "err", err)
			continue
		}
		for k, v := range data {
			sec.Data[k] = v
		}
		if _, err := secrets.Update(ctx, sec, metav1.UpdateOptions{}); err != nil {
			logger.Warn("instance conn host heal: update", "secret", connName, "ns", a.Namespace, "err", err)
			continue
		}
		healed++
		logger.Info("instance conn host heal: qualified hosts; consuming pods pick it up on next restart",
			"secret", connName, "ns", a.Namespace)
	}
	return healed, nil
}
