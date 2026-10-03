package spec

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"kuso/server/internal/kube"
	"kuso/server/internal/projects"
	"kuso/server/internal/registrycreds"
	"kuso/server/internal/secrets"
)

// diffServiceSpec compares the declarative patch apply would send for
// desired (servicePatchReq) against the live CR, projecting each field
// the way projects.PatchService writes it. It returns the MINIMAL
// request — every field whose projected value already matches live is
// dropped, so apply neither churns unchanged fields nor turns an absent
// block into `{}` — plus the per-field diffs for the plan.
//
// CR-only sub-fields the YAML can't express (volume storageClass /
// accessMode, static builder/runtime images, buildpacks lifecycle image)
// are carried over from live into the request, so re-applying doesn't
// silently reset them.
func diffServiceSpec(live *kube.KusoService, desired ServiceSpec) (projects.PatchServiceRequest, []FieldChange) {
	req := servicePatchReq(desired)
	ls := live.Spec
	var out []FieldChange
	add := func(field, from, to string, destructive bool) {
		out = append(out, FieldChange{Field: field, From: from, To: to, Destructive: destructive})
	}

	if *req.Port == ls.Port {
		req.Port = nil
	} else {
		add("port", fmt.Sprint(ls.Port), fmt.Sprint(*req.Port), false)
	}
	if *req.Runtime == ls.Runtime {
		req.Runtime = nil
	} else {
		add("runtime", orNone(ls.Runtime), orNone(*req.Runtime), false)
	}
	if *req.Internal == ls.Internal {
		req.Internal = nil
	} else {
		add("internal", fmt.Sprint(ls.Internal), fmt.Sprint(*req.Internal), false)
	}
	if *req.PrivateEgress == ls.PrivateEgress {
		req.PrivateEgress = nil
	} else {
		add("privateEgress", fmt.Sprint(ls.PrivateEgress), fmt.Sprint(*req.PrivateEgress), false)
	}
	if *req.PlatformAPIEgress == ls.PlatformAPIEgress {
		req.PlatformAPIEgress = nil
	} else {
		add("platformApiEgress", fmt.Sprint(ls.PlatformAPIEgress), fmt.Sprint(*req.PlatformAPIEgress), false)
	}
	if req.WaitForCI != nil {
		if *req.WaitForCI == ls.WaitForCI {
			req.WaitForCI = nil
		} else {
			add("waitForCI", fmt.Sprint(ls.WaitForCI), fmt.Sprint(*req.WaitForCI), false)
		}
	}

	if req.Uptime != nil {
		var liveUp kube.KusoServiceUptime
		if ls.Uptime != nil {
			liveUp = *ls.Uptime
		}
		want := kube.KusoServiceUptime{Disabled: *req.Uptime.Disabled, Path: strings.TrimSpace(*req.Uptime.Path)}
		if want == liveUp {
			req.Uptime = nil
		} else {
			add("uptime", renderUptime(liveUp), renderUptime(want), false)
		}
	}

	// Domains: order matters (the first is the service's public host).
	from, to := renderDomains(ls.Domains), renderServiceDomains(*req.Domains)
	if from == to {
		req.Domains = nil
	} else {
		keep := map[string]bool{}
		for _, d := range *req.Domains {
			keep[d.Host] = true
		}
		removed := false
		for _, d := range ls.Domains {
			if !keep[d.Host] {
				removed = true
			}
		}
		add("domains", from, to, removed)
	}

	// Scale / Sleep: PatchService overwrites min/max/targetCPU and
	// enabled/afterMinutes/nonProduction on the live block (sleep.wakeOn
	// is never touched). No block in the file and none on the CR means
	// nothing to reset — the empty-vs-absent case.
	if desired.Scale == nil && ls.Scale == nil {
		req.Scale = nil
	} else {
		n := len(out)
		liveMin, liveMax, liveCPU := 1, 0, 0
		if ls.Scale != nil {
			liveMin, liveMax, liveCPU = ls.Scale.MinValue(), ls.Scale.Max, ls.Scale.TargetCPU
		}
		if m := *req.Scale.Min; m != liveMin {
			add("scale.min", fmt.Sprint(liveMin), fmt.Sprint(m), m == 0 && liveMin > 0)
		}
		if m := *req.Scale.Max; m != liveMax {
			add("scale.max", fmt.Sprint(liveMax), fmt.Sprint(m), false)
		}
		if c := *req.Scale.TargetCPU; c != liveCPU {
			add("scale.targetCPU", fmt.Sprint(liveCPU), fmt.Sprint(c), false)
		}
		if len(out) == n {
			req.Scale = nil
		}
	}
	if desired.Sleep == nil && ls.Sleep == nil {
		req.Sleep = nil
	} else {
		n := len(out)
		var liveSleep kube.KusoServiceSleep
		if ls.Sleep != nil {
			liveSleep = *ls.Sleep
		}
		if v := *req.Sleep.Enabled; v != liveSleep.Enabled {
			add("sleep.enabled", fmt.Sprint(liveSleep.Enabled), fmt.Sprint(v), false)
		}
		if v := *req.Sleep.AfterMinutes; v != liveSleep.AfterMinutes {
			add("sleep.afterMinutes", fmt.Sprint(liveSleep.AfterMinutes), fmt.Sprint(v), false)
		}
		if v := *req.Sleep.NonProduction; v != liveSleep.NonProduction {
			add("sleep.nonProduction", orNone(liveSleep.NonProduction), orNone(v), false)
		}
		if len(out) == n {
			req.Sleep = nil
		}
	}

	// Placement: nil and an empty override both mean "no constraint of
	// our own", so they compare equal.
	from = renderPlacement(ls.Placement)
	if req.Placement.Clear {
		to = renderPlacement(nil)
	} else {
		to = renderPlacement(&kube.KusoPlacement{Labels: req.Placement.Labels, Nodes: req.Placement.Nodes})
	}
	if from == to {
		req.Placement = nil
	} else {
		add("placement", from, to, false)
	}

	// Volumes: carry storageClass/accessMode over from the live volume of
	// the same name — the YAML has no field for them.
	liveVols := map[string]kube.KusoVolume{}
	for _, v := range ls.Volumes {
		liveVols[v.Name] = v
	}
	vols := *req.Volumes
	for i := range vols {
		if lv, ok := liveVols[vols[i].Name]; ok {
			vols[i].StorageClass = lv.StorageClass
			vols[i].AccessMode = lv.AccessMode
		}
	}
	from, to = renderKubeVolumes(ls.Volumes), renderVolumePatches(vols)
	if from == to {
		req.Volumes = nil
	} else {
		keep := map[string]int{}
		for _, v := range vols {
			keep[v.Name] = v.SizeGi
		}
		destructive := false
		for _, v := range ls.Volumes {
			size, ok := keep[v.Name]
			if !ok || (size > 0 && size < v.SizeGi) {
				destructive = true
			}
		}
		add("volumes", from, to, destructive)
	}

	// Static / Buildpacks: preserve the CR-only image fields; nil and an
	// all-empty block compare equal.
	if ls.Static != nil {
		req.Static.BuilderImage = ls.Static.BuilderImage
		req.Static.RuntimeImage = ls.Static.RuntimeImage
	}
	from, to = renderJSON(normStatic(ls.Static)), renderJSON(normStatic(&kube.KusoStaticSpec{
		BuilderImage: req.Static.BuilderImage, RuntimeImage: req.Static.RuntimeImage,
		BuildCmd: req.Static.BuildCmd, OutputDir: req.Static.OutputDir,
	}))
	if from == to {
		req.Static = nil
	} else {
		add("static", from, to, false)
	}
	if ls.Buildpacks != nil {
		req.Buildpacks.LifecycleImage = ls.Buildpacks.LifecycleImage
	}
	from, to = renderJSON(normBuildpacks(ls.Buildpacks)), renderJSON(normBuildpacks(&kube.KusoBuildpacksSpec{
		BuilderImage: req.Buildpacks.BuilderImage, LifecycleImage: req.Buildpacks.LifecycleImage,
	}))
	if from == to {
		req.Buildpacks = nil
	} else {
		add("buildpacks", from, to, false)
	}

	// Image: PatchService trims, drops an empty repository to nil and
	// defaults the tag to "latest".
	from, to = renderImage(ls.Image), "(none)"
	if repo := strings.TrimSpace(req.Image.Repository); repo != "" {
		tag := strings.TrimSpace(req.Image.Tag)
		if tag == "" {
			tag = "latest"
		}
		to = repo + ":" + tag
		if ps := pullSecretName(ls.Project, *req.Image.PullSecret); ps != "" {
			to += " (pull secret " + ps + ")"
		}
	}
	if from == to {
		req.Image = nil
	} else {
		add("image", from, to, false)
	}

	from, to = renderList(ls.Command), renderList(*req.Command)
	if from == to {
		req.Command = nil
	} else {
		add("command", from, to, false)
	}

	// Release: PatchService treats Clear and an empty command alike and
	// defaults the timeout to 900s.
	from, to = renderRelease(ls.Release), "(none)"
	if !req.Release.Clear && len(req.Release.Command) > 0 {
		timeout := req.Release.TimeoutSeconds
		if timeout <= 0 {
			timeout = 900
		}
		to = renderRelease(&kube.KusoReleaseSpec{Command: req.Release.Command, TimeoutSeconds: timeout})
	}
	if from == to {
		req.Release = nil
	} else {
		add("release", from, to, ls.Release != nil && len(ls.Release.Command) > 0 && to == "(none)")
	}

	from, to = renderList(ls.WatchPaths), renderList(*req.WatchPaths)
	if from == to {
		req.WatchPaths = nil
	} else {
		add("watchPaths", from, to, false)
	}

	from, to = renderMap(ls.BuildArgs), renderMap(*req.BuildArgs)
	if from == to {
		req.BuildArgs = nil
	} else {
		add("buildArgs", from, to, false)
	}
	from, to = renderList(ls.PublicEnv), renderList(*req.PublicEnv)
	if from == to {
		req.PublicEnv = nil
	} else {
		add("publicEnv", from, to, false)
	}

	// SecurityContext: nil in the request means "leave alone", so only
	// a declared block can differ.
	if req.SecurityContext != nil {
		from, to = renderJSON(ls.SecurityContext), renderJSON(req.SecurityContext)
		if from == to {
			req.SecurityContext = nil
		} else {
			add("securityContext", from, to, false)
		}
	}
	return req, out
}

