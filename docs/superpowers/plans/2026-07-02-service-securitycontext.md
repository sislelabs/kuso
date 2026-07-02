# Per-service securityContext (capabilities.add + allowPrivilegeEscalation) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax.

**Goal:** Let a KusoService opt into extra Linux capabilities and privilege escalation so images that self-drop root at runtime (setpriv / gosu / su-exec — uptime-kuma and many self-host images) can run. kuso still drops ALL caps by default; this only ADDS what a service explicitly requests.

**Architecture:** Add an optional `securityContext` block to `KusoServiceSpec`, mirror it onto `KusoEnvironmentSpec` (exactly like the existing `Resources` free-form block), propagate it service→env, drift-check it, and render it in the env helm chart's container securityContext. Then update the uptime-kuma marketplace template to request `SETUID`/`SETGID` + `allowPrivilegeEscalation: true`, and re-smoke all 8 catalog apps live.

**Tech Stack:** Go (server-go internal/kube, internal/projects, internal/spec), operator helm chart (kusoenvironment), CRD YAML, cobra CLI, Next.js/React web, live cluster via `dist/kuso-*` CLI + ssh.

## Global Constraints

- **Follow the `Resources map[string]any` pattern verbatim.** `Resources` is the canonical precedent for a free-form pod-config block: it lives on both `KusoServiceSpec` (types.go:272) and `KusoEnvironmentSpec` (types.go:572), is copied in `env_groups.go:607`, propagated in `propagate.go`, drift-checked in `drift.go:294`, and rendered by the env chart. The new field walks the identical path. When unsure, grep `Resources` and mirror.
- **Default posture is unchanged.** With no `securityContext` set, the rendered container keeps `allowPrivilegeEscalation: false` + `capabilities: drop: ["ALL"]` exactly as today. The field only *adds* caps and *optionally* flips allowPrivilegeEscalation.
- **Field shape (narrow by design):** `securityContext: { capabilities: { add: [string] }, allowPrivilegeEscalation: *bool }`. NO runAsUser, NO privileged, NO hostPath. Capability names are the k8s short form without the `CAP_` prefix (e.g. `SETUID`, `SETGID`, `NET_BIND_SERVICE`).
- **CRD apply is a separate manual step.** The release auto-updater only flips image tags; schema changes need `kubectl apply -f` of the CRD via ssh (per CLAUDE.md release flow).
- **kuso module is `kuso/server`**; imports are `kuso/server/internal/...`.
- **Live smoke domain:** `<app>.sislelabs.com` (the cluster's proven wildcard). Cluster: `ssh -i ~/.ssh/keys/hetzner root@kuso.sislelabs.com`. CLI: `dist/kuso-darwin-arm64`.

---

## File Structure

**Modified:**
- `server-go/internal/kube/types.go` — add `KusoSecurityContext` + `KusoCapabilities` types; add `SecurityContext *KusoSecurityContext` to `KusoServiceSpec` and `KusoEnvironmentSpec`.
- `server-go/internal/projects/env_groups.go` — copy `SecurityContext` when building the env spec (~line 607, next to `Resources`).
- `server-go/internal/projects/propagate.go` — mirror `SecurityContext` onto the env CR unconditionally (next to `Healthcheck`, ~line 165) OR via a changed-flag; unconditional mirror is simplest and matches Healthcheck.
- `server-go/internal/projects/drift.go` — add a `SecurityContext` DeepEqual drift check (~line 294, next to `Resources`).
- `operator/helm-charts/kusoenvironment/templates/deployment.yaml` — render `capabilities.add` + `allowPrivilegeEscalation` from `.Values.securityContext` (container securityContext block, ~line 186).
- `operator/helm-charts/kusoenvironment/values.yaml` — document `securityContext: {}` default.
- `operator/config/crd/bases/application.kuso.sislelabs.com_kusoservices.yaml` — add the `securityContext` schema under spec.
- `operator/config/crd/bases/application.kuso.sislelabs.com_kusoenvironments.yaml` — same schema under spec.
- `server-go/internal/spec/spec.go` — add `SecurityContext` to the kuso.yaml `ServiceSpec` (so marketplace templates + config-as-code can set it) + wire it in the apply reconciler path.
- `server-go/internal/marketplace/templates/uptime-kuma/kuso.yaml` — request `SETUID`/`SETGID` + `allowPrivilegeEscalation: true`.
- `web/src/features/projects/*` + a settings UI control — surface the field (read + edit).
- `cli` — expose on the service create/patch path if it has one (check; may be config-as-code only).

**Test:**
- `server-go/internal/projects/propagate_test.go` — extend to assert SecurityContext propagates.
- `server-go/internal/spec/*_test.go` — parse round-trip for the new kuso.yaml field.
- `server-go/internal/marketplace/render_test.go` / `catalog_test.go` — already validate templates; confirm uptime-kuma still passes.

---

## Task 1: Go types — KusoSecurityContext on service + env spec

**Files:**
- Modify: `server-go/internal/kube/types.go`

**Interfaces:**
- Produces:
  - `type KusoCapabilities struct { Add []string \`json:"add,omitempty"\` }`
  - `type KusoSecurityContext struct { Capabilities *KusoCapabilities \`json:"capabilities,omitempty"\`; AllowPrivilegeEscalation *bool \`json:"allowPrivilegeEscalation,omitempty"\` }`
  - `KusoServiceSpec.SecurityContext *KusoSecurityContext \`json:"securityContext,omitempty"\``
  - `KusoEnvironmentSpec.SecurityContext *KusoSecurityContext \`json:"securityContext,omitempty"\``

- [ ] **Step 1: Add the types** near `KusoScaleSpec`/`KusoHealthcheck` definitions:

```go
// KusoCapabilities lists Linux capabilities to ADD to the container.
// kuso always drops ALL by default; entries here are added back on top.
// Names are the k8s short form without CAP_ (e.g. "SETUID", "SETGID").
type KusoCapabilities struct {
	Add []string `json:"add,omitempty"`
}

// KusoSecurityContext is the narrow, opt-in escape hatch for images that
// self-drop root at runtime (setpriv/gosu/su-exec). Nil = kuso's default
// hardened context (drop ALL, allowPrivilegeEscalation false). Only the
// fields set here relax that default.
type KusoSecurityContext struct {
	Capabilities             *KusoCapabilities `json:"capabilities,omitempty"`
	AllowPrivilegeEscalation *bool             `json:"allowPrivilegeEscalation,omitempty"`
}
```

- [ ] **Step 2: Add the field to `KusoServiceSpec`** (next to `Resources`, ~line 272):

```go
	// SecurityContext opts a service into extra Linux capabilities and/or
	// privilege escalation. Nil keeps kuso's default (drop ALL, no
	// escalation). Propagated onto every owned KusoEnvironment.
	SecurityContext *KusoSecurityContext `json:"securityContext,omitempty"`
```

- [ ] **Step 3: Add the field to `KusoEnvironmentSpec`** (next to its `Resources`, ~line 572):

```go
	SecurityContext *KusoSecurityContext `json:"securityContext,omitempty"`
```

- [ ] **Step 4: Build**

Run: `cd server-go && go build ./...`
Expected: compiles.

- [ ] **Step 5: Commit**

```bash
git add server-go/internal/kube/types.go
git commit -m "feat(service): KusoSecurityContext type on service + env spec"
```

---

## Task 2: Propagate + copy + drift-check service→env

**Files:**
- Modify: `server-go/internal/projects/env_groups.go`
- Modify: `server-go/internal/projects/propagate.go`
- Modify: `server-go/internal/projects/drift.go`
- Test: `server-go/internal/projects/propagate_test.go`

**Interfaces:**
- Consumes: `KusoSecurityContext` (Task 1).
- Produces: SecurityContext is copied when an env is first built, mirrored on every propagation pass, and drift-detected so a service-level edit re-propagates.

- [ ] **Step 1: Write the failing test** — extend `propagate_test.go`. Find the existing test that sets `Resources` on a service and asserts it lands on the env (~line 51-90). Add an analogous assertion:

```go
func TestPropagate_SecurityContext(t *testing.T) {
	esc := true
	sc := &kube.KusoSecurityContext{
		Capabilities:             &kube.KusoCapabilities{Add: []string{"SETUID", "SETGID"}},
		AllowPrivilegeEscalation: &esc,
	}
	// Build a service spec with SecurityContext set + one env, run the
	// same propagation entry point the existing Resources test uses,
	// then assert env.Spec.SecurityContext deep-equals sc.
	// (Mirror the existing Resources test's scaffolding exactly — same
	// fake kube client, same svc/env construction, same call.)
	// ...
	if !reflect.DeepEqual(gotEnv.Spec.SecurityContext, sc) {
		t.Fatalf("securityContext not propagated: got %+v want %+v", gotEnv.Spec.SecurityContext, sc)
	}
}
```

(Implementer: copy the exact scaffolding of the existing `Resources` propagation test in this file — same helpers, same call site — substituting SecurityContext. Do not invent a new harness.)

- [ ] **Step 2: Run test to verify it fails**

Run: `cd server-go && go test ./internal/projects/ -run TestPropagate_SecurityContext -v`
Expected: FAIL (env.Spec.SecurityContext is nil — not yet copied).

- [ ] **Step 3: Copy in env_groups.go** (~line 607, in the struct literal that builds the env spec, next to `Resources: item.svc.Spec.Resources,`):

```go
				SecurityContext:  item.svc.Spec.SecurityContext,
```

- [ ] **Step 4: Mirror in propagate.go** (~line 165, next to the unconditional `env.Spec.Healthcheck = svc.Spec.Healthcheck`):

```go
				// SecurityContext mirrors unconditionally like Healthcheck —
				// a service-level caps/escalation edit must reach every env
				// so the chart re-renders the container securityContext.
				env.Spec.SecurityContext = svc.Spec.SecurityContext
```

- [ ] **Step 5: Drift-check in drift.go** (~line 294, next to the `Resources` DeepEqual):

```go
	if !reflect.DeepEqual(svc.Spec.SecurityContext, env.Spec.SecurityContext) {
		changed.Resources = true // reuse an existing changed-flag path, OR add changed.SecurityContext if the flag set is exhaustive
	}
```

(Implementer: check how `drift.go` signals a change — if it sets a specific `changed.X` flag consumed by `propagate.go`'s `Any()`, and SecurityContext is mirrored UNCONDITIONALLY in propagate.go step 4, then the drift signal only needs to trigger a propagation pass at all. If Healthcheck has no dedicated drift flag because it mirrors unconditionally, follow that exact precedent for SecurityContext and SKIP the drift.go change. Inspect how Healthcheck is handled in drift.go before editing — mirror it.)

- [ ] **Step 6: Run test to verify it passes**

Run: `cd server-go && go test ./internal/projects/ -run TestPropagate -v`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add server-go/internal/projects/env_groups.go server-go/internal/projects/propagate.go server-go/internal/projects/drift.go server-go/internal/projects/propagate_test.go
git commit -m "feat(service): propagate SecurityContext service→env with drift"
```

---

## Task 3: Render in the env helm chart

**Files:**
- Modify: `operator/helm-charts/kusoenvironment/templates/deployment.yaml`
- Modify: `operator/helm-charts/kusoenvironment/values.yaml`

**Interfaces:**
- Consumes: the env CR's `spec.securityContext` (surfaced to the chart as `.Values.securityContext` by the operator — confirm how the operator maps env spec → helm values; it should be automatic since the whole spec becomes values).

- [ ] **Step 1: Render the container securityContext** — replace the current hardcoded block (deployment.yaml ~line 186):

```yaml
          securityContext:
            allowPrivilegeEscalation: {{ if and .Values.securityContext .Values.securityContext.allowPrivilegeEscalation }}true{{ else }}false{{ end }}
            capabilities:
              drop: ["ALL"]
              {{- with .Values.securityContext }}
              {{- with .capabilities }}
              {{- with .add }}
              add:
                {{- range . }}
                - {{ . | quote }}
                {{- end }}
              {{- end }}
              {{- end }}
              {{- end }}
```

Verify: with no securityContext, this renders exactly the current output (`allowPrivilegeEscalation: false`, `drop: ["ALL"]`, no `add:`). With `securityContext.capabilities.add: [SETUID,SETGID]` + `allowPrivilegeEscalation: true`, it renders both.

- [ ] **Step 2: Document the default** in values.yaml (near `resources: {}`):

```yaml
# securityContext: opt-in extra Linux capabilities / privilege escalation
# for images that self-drop root at runtime (setpriv/gosu/su-exec).
# Default (empty) = drop ALL caps, allowPrivilegeEscalation false.
#   securityContext:
#     capabilities: { add: ["SETUID", "SETGID"] }
#     allowPrivilegeEscalation: true
securityContext: {}
```

- [ ] **Step 3: Lint/template the chart** to confirm both render paths:

Run: `cd operator && helm template test helm-charts/kusoenvironment --set securityContext=null | grep -A6 'securityContext:'` (default path) then `... --set securityContext.capabilities.add[0]=SETUID --set securityContext.allowPrivilegeEscalation=true | grep -A8 'securityContext:'`
Expected: default shows drop ALL + no add; override shows add + allowPrivilegeEscalation true. (If helm isn't installed locally, note it and rely on the live smoke in Task 7.)

- [ ] **Step 4: Commit**

```bash
git add operator/helm-charts/kusoenvironment/templates/deployment.yaml operator/helm-charts/kusoenvironment/values.yaml
git commit -m "feat(chart): render opt-in container securityContext (caps.add + allowPrivilegeEscalation)"
```

---

## Task 4: CRD schema (service + environment)

**Files:**
- Modify: `operator/config/crd/bases/application.kuso.sislelabs.com_kusoservices.yaml`
- Modify: `operator/config/crd/bases/application.kuso.sislelabs.com_kusoenvironments.yaml`

- [ ] **Step 1: Add the schema** under `spec.properties` in BOTH CRDs (mirror the indentation of the existing `resources`/`scale` blocks):

```yaml
                securityContext:
                  type: object
                  description: >-
                    Opt-in extra Linux capabilities and privilege escalation
                    for images that self-drop root at runtime. Default drops
                    ALL caps with no escalation.
                  properties:
                    capabilities:
                      type: object
                      properties:
                        add:
                          type: array
                          items:
                            type: string
                    allowPrivilegeEscalation:
                      type: boolean
```

- [ ] **Step 2: Validate YAML**

Run: `cd operator && for f in config/crd/bases/application.kuso.sislelabs.com_kuso{services,environments}.yaml; do python3 -c "import yaml,sys; yaml.safe_load(open('$f'))" && echo "$f OK"; done`
Expected: both OK.

- [ ] **Step 3: Commit**

```bash
git add operator/config/crd/bases/application.kuso.sislelabs.com_kusoservices.yaml operator/config/crd/bases/application.kuso.sislelabs.com_kusoenvironments.yaml
git commit -m "feat(crd): securityContext schema on kusoservice + kusoenvironment"
```

---

## Task 5: kuso.yaml (config-as-code + marketplace) support

**Files:**
- Modify: `server-go/internal/spec/spec.go`
- Modify: `server-go/internal/spec/apply.go` (wire the field into the create/patch request)
- Modify: `server-go/internal/marketplace/templates/uptime-kuma/kuso.yaml`
- Test: `server-go/internal/spec/spec_test.go` (or the nearest existing spec parse test)

**Interfaces:**
- Consumes: `KusoSecurityContext` (Task 1).
- Produces: `spec.ServiceSpec.SecurityContext *SecuritySpec` where `SecuritySpec` mirrors the shape; the apply reconciler sets it on the KusoService via the projects create/patch request.

- [ ] **Step 1: Add spec types** to spec.go (near the ServiceSpec optional blocks):

```go
type SecuritySpec struct {
	Capabilities             *CapabilitiesSpec `yaml:"capabilities,omitempty"`
	AllowPrivilegeEscalation *bool             `yaml:"allowPrivilegeEscalation,omitempty"`
}

type CapabilitiesSpec struct {
	Add []string `yaml:"add,omitempty"`
}
```

Add to `ServiceSpec`:
```go
	SecurityContext *SecuritySpec `yaml:"securityContext,omitempty"`
```

- [ ] **Step 2: Wire into apply** — in `apply.go`, wherever the reconciler maps `spec.ServiceSpec` → `projects.CreateServiceRequest`/`PatchServiceRequest`, translate `SecurityContext` into the `kube.KusoSecurityContext` shape and pass it through. (Check the projects request types carry the field — if `CreateServiceRequest` doesn't have a SecurityContext field, add it there and set it on `svc.Spec.SecurityContext` in `AddService`/`PatchService`.)

- [ ] **Step 3: Write a parse round-trip test** — add to the spec test file: parse a kuso.yaml with `securityContext: { capabilities: { add: [SETUID, SETGID] }, allowPrivilegeEscalation: true }`, assert the parsed struct carries it, and (if MarshalFile-equivalent exists) that it round-trips.

- [ ] **Step 4: Update the uptime-kuma template** — add to the service in `templates/uptime-kuma/kuso.yaml`:

```yaml
      securityContext:
        capabilities:
          add: ["SETUID", "SETGID", "DAC_OVERRIDE"]
        allowPrivilegeEscalation: true
```

(SETUID+SETGID cover setpriv's setgroups; DAC_OVERRIDE covers the entrypoint chowning /app/data. Trim to the minimal set that actually works during the live smoke — start with SETUID+SETGID, add DAC_OVERRIDE only if the smoke still fails on a permissions error.)

- [ ] **Step 5: Run tests**

Run: `cd server-go && go build ./... && go test ./internal/spec/ ./internal/marketplace/ -v 2>&1 | tail -20`
Expected: build + tests pass; uptime-kuma still passes the catalog guardrail (the new field must be a valid spec.ServiceSpec field so Parse accepts it).

- [ ] **Step 6: Commit**

```bash
git add server-go/internal/spec/ server-go/internal/marketplace/templates/uptime-kuma/kuso.yaml
git commit -m "feat(spec): securityContext in kuso.yaml + uptime-kuma requests setpriv caps"
```

---

## Task 6: Web + CLI surface

**Files:**
- Modify: web service-settings UI + `web/src/features/projects/*` types.
- Modify: CLI service create/patch if applicable.

**Interfaces:**
- Consumes: the server wire shape `securityContext: {capabilities:{add:[]},allowPrivilegeEscalation}`.

- [ ] **Step 1: Web types** — add `securityContext?` to the service TS type in `web/src/features/projects/api.ts` (or wherever the service shape lives), matching the wire shape.

- [ ] **Step 2: Web UI** — add a minimal control in the service settings panel (`web/src/components/service/overlay/settings/`): a capabilities multi-input (or comma-separated text) + an allowPrivilegeEscalation toggle, gated behind an "Advanced / security" disclosure so it doesn't clutter the common path. Read + PATCH via the existing service-patch mutation. Keep it small — this is an escape hatch, not a headline feature.

- [ ] **Step 3: CLI** — if there is a `kuso service create/patch` with flags for scale/etc, add `--cap-add` (repeatable) + `--allow-privilege-escalation`. If service mutation is config-as-code-only (via `kuso apply`), the spec.go change in Task 5 already covers it — note that and skip.

- [ ] **Step 4: Typecheck + build**

Run: `cd web && npx tsc --noEmit && npm run build`
Expected: clean; static export succeeds.

- [ ] **Step 5: Commit**

```bash
git add web/src cli
git commit -m "feat(web,cli): surface per-service securityContext"
```

---

## Task 7: Ship, apply CRD, and live-smoke ALL 8 apps

**Files:**
- Modify: `docs/AGENT_SMOKE_TEST.md` (record results)

**Interfaces:**
- Consumes: everything above; the live cluster.

- [ ] **Step 1: Version bump + build artifacts**

Run: `cd /Users/sisle/code/work/kuso && (cd web && npm run build) && (cd server-go && go build ./...) && (cd cli && go build -o /tmp/kuso ./cmd)`
Expected: all build.

- [ ] **Step 2: Ship the release** (real, not dry-run — the flag is `--dry-run`, NOT `DRY_RUN=1`):

Run: `make ship VERSION=v0.18.106`
Expected: images pushed, GH release cut, version files bumped + committed + pushed.

- [ ] **Step 3: Apply the CRD schema changes** (the auto-updater does NOT do this):

```bash
scp -i ~/.ssh/keys/hetzner operator/config/crd/bases/application.kuso.sislelabs.com_kuso{services,environments}.yaml root@kuso.sislelabs.com:/tmp/
ssh -i ~/.ssh/keys/hetzner root@kuso.sislelabs.com "kubectl apply -f /tmp/application.kuso.sislelabs.com_kusoservices.yaml -f /tmp/application.kuso.sislelabs.com_kusoenvironments.yaml"
# restart the operator so its informer picks up the schema
ssh -i ~/.ssh/keys/hetzner root@kuso.sislelabs.com "kubectl -n kuso rollout restart deploy/kuso-operator 2>/dev/null || kubectl -n kuso delete pod -l app.kubernetes.io/name=kuso-operator"
```

- [ ] **Step 4: Wait for the server to roll to v0.18.106**

Poll: `ssh -i ~/.ssh/keys/hetzner root@kuso.sislelabs.com "kubectl -n kuso get deploy kuso-server -o jsonpath='{.spec.template.spec.containers[0].image}'"` until it shows v0.18.106 (updater tick, or trigger via dashboard/`POST /api/system/update`).

- [ ] **Step 5: Smoke ALL 8 apps** — for each of uptime-kuma, umami, n8n, vaultwarden, gitea, metabase, plausible, listmonk:

```bash
# per app <A>:
/tmp/kuso marketplace deploy <A> --project smoke-<A> --set host=smoke-<A>.sislelabs.com
# wait for rollout, then check:
ssh -i ~/.ssh/keys/hetzner root@kuso.sislelabs.com "kubectl -n kuso rollout status deploy/smoke-<A>-<svc>-production --timeout=240s"
curl -I https://smoke-<A>.sislelabs.com   # expect 200/302 once cert + rollout land
```

Record per-app: reached Ready? HTTP status? any crash (capture logs)? For DB-backed apps allow longer (addon must come up first). uptime-kuma is the critical one — it MUST now run (that was the whole point). Apps that self-drop root and still fail → note which caps they need; add to their template's securityContext and re-smoke.

- [ ] **Step 6: Teardown** — delete every `smoke-<A>` project:

```bash
for A in uptime-kuma umami n8n vaultwarden gitea metabase plausible listmonk; do
  ssh -i ~/.ssh/keys/hetzner root@kuso.sislelabs.com "kubectl -n kuso delete kusoproject smoke-$A --wait=false"
done
```

- [ ] **Step 7: Record results in docs/AGENT_SMOKE_TEST.md** — update the marketplace section with the per-app smoke matrix (which run clean, which needed extra caps). Commit:

```bash
git add docs/AGENT_SMOKE_TEST.md
git commit -m "docs(smoke): marketplace all-8 live smoke results + required caps per app"
```

---

## Self-Review Notes

- **Spec coverage:** securityContext field on service+env spec (T1), propagation+drift (T2), chart render (T3), CRD schema (T4), kuso.yaml + marketplace template (T5), web+CLI surface (T6), ship+CRD-apply+smoke-all (T7). Default-unchanged posture asserted in T3 step 1. uptime-kuma fix in T5 step 4, validated live in T7 step 5.
- **Placeholder scan:** T2 step 1 and T6 steps 2-3 reference "mirror the existing pattern / check if a flag exists" — these are deliberate "inspect before editing" instructions with the exact precedent named (Resources test, Healthcheck drift handling), not vague TODOs. T5 step 2 flags a conditional (add field to CreateServiceRequest if absent) with the concrete check.
- **Type consistency:** `KusoSecurityContext{Capabilities *KusoCapabilities, AllowPrivilegeEscalation *bool}` used identically in T1/T2; spec.go mirror `SecuritySpec` in T5; TS `securityContext?` in T6. Field name `securityContext` (json/yaml) consistent across CRD, chart values, spec, web.
- **Risk:** the exact cap set uptime-kuma needs is empirically confirmed only at T7 smoke — T5 starts with SETUID+SETGID+DAC_OVERRIDE and the plan says trim/extend based on the live result. That's the correct place to nail it down (live), not guessed at author time.
