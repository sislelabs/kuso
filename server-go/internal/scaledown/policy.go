package scaledown

import (
	"strings"

	"kuso/server/internal/kube"
)

// defaultAfterMinutes is the idle window when the service sets none.
const defaultAfterMinutes = 30

// Policy is the sleep decision for one env, independent of traffic.
type Policy struct {
	// Production: the env is the service's production env. Production
	// sleeps only when the service opted in (sleep.enabled); every other
	// env sleeps by default unless the service set sleep.nonProduction=off.
	Production bool
	// Allowed: policy lets this env sleep. Ignores stopped, which only
	// blocks the scale action, so a stop/start doesn't flap routing.
	Allowed bool
	// Stopped: the service or env is hard-stopped (pinned to 0 by the
	// operator, must not be slept or woken).
	Stopped bool
	// AutoRoute is the desired env.spec.autoSleep: route the Ingress via
	// the activator because the non-production default applies. False for
	// production, whose routing rides on the propagated spec.sleep.enabled.
	AutoRoute bool
	// AfterMinutes is the idle window.
	AfterMinutes int
}

// Eligible reports whether the env may be scaled to zero once idle.
func (p Policy) Eligible() bool { return p.Allowed && !p.Stopped }

// resolvePolicy applies the sleep policy. proj may be nil (not found).
func resolvePolicy(proj *kube.KusoProject, svc *kube.KusoService, env *kube.KusoEnvironment) Policy {
	p := Policy{
		Production:   isProductionEnv(svc, env),
		Stopped:      svc.Spec.Stopped || env.Spec.Stopped,
		AfterMinutes: defaultAfterMinutes,
	}
	sl := svc.Spec.Sleep
	if sl != nil && sl.AfterMinutes > 0 {
		p.AfterMinutes = sl.AfterMinutes
	}

	// Hard off-switches. A worker or internal env has no Ingress, so no
	// request can reach the activator to wake it. excludePaths means "some
	// path must stay reachable" (same guard as effectiveScaleMin).
	// Project alwaysOn opts the whole project out of scale-to-zero.
	if env.Spec.Runtime == "worker" || env.Spec.Internal || svc.Spec.Internal {
		return p
	}
	if sl != nil && sl.WakeOn != nil && len(sl.WakeOn.ExcludePaths) > 0 {
		return p
	}
	if proj != nil && proj.Spec.AlwaysOn {
		return p
	}

	if p.Production {
		p.Allowed = sl != nil && sl.Enabled
		return p
	}
	p.Allowed = sl == nil || sl.NonProduction != kube.SleepNonProductionOff
	p.AutoRoute = p.Allowed
	return p
}

// isProductionEnv classifies an env. Named envs (kind=custom) and PR
// previews are non-production by kind. Env-group clones are separate
// KusoServices labelled kuso.sislelabs.com/env=<group> whose env CRs still
// carry kind=production, so the group label (on service or env) is what
// marks them.
func isProductionEnv(svc *kube.KusoService, env *kube.KusoEnvironment) bool {
	if svc != nil {
		if v := svc.Labels[kube.LabelEnv]; v != "" && v != "production" {
			return false
		}
	}
	switch env.Spec.Kind {
	case "preview", "custom":
		return false
	}
	if v := env.Labels[kube.LabelEnv]; v != "" && v != "production" {
		return false
	}
	return true
}

// activatorBackend reports whether an Ingress backend Service name is the
// activator: kuso-activator itself (kuso namespace) or the chart's
// per-env ExternalName mirror "<env>-activator" (other namespaces).
func activatorBackend(name string) bool {
	return name == activatorDeployment || strings.HasSuffix(name, "-activator")
}