// patchIsEmpty reports whether req would change nothing.
func patchIsEmpty(req projects.PatchServiceRequest) bool {
	v := reflect.ValueOf(req)
	for i := 0; i < v.NumField(); i++ {
		if !v.Field(i).IsZero() {
			return false
		}
	}
	return true
}

// envDiffer compares a service's desired env against the live CR env
// the same way apply's SetEnvPending would rewrite it, plus the
// generated / secret-held keys that live in the managed Secret.
type envDiffer struct {
	ctx     context.Context
	project string
	svcRefs map[string]projects.ServiceRef
	secrets *secrets.Service
	canRead bool // a clientset is wired, so managed-Secret keys are readable
}

func newEnvDiffer(ctx context.Context, k *kube.Client, ns, project string, svcs []kube.KusoService) *envDiffer {
	d := &envDiffer{ctx: ctx, project: project, svcRefs: map[string]projects.ServiceRef{}}
	// Mirror projects.buildServiceResolver: production env host backs
	// PUBLIC_*, a custom domain takes precedence.
	prodEnv := map[string]*kube.KusoEnvironment{}
	if envs, err := k.ListKusoEnvironments(ctx, ns); err == nil {
		for i := range envs {
			if envs[i].Labels[kube.LabelEnv] == "production" {
				prodEnv[envs[i].Spec.Service] = &envs[i]
			}
		}
	}
	for _, s := range svcs {
		port := s.Spec.Port
		if port == 0 {
			port = 8080
		}
		ref := projects.ServiceRef{FQN: s.Name, Port: port, NS: ns}
		if len(s.Spec.Domains) > 0 && s.Spec.Domains[0].Host != "" {
			ref.PublicHost, ref.PublicTLS = s.Spec.Domains[0].Host, s.Spec.Domains[0].TLS
		} else if e := prodEnv[s.Name]; e != nil && e.Spec.Host != "" {
			ref.PublicHost, ref.PublicTLS = e.Spec.Host, e.Spec.TLSEnabled
		}
		d.svcRefs[s.Name] = ref
		d.svcRefs[shortName(project, s.Name)] = ref
	}
	if k.Clientset != nil {
		d.secrets = secrets.New(k, ns)
		d.canRead = true
	}
	return d
}

// canonical env forms: "lit:<value>" or "ref:<addon>.<KEY>" or
// "from:<secret>/<key>" for a non-addon secretKeyRef.
func (d *envDiffer) canonLive(e kube.KusoEnvVar) (string, bool) {
	if e.ValueFrom != nil {
		if ref, ok := reverseAddonConnRef(d.project, e.ValueFrom); ok {
			return "ref:" + strings.TrimSuffix(strings.TrimPrefix(ref, "${{ "), " }}"), true
		}
		b, _ := json.Marshal(e.ValueFrom)
		return "from:" + string(b), true
	}
	if e.Value == "" {
		return "", false
	}
	return "lit:" + e.Value, true
}

func (d *envDiffer) canonDesired(v string) string {
	ref, ok, err := projects.ParseVarRef(v)
	if err != nil || !ok {
		return "lit:" + v
	}
	if projects.IsServiceKey(ref.Key) {
		if sref, found := d.svcRefs[ref.Name]; found {
			return "lit:" + projects.ExpandServiceKey(sref, ref.Key)
		}
	}
	return "ref:" + shortName(d.project, ref.Name) + "." + ref.Key
}

func displayEnv(canon string) string {
	switch {
	case strings.HasPrefix(canon, "ref:"):
		return "${{ " + strings.TrimPrefix(canon, "ref:") + " }}"
	case strings.HasPrefix(canon, "from:"):
		return "(secret ref)"
	case canon == "":
		return "(unset)"
	}
	return "(value)"
}

// diff returns the env field changes, whether the CR env (the list
// SetEnvPending replaces) differs, and warnings for {secret: true}
// keys the managed Secret doesn't hold.
func (d *envDiffer) diff(live *kube.KusoService, service string, desired ServiceSpec) ([]FieldChange, bool, []string) {
	liveEnv := map[string]string{}
	for _, e := range live.Spec.EnvVars {
		if c, ok := d.canonLive(e); ok && e.Name != "" {
			liveEnv[e.Name] = c
		}
	}
	want := map[string]string{}
	for k, v := range desired.Env {
		if v.managedElsewhere() || v.Value == "" {
			continue
		}
		if v.Value == projects.EnvMaskSentinel {
			// Apply resolves the mask to the stored literal ("keep").
			if c := liveEnv[k]; strings.HasPrefix(c, "lit:") {
				want[k] = c
				continue
			}
			want[k] = "lit:" + v.Value
			continue
		}
		want[k] = d.canonDesired(v.Value)
	}

	var out []FieldChange
	keys := map[string]bool{}
	for k := range liveEnv {
		keys[k] = true
	}
	for k := range want {
		keys[k] = true
	}
	for _, k := range sortedKeys(keys) {
		from, to := liveEnv[k], want[k]
		if from == to {
			continue
		}
		toDisp := displayEnv(to)
		if from != "" && to != "" && toDisp == "(value)" && displayEnv(from) == "(value)" {
			toDisp = "(new value)"
		}
		if to == "" {
			toDisp = "(removed)"
			if desired.Env[k].Secret {
				toDisp = "(removed; the Secret-held value takes effect)"
			}
		}
		out = append(out, FieldChange{Field: "env." + k, From: displayEnv(from), To: toDisp})
	}
	crEnvChanged := len(out) > 0

	var warnings []string
	var marked, gen []string
	for k, v := range desired.Env {
		if v.Secret {
			marked = append(marked, k)
		}
		if v.IsGenerated() {
			gen = append(gen, k)
		}
	}
	if (len(marked) > 0 || len(gen) > 0) && d.canRead {
		held := map[string]bool{}
		if keysList, err := d.secrets.ListKeys(d.ctx, d.project, service, ""); err == nil {
			for _, k := range keysList {
				held[k] = true
			}
			sort.Strings(gen)
			for _, k := range gen {
				if !held[k] {
					out = append(out, FieldChange{Field: "env." + k, From: "(unset)", To: "{generate: " + desired.Env[k].Generate + "}"})
				}
			}
			sort.Strings(marked)
			for _, k := range marked {
				if !held[k] {
					warnings = append(warnings, missingSecretWarning(d.project, service, k))
				}
			}
		}
	}
	return out, crEnvChanged, warnings
}

func missingSecretWarning(project, service, key string) string {
	return fmt.Sprintf("service %s: env %s is {secret: true} but the service's Secret doesn't hold it — apply won't set it; run `kuso env set %s %s %s=…`", service, key, project, service, key)
}

// secretMarkedKeys returns the sorted {secret: true} keys of an env map.
func secretMarkedKeys(env map[string]EnvValue) []string {
	var out []string
	for k, v := range env {
		if v.Secret {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// diffAddon compares an exported live addon against the desired spec.
// Fields the file leaves empty are "unspecified" (the server filled a
// default on create), not a difference.
func diffAddon(live, desired AddonSpec) []FieldChange {
	var out []FieldChange
	cmp := func(field, from, to string) {
		if from != to {
			out = append(out, FieldChange{Field: field, From: orNone(from), To: orNone(to)})
		}
	}
	cmp("kind", live.Kind, desired.Kind)
	for _, p := range []struct{ field, from, to string }{
		{"version", live.Version, desired.Version},
		{"size", live.Size, desired.Size},
		{"storageSize", live.StorageSize, desired.StorageSize},
		{"database", live.Database, desired.Database},
		{"tls", live.TLS, desired.TLS},
	} {
		if p.to != "" {
			cmp(p.field, p.from, p.to)
		}
	}
	cmp("ha", fmt.Sprint(live.HA), fmt.Sprint(desired.HA))
	cmp("useInstanceAddon", live.UseInstanceAddon, desired.UseInstanceAddon)
	cmp("pooler", renderJSON(normPooler(live.Pooler)), renderJSON(normPooler(desired.Pooler)))
	cmp("backup", renderJSON(normBackup(live.Backup)), renderJSON(normBackup(desired.Backup)))
	cmp("placement", renderPlacementSpec(live.Placement), renderPlacementSpec(desired.Placement))
	cmp("external", renderJSON(live.External), renderJSON(desired.External))
	return out
}

// diffCron compares an exported live cron against the desired spec,
// honouring cronUpdateReq's semantics (a nil command / empty image
// leaves the live value alone). notApplied holds differences
// UpdateProject can't make (kind).
func diffCron(live, desired CronSpec) (fields, notApplied []FieldChange) {
	cmp := func(field, from, to string) {
		if from != to {
			fields = append(fields, FieldChange{Field: field, From: orNone(from), To: orNone(to)})
		}
	}
	cmp("schedule", live.Schedule, desired.Schedule)
	cmp("suspend", fmt.Sprint(live.Suspend), fmt.Sprint(desired.Suspend))
	cmp("pinImage", fmt.Sprint(live.PinImage), fmt.Sprint(desired.PinImage))
	if desired.Command != nil {
		cmp("command", renderList(live.Command), renderList(desired.Command))
	}
	if desired.Kind == "http" {
		cmp("url", live.URL, strings.TrimSpace(desired.URL))
	}
	if desired.Kind == "command" && desired.Image != "" {
		cmp("image", live.Image, joinImage(splitImage(desired.Image)))
	}
	if live.Kind != desired.Kind {
		notApplied = append(notApplied, FieldChange{Field: "kind", From: live.Kind, To: desired.Kind + " (not applied: recreate the cron to change its kind)"})
	}
	return fields, notApplied
}

// --- rendering / normalization ---

func orNone(s string) string {
	if s == "" {
		return "(none)"
	}
	return s
}

func renderList(in []string) string {
	if len(in) == 0 {
		return "(none)"
	}
	return "[" + strings.Join(in, " ") + "]"
}

func renderMap(in map[string]string) string {
	if len(in) == 0 {
		return "(none)"
	}
	parts := make([]string, 0, len(in))
	for _, k := range sortedMapKeys(in) {
		parts = append(parts, k+"="+in[k])
	}
	return "{" + strings.Join(parts, ", ") + "}"
}

func renderDomain(host string, tls bool, secret string) string {
	s := host
	if tls {
		s += "(tls)"
	}
	if secret != "" {
		s += "(secret " + secret + ")"
	}
	return s
}

func renderDomains(in []kube.KusoDomain) string {
	if len(in) == 0 {
		return "(none)"
	}
	parts := make([]string, 0, len(in))
	for _, d := range in {
		parts = append(parts, renderDomain(d.Host, d.TLS, d.TLSSecret))
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

func renderServiceDomains(in []projects.ServiceDomain) string {
	kd := make([]kube.KusoDomain, 0, len(in))
	for _, d := range in {
		kd = append(kd, kube.KusoDomain{Host: d.Host, TLS: d.TLS, TLSSecret: d.TLSSecret})
	}
	return renderDomains(kd)
}

func renderVolume(name, mount string, size int, class, mode string) string {
	s := fmt.Sprintf("%s:%s(%dGi", name, mount, size)
	if class != "" {
		s += " " + class
	}
	if mode != "" {
		s += " " + mode
	}
	return s + ")"
}

func renderKubeVolumes(in []kube.KusoVolume) string {
	if len(in) == 0 {
		return "(none)"
	}
	parts := make([]string, 0, len(in))
	for _, v := range in {
		parts = append(parts, renderVolume(v.Name, v.MountPath, v.SizeGi, v.StorageClass, v.AccessMode))
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

func renderVolumePatches(in []projects.VolumePatch) string {
	if len(in) == 0 {
		return "(none)"
	}
	parts := make([]string, 0, len(in))
	for _, v := range in {
		parts = append(parts, renderVolume(v.Name, v.MountPath, v.SizeGi, v.StorageClass, v.AccessMode))
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

func renderPlacement(p *kube.KusoPlacement) string {
	if p == nil || (len(p.Labels) == 0 && len(p.Nodes) == 0) {
		return "(project default)"
	}
	var parts []string
	if len(p.Labels) > 0 {
		parts = append(parts, "labels "+renderMap(p.Labels))
	}
	if len(p.Nodes) > 0 {
		parts = append(parts, "nodes "+renderList(p.Nodes))
	}
	return strings.Join(parts, " ")
}

func renderPlacementSpec(p *PlacementSpec) string {
	if p == nil {
		return renderPlacement(nil)
	}
	return renderPlacement(&kube.KusoPlacement{Labels: p.Labels, Nodes: p.Nodes})
}

func renderImage(img *kube.KusoImage) string {
	if img == nil || img.Repository == "" {
		return "(none)"
	}
	tag := img.Tag
	if tag == "" {
		tag = "latest"
	}
	out := img.Repository + ":" + tag
	if img.PullSecret != "" {
		out += " (pull secret " + img.PullSecret + ")"
	}
	return out
}

// pullSecretName maps a kuso.yaml pullSecret reference (registry host or
// Secret name) to the Secret name PatchService stores, so a file saying
// "ghcr.io" compares equal to a live "<project>-regcred-ghcr-io".
func pullSecretName(project, ref string) string {
	ref = strings.TrimSpace(ref)
	if ref == "" || strings.HasPrefix(ref, project+"-regcred-") {
		return ref
	}
	host, err := registrycreds.NormalizeRegistry(ref)
	if err != nil {
		return ref
	}
	return registrycreds.SecretName(project, host)
}

func renderRelease(r *kube.KusoReleaseSpec) string {
	if r == nil || len(r.Command) == 0 {
		return "(none)"
	}
	return fmt.Sprintf("%s (timeout %ds)", renderList(r.Command), r.TimeoutSeconds)
}

// renderJSON renders a normalized value; nil renders as "(none)".
func renderJSON(v any) string {
	rv := reflect.ValueOf(v)
	if v == nil || (rv.Kind() == reflect.Pointer && rv.IsNil()) {
		return "(none)"
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(b)
}

func normStatic(s *kube.KusoStaticSpec) *kube.KusoStaticSpec {
	if s == nil || *s == (kube.KusoStaticSpec{}) {
		return nil
	}
	return s
}

func normBuildpacks(s *kube.KusoBuildpacksSpec) *kube.KusoBuildpacksSpec {
	if s == nil || *s == (kube.KusoBuildpacksSpec{}) {
		return nil
	}
	return s
}

func normPooler(p *AddonPoolerSpec) *AddonPoolerSpec {
	if p == nil || !p.Enabled {
		return nil
	}
	return p
}

func normBackup(b *AddonBackupSpec) *AddonBackupSpec {
	if b == nil || *b == (AddonBackupSpec{}) {
		return nil
	}
	return b
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedMapKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func renderUptime(u kube.KusoServiceUptime) string {
	state := "on"
	if u.Disabled {
		state = "off"
	}
	if u.Path != "" {
		state += " path=" + u.Path
	}
	return state
}
