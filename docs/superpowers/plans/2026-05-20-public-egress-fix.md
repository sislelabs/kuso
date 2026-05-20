# Kuso Public-Egress Fix Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give kuso runtime service pods a supported way to receive the `kuso.sislelabs.com/network-egress-public=true` label, so public internet egress works by default and every existing service is healed.

**Architecture:** Add an opt-out `PrivateEgress bool` field to `KusoServiceSpec` and `KusoEnvironmentSpec` (mirroring the existing `Internal` field exactly). The helm-operator passes a CR's `.spec` straight through as Helm values, so `KusoEnvironment.spec.privateEgress` becomes `.Values.privateEgress` with no mapping code. The `kusoenvironment` chart's pod template stamps the egress label unless `privateEgress` is true. The `kusoproject` NetworkPolicy chart is unchanged — `allow-public-egress` already keys on that label.

**Tech Stack:** Go (server-go, controller types, CLI — all one `go.work`), Helm charts (operator-sdk helm-operator), Kubernetes CRDs (hand-maintained YAML, frozen by a golden test).

**Verification model:** Go tests via `go test`. The CRD schema is frozen by `TestCRDSchema_GoldenStable` in `server-go/internal/kube/` — regenerate its golden snapshot with `KUSO_UPDATE_GOLDENS=1`. Helm charts have golden render tests under `server-go/internal/kube/` / chart `tests/`. All `go` commands run from `server-go/` or `cli/` as noted (the repo is a `go.work` workspace).

---

## File Structure

Files modified:
- `server-go/internal/kube/types.go` — add `PrivateEgress bool` to `KusoServiceSpec` and `KusoEnvironmentSpec`.
- `server-go/internal/projects/propagate.go` — add `PrivateEgress` to `changedFields` + the propagation loop.
- `server-go/internal/projects/services_ops.go` — detect `privateEgressChanged` in `PatchService`, pass it to `propagateChangedToEnvs`, copy `PrivateEgress` onto the two env-CR creation sites, add `PrivateEgress *bool` to the patch request struct.
- `operator/helm-charts/kusoenvironment/values.yaml` — add `privateEgress: false`.
- `operator/helm-charts/kusoenvironment/templates/deployment.yaml` — conditionally stamp the egress label on the pod template.
- `operator/config/crd/bases/application.kuso.sislelabs.com_kusoservices.yaml` — add `privateEgress` property.
- `operator/config/crd/bases/application.kuso.sislelabs.com_kusoenvironments.yaml` — add `privateEgress` property.
- `dist/crds.yaml` — add `privateEgress` to both bundled CRD schemas.
- `cli/pkg/kusoApi/projects.go` — add `PrivateEgress *bool` to the `PatchService` request struct.
- `cli/cmd/kusoCli/project.go` — add `--private-egress=on|off` flag to `service set`.

Files regenerated:
- `server-go/internal/kube/testdata/crd_schema/*` — golden snapshot, via `KUSO_UPDATE_GOLDENS=1`.

Files unchanged: the `kusoproject` chart (NetworkPolicy templates), `buildcontroller.go` (build pods already get the label).

---

## Task 1: Add `PrivateEgress` to the Go CR types

**Files:**
- Modify: `server-go/internal/kube/types.go`

- [ ] **Step 1: Confirm baseline build is green**

Run (from `server-go/`): `go build ./...`
Expected: succeeds. If it fails, STOP and report BLOCKED — the change must start from a green build.

- [ ] **Step 2: Add the field to `KusoServiceSpec`**

In `server-go/internal/kube/types.go`, find the `Internal bool` field inside `type KusoServiceSpec struct` (it has a multi-line comment ending `...Workers (runtime= worker) implicitly have no Ingress regardless of this flag.`). Immediately AFTER the `Internal bool` line, add:

```go
	// PrivateEgress, when true, denies this service's pods egress to
	// the public internet — they can still reach sibling pods, DNS,
	// and the in-cluster registry. Default false: pods CAN reach the
	// internet (most apps call external APIs). The kusoenvironment
	// chart stamps the kuso.sislelabs.com/network-egress-public label
	// on the pod template unless this is true; the kusoproject
	// NetworkPolicy's allow-public-egress rule keys on that label.
	// Mirrored onto every KusoEnvironment owned by this service.
	PrivateEgress bool `json:"privateEgress,omitempty"`
```

- [ ] **Step 3: Add the mirrored field to `KusoEnvironmentSpec`**

In the same file, find the `Internal bool` field inside `type KusoEnvironmentSpec struct` (its comment ends `...Propagated via propagateInternalToEnvs.`). Immediately AFTER the `Internal bool` line, add:

```go
	// PrivateEgress mirrors KusoService.spec.privateEgress so the
	// kusoenvironment chart (which reads only the env CR) can gate the
	// public-egress pod label. Server-managed: propagated from the
	// service spec by propagateChangedToEnvs.
	PrivateEgress bool `json:"privateEgress,omitempty"`
```

- [ ] **Step 4: Verify build**

Run (from `server-go/`): `go build ./...`
Expected: succeeds, no errors.

- [ ] **Step 5: Commit**

```bash
git add server-go/internal/kube/types.go
git commit -m "feat(types): add PrivateEgress to KusoService and KusoEnvironment specs"
```

---

## Task 2: Propagate `PrivateEgress` service → env

**Files:**
- Modify: `server-go/internal/projects/propagate.go`

- [ ] **Step 1: Add `PrivateEgress` to the `changedFields` struct**

In `server-go/internal/projects/propagate.go`, find `type changedFields struct`. After the `Runtime bool` field (the last field, with its multi-line comment), add:

```go
	// PrivateEgress carries spec.privateEgress changes. The chart
	// stamps the public-egress pod label off the env CR's value, so a
	// service-level toggle that isn't propagated never reaches a pod.
	PrivateEgress bool
```

- [ ] **Step 2: Add `PrivateEgress` to the `any()` method**

In the same file, find `func (c changedFields) any() bool`. Change its return expression to include the new field:

```go
func (c changedFields) any() bool {
	return c.EnvVars || c.Placement || c.Volumes || c.Port || c.Scale || c.Domains || c.Internal || c.Runtime || c.PrivateEgress
}
```

- [ ] **Step 3: Apply the field inside the propagation loop**

In `propagateChangedToEnvs`, inside the `UpdateKusoEnvironmentWithRetry` callback, find the `if changed.Runtime {` block (`env.Spec.Runtime = svc.Spec.Runtime`). Immediately AFTER that block's closing `}`, add:

```go
			if changed.PrivateEgress {
				env.Spec.PrivateEgress = svc.Spec.PrivateEgress
			}
```

- [ ] **Step 4: Verify build**

Run (from `server-go/`): `go build ./...`
Expected: succeeds.

- [ ] **Step 5: Commit**

```bash
git add server-go/internal/projects/propagate.go
git commit -m "feat(propagate): mirror PrivateEgress onto env CRs"
```

---

## Task 3: Wire `PrivateEgress` through `PatchService` + env creation

**Files:**
- Modify: `server-go/internal/projects/services_ops.go`

- [ ] **Step 1: Add `PrivateEgress` to the patch request struct**

In `server-go/internal/projects/services_ops.go`, find the patch-request struct that contains `Internal *bool` (around line 1286, the field with comment `// Internal toggles the public-Ingress gate. true skips the ...`). Immediately AFTER the `Internal *bool ...json:"internal,omitempty"...` line, add:

```go
	// PrivateEgress toggles public-internet egress. true = pods are
	// namespace-internal only; false/unset = pods can reach the
	// internet. Pointer so "unset" (leave alone) is distinguishable.
	PrivateEgress *bool `json:"privateEgress,omitempty"`
```

- [ ] **Step 2: Detect `privateEgressChanged` in `PatchService`**

In the same file, find the `internalChanged` detection block:

```go
	internalChanged := false
	if req.Internal != nil {
		svc.Spec.Internal = *req.Internal
		internalChanged = true
	}
```

Immediately AFTER that block, add the parallel detection:

```go
	privateEgressChanged := false
	if req.PrivateEgress != nil {
		svc.Spec.PrivateEgress = *req.PrivateEgress
		privateEgressChanged = true
	}
```

- [ ] **Step 3: Pass `privateEgressChanged` to the propagation call**

In the same file, find the `propagateChangedToEnvs` call inside `PatchService` — the one passing a multi-field `changedFields{...}` literal with `Internal: internalChanged` and `Runtime: runtimeChanged`. Add the new field to that literal:

```go
	if err := s.propagateChangedToEnvs(ctx, ns, project, service, updated, changedFields{
		Placement:     placementChanged,
		Volumes:       volumesChanged,
		Port:          portChanged,
		Scale:         scaleChanged,
		Domains:       domainsChanged,
		Internal:      internalChanged,
		Runtime:       runtimeChanged,
		PrivateEgress: privateEgressChanged,
	}); err != nil {
```

(Keep the existing body of that `if` unchanged — only the struct literal gains the field.)

- [ ] **Step 4: Copy `PrivateEgress` onto both env-CR creation sites**

There are two places in this file that build a `kube.KusoEnvironmentSpec{...}` literal for a new production env. Both set `Internal:`. In EACH, add a `PrivateEgress:` line right after the `Internal:` line.

Site A — the literal with `Internal: created.Spec.Internal,`:
```go
			Internal:         created.Spec.Internal,
			PrivateEgress:    created.Spec.PrivateEgress,
```

Site B — the literal with `Internal: svc.Spec.Internal,`:
```go
			Internal:         svc.Spec.Internal,
			PrivateEgress:    svc.Spec.PrivateEgress,
```

- [ ] **Step 5: Verify build**

Run (from `server-go/`): `go build ./...`
Expected: succeeds.

- [ ] **Step 6: Commit**

```bash
git add server-go/internal/projects/services_ops.go
git commit -m "feat(services): wire PrivateEgress through PatchService and env creation"
```

---

## Task 4: Propagation unit test

**Files:**
- Modify/Create: a `_test.go` in `server-go/internal/projects/` covering `propagateChangedToEnvs`.

- [ ] **Step 1: Find the existing propagation test**

Run (from repo root): `grep -rln "propagateChangedToEnvs\|changedFields{" server-go/internal/projects/*_test.go`
Read the file(s) returned. Identify the test that exercises propagation of a simple field (e.g. `Internal` or `Runtime`) — it sets up a fake/real `Service` with a `KusoService` + one or more `KusoEnvironment` CRs, calls `propagateChangedToEnvs` with a `changedFields{...}`, and asserts the env CR was updated.

If NO such test exists, report DONE_WITH_CONCERNS noting the propagation path has no existing test harness, and write a minimal test following the pattern of the nearest existing test in that package that constructs a `Service` with a fake Kube client.

- [ ] **Step 2: Add a `PrivateEgress` propagation test case**

Mirror the existing `Internal` (or `Runtime`) propagation test exactly, but for `PrivateEgress`. The test must:
- Create a `KusoService` with `Spec.PrivateEgress = true` and at least one owned `KusoEnvironment` whose `Spec.PrivateEgress` starts `false`.
- Call `propagateChangedToEnvs(ctx, ns, project, service, svc, changedFields{PrivateEgress: true})`.
- Assert every owned env CR now has `Spec.PrivateEgress == true`.

Use the same construction helpers, fake client, and assertion style as the test you read in Step 1. Name the test `TestPropagateChangedToEnvs_PrivateEgress` (or add a subtest to an existing table-driven propagation test if that's the file's pattern).

- [ ] **Step 3: Run the test**

Run (from `server-go/`): `go test ./internal/projects/ -run PrivateEgress -v`
Expected: PASS.

- [ ] **Step 4: Run the full package to catch regressions**

Run (from `server-go/`): `go test ./internal/projects/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add server-go/internal/projects/
git commit -m "test(propagate): cover PrivateEgress service→env propagation"
```

---

## Task 5: `kusoenvironment` chart — values + pod label

**Files:**
- Modify: `operator/helm-charts/kusoenvironment/values.yaml`
- Modify: `operator/helm-charts/kusoenvironment/templates/deployment.yaml`

- [ ] **Step 1: Add the chart value**

In `operator/helm-charts/kusoenvironment/values.yaml`, find the `internal: false` line (it has a comment block above it ending `Mirrors KusoService.spec.internal.`). Immediately AFTER the `internal: false` line, add:

```yaml

# privateEgress: when true, this service's pods are denied public
# internet egress (sibling pods, DNS, and the in-cluster registry are
# still reachable). Default false: pods CAN reach the internet. When
# false, the pod template carries the kuso.sislelabs.com/network-egress-public
# label, which the kusoproject NetworkPolicy's allow-public-egress rule
# selects. Mirrors KusoService.spec.privateEgress.
privateEgress: false
```

- [ ] **Step 2: Stamp the egress label on the pod template**

In `operator/helm-charts/kusoenvironment/templates/deployment.yaml`, find the pod template's label block — the `spec.template.metadata.labels` (NOT the top-level Deployment `metadata.labels`). It looks like:

```yaml
  template:
    metadata:
      labels:
        {{- include "kusoenvironment.selectorLabels" . | nindent 8 }}
        {{- include "kusoenvironment.labels" . | nindent 8 }}
```

Immediately AFTER the `{{- include "kusoenvironment.labels" . | nindent 8 }}` line (still inside that `labels:` block), add:

```yaml
        {{- if not .Values.privateEgress }}
        kuso.sislelabs.com/network-egress-public: "true"
        {{- end }}
```

CRITICAL: this must be the **pod template** label block (under `spec.template.metadata.labels`, indented to `nindent 8`), not the Deployment's own `metadata.labels` near the top of the file. NetworkPolicy `podSelector` matches pod labels only. If you stamp it on the Deployment metadata the fix is inert.

- [ ] **Step 3: Lint the chart**

Run (from repo root): `helm lint operator/helm-charts/kusoenvironment`
Expected: `1 chart(s) linted, 0 chart(s) failed`. If `helm` is unavailable, skip and rely on Task 6's golden render.

- [ ] **Step 4: Render-check the template manually**

Run (from repo root):
```bash
helm template t operator/helm-charts/kusoenvironment --show-only templates/deployment.yaml | grep -A12 "template:" | grep -E "network-egress-public|labels:"
```
Expected: `kuso.sislelabs.com/network-egress-public: "true"` appears under the pod template labels (because `privateEgress` defaults false).

Then:
```bash
helm template t operator/helm-charts/kusoenvironment --set privateEgress=true --show-only templates/deployment.yaml | grep -c "network-egress-public"
```
Expected: `0` (label absent when `privateEgress=true`).

If `helm` is unavailable, skip — Task 6 covers this in the golden tests.

- [ ] **Step 5: Commit**

```bash
git add operator/helm-charts/kusoenvironment/values.yaml operator/helm-charts/kusoenvironment/templates/deployment.yaml
git commit -m "feat(chart): stamp public-egress pod label unless privateEgress set"
```

---

## Task 6: Update chart golden render tests

**Files:**
- Modify: the `kusoenvironment` chart render test(s) under `server-go/internal/kube/` or `operator/helm-charts/kusoenvironment/tests/`.

- [ ] **Step 1: Locate the chart render tests**

Run (from repo root):
```bash
grep -rln "kusoenvironment" server-go/internal/kube/*_test.go operator/helm-charts/kusoenvironment/tests/ 2>/dev/null
ls operator/helm-charts/kusoenvironment/tests/ 2>/dev/null
```
Read whatever render/golden test covers the `kusoenvironment` deployment template. Identify how it asserts pod-template content and where golden output files live.

- [ ] **Step 2: Run the existing render tests to see current state**

Run (from `server-go/`): `go test ./internal/kube/ -run "Render|Golden|Chart" -v 2>&1 | tail -30`
Expected: the new label will likely make an existing golden snapshot stale — note which test fails and which golden file is involved.

- [ ] **Step 3: Add an assertion for the egress label**

If the render test is golden-snapshot based: the new `network-egress-public` label is now part of the default render. Regenerate the affected golden (Step 4) — the snapshot itself becomes the assertion.

If the render test is assertion based (greps the rendered YAML): add two cases — (a) default values → rendered pod template contains `kuso.sislelabs.com/network-egress-public: "true"`; (b) `privateEgress: true` → rendered pod template does NOT contain `network-egress-public`. Follow the file's existing test style.

- [ ] **Step 4: Regenerate any stale golden snapshots**

If the repo uses `KUSO_UPDATE_GOLDENS=1` for chart goldens (same mechanism as the CRD golden test), run (from `server-go/`):
```bash
KUSO_UPDATE_GOLDENS=1 go test ./internal/kube/ -run "Render|Golden|Chart"
```
Then inspect the diff (`git diff` on the golden files) and confirm the ONLY change is the added `network-egress-public` label in the default-render snapshot — nothing else.

- [ ] **Step 5: Run the render tests clean**

Run (from `server-go/`): `go test ./internal/kube/ -run "Render|Golden|Chart"`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add server-go/internal/kube/ operator/helm-charts/kusoenvironment/tests/
git commit -m "test(chart): cover public-egress label in kusoenvironment render"
```

---

## Task 7: Add `privateEgress` to the CRD schemas

**Files:**
- Modify: `operator/config/crd/bases/application.kuso.sislelabs.com_kusoservices.yaml`
- Modify: `operator/config/crd/bases/application.kuso.sislelabs.com_kusoenvironments.yaml`
- Modify: `dist/crds.yaml`

- [ ] **Step 1: Add `privateEgress` to the `kusoservices` CRD**

In `operator/config/crd/bases/application.kuso.sislelabs.com_kusoservices.yaml`, find the `internal:` property (under `spec.properties`, the block with `type: boolean` / `default: false` and a description starting `When true, skip the Ingress entirely...`). Immediately AFTER the `internal:` block (after its `default: false` line, at the SAME indentation as `internal:`), add:

```yaml
                privateEgress:
                  type: boolean
                  description: When true, this service's pods are denied public internet egress (sibling pods, DNS and the in-cluster registry stay reachable). Default false — pods can reach the internet. Mirrored onto every KusoEnvironment owned by this service.
                  default: false
```

Match the exact indentation of the surrounding `internal:` property (the property key is indented 16 spaces in this file).

- [ ] **Step 2: Add `privateEgress` to the `kusoenvironments` CRD**

In `operator/config/crd/bases/application.kuso.sislelabs.com_kusoenvironments.yaml`, find the `internal:` property (description ends `Mirrors KusoService.spec.internal.`). Immediately AFTER its `default: false` line, at the same indentation, add:

```yaml
                privateEgress:
                  type: boolean
                  description: When true, deny public internet egress for this env's pods. Mirrors KusoService.spec.privateEgress.
                  default: false
```

- [ ] **Step 3: Add `privateEgress` to the bundled `dist/crds.yaml`**

`dist/crds.yaml` bundles both CRDs. Run (from repo root): `grep -n "internal:" dist/crds.yaml` — it returns two line numbers (one per CRD). For EACH `internal:` block, find the block's `default: false` line and insert a `privateEgress:` property immediately after it, at the same indentation as `internal:`. Use the kusoservices description text for the block inside the KusoService CRD definition, and the kusoenvironments description text for the block inside the KusoEnvironment CRD definition. To tell which is which: the KusoService block appears under the CRD whose `names.kind` is `KusoService`; the KusoEnvironment block under `names.kind: KusoEnvironment`. (The KusoService one is the earlier line number — its CRD is defined first.)

- [ ] **Step 4: Validate the YAML parses**

Run (from repo root):
```bash
python3 -c "import yaml,sys; list(yaml.safe_load_all(open('operator/config/crd/bases/application.kuso.sislelabs.com_kusoservices.yaml'))); list(yaml.safe_load_all(open('operator/config/crd/bases/application.kuso.sislelabs.com_kusoenvironments.yaml'))); list(yaml.safe_load_all(open('dist/crds.yaml'))); print('YAML OK')"
```
Expected: `YAML OK`. If it errors, the indentation is wrong — fix it.

- [ ] **Step 5: Regenerate the CRD schema golden snapshot**

The CRD schema is frozen by `TestCRDSchema_GoldenStable` in `server-go/internal/kube/crd_schema_test.go` (golden dir `testdata/crd_schema`). Run (from `server-go/`):
```bash
KUSO_UPDATE_GOLDENS=1 go test ./internal/kube/ -run TestCRDSchema_GoldenStable
```
Then `git diff server-go/internal/kube/testdata/crd_schema/` — confirm the only changes are the added `privateEgress` property in the kusoservices and kusoenvironments schema snapshots.

- [ ] **Step 6: Run the CRD golden test clean**

Run (from `server-go/`): `go test ./internal/kube/ -run TestCRDSchema_GoldenStable`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add operator/config/crd/bases/ dist/crds.yaml server-go/internal/kube/testdata/crd_schema/
git commit -m "feat(crd): add privateEgress to KusoService and KusoEnvironment schemas"
```

---

## Task 8: CLI — `--private-egress` flag on `service set`

**Files:**
- Modify: `cli/pkg/kusoApi/projects.go`
- Modify: `cli/cmd/kusoCli/project.go`

NOTE: the spec said `kuso project service add --private-egress`, but `service add` exposes no field flags — kuso's convention is to configure service fields via `kuso project service set` (the command that already has `--internal=on|off`). This task implements `--private-egress=on|off` on `service set`, matching `--internal`, which fulfills the spec's intent ("the CLI exposes the field").

- [ ] **Step 1: Add `PrivateEgress` to the API request struct**

In `cli/pkg/kusoApi/projects.go`, find the `PatchService` request struct containing `Internal *bool ...json:"internal,omitempty"...`. Immediately AFTER that `Internal` line, add:

```go
	PrivateEgress *bool `json:"privateEgress,omitempty"`
```

- [ ] **Step 2: Add the flag variable**

In `cli/cmd/kusoCli/project.go`, find the `var (...)` block that declares `serviceSetInternal string` (alongside `serviceSetRuntime`, `serviceSetDomains`). Add, right after `serviceSetInternal`:

```go
	serviceSetPrivateEgress string // "on" | "off" | "" (leave alone)
```

- [ ] **Step 3: Parse the flag in the `service set` RunE**

In the same file, in `serviceSetCmd`'s `RunE`, find the `if cmd.Flags().Changed("internal") {` block (the switch on `serviceSetInternal` setting `req.Internal`). Immediately AFTER that block's closing `}`, add the parallel block:

```go
		if cmd.Flags().Changed("private-egress") {
			switch serviceSetPrivateEgress {
			case "on", "true", "yes":
				req.PrivateEgress = kusoApi.BoolPtr(true)
			case "off", "false", "no":
				req.PrivateEgress = kusoApi.BoolPtr(false)
			default:
				return fmt.Errorf("--private-egress must be on|off (got %q)", serviceSetPrivateEgress)
			}
		}
```

- [ ] **Step 4: Register the flag**

In the same file, find where `serviceSetCmd` flags are registered — the line `serviceSetCmd.Flags().StringVar(&serviceSetInternal, "internal", ...)`. Immediately AFTER it, add:

```go
	serviceSetCmd.Flags().StringVar(&serviceSetPrivateEgress, "private-egress", "", "deny public internet egress (on|off)")
```

Then find the parallel registration on the top-level alias `serviceSetTopCmd` (it re-registers `--runtime`, `--domains` — search for `serviceSetTopCmd.Flags().StringVar(&serviceSetRuntime`). After the `serviceSetTopCmd` block registers `--domains` (and `--internal` if present there), add the matching line. Run `grep -n "serviceSetTopCmd.Flags()" cli/cmd/kusoCli/project.go` first to see exactly which flags the alias registers, and add `--private-egress` to the alias if and only if the alias also registers `--internal` (keep the alias and the original in parity).

- [ ] **Step 5: Update the `service set` help text**

In `serviceSetCmd`'s `Long`/help string, find the line documenting `--internal=on` / `--internal=off`. Add two parallel lines:

```
  --private-egress=on           # deny public internet egress
  --private-egress=off          # allow public internet egress
```

- [ ] **Step 6: Build the CLI**

Run (from `cli/`): `go build ./...`
Expected: succeeds.

- [ ] **Step 7: Smoke-check the flag is registered**

Run (from `cli/`): `go run ./cmd/kusoCli project service set --help 2>&1 | grep private-egress`
Expected: the `--private-egress` flag line appears.

- [ ] **Step 8: Commit**

```bash
git add cli/pkg/kusoApi/projects.go cli/cmd/kusoCli/project.go
git commit -m "feat(cli): add --private-egress flag to service set"
```

---

## Task 9: Full build + test sweep

**Files:** none (verification only)

- [ ] **Step 1: Build the whole workspace**

Run (from `server-go/`): `go build ./...`
Run (from `cli/`): `go build ./...`
Expected: both succeed.

- [ ] **Step 2: Run the affected test packages**

Run (from `server-go/`):
```bash
go test ./internal/kube/ ./internal/projects/
```
Expected: PASS. These cover the CRD golden, chart render, and propagation tests.

- [ ] **Step 3: Run the full server-go test suite to catch regressions**

Run (from `server-go/`): `go test ./...`
Expected: PASS. If a pre-existing unrelated test fails, note it but do not fix it (out of scope); if a test fails because of this change, fix it.

- [ ] **Step 4: Verify CRD source and bundle agree**

Run (from repo root):
```bash
grep -c "privateEgress:" operator/config/crd/bases/application.kuso.sislelabs.com_kusoservices.yaml \
  operator/config/crd/bases/application.kuso.sislelabs.com_kusoenvironments.yaml \
  dist/crds.yaml
```
Expected: `kusoservices` → 1, `kusoenvironments` → 1, `dist/crds.yaml` → 2.

- [ ] **Step 5: Final commit (only if cleanup was needed)**

```bash
git add -A
git commit -m "chore: public-egress fix verification cleanup"
```
If nothing changed, skip.

---

## Rollout (post-merge — performed manually, not part of subagent execution)

After this plan is merged and a kuso release is cut:
1. Deploy the new operator + server + CRDs to the cluster (`kubectl apply` the updated `dist/crds.yaml`, roll the operator and kuso-server).
2. The `distill` project reconciles; the `kusoenvironment` chart now stamps `network-egress-public=true` on distill's runtime pods.
3. Revert the manual stopgap patch so the live policy matches a clean chart render:
   ```bash
   ssh -i ~/.ssh/keys/hetzner root@kuso.sislelabs.com \
     'kubectl patch networkpolicy distill-allow-public-egress -n kuso --type=json \
        -p "[{\"op\":\"replace\",\"path\":\"/spec/podSelector\",\"value\":{\"matchLabels\":{\"kuso.sislelabs.com/network-egress-public\":\"true\",\"kuso.sislelabs.com/project\":\"distill\"}}}]"'
   ```
4. Verify distill: Discord OAuth login succeeds, the bot connects to the gateway.

---

## Self-Review Notes

- **Spec coverage:** Spec §Changes 1-2 (CR types) → Task 1. §3 (propagation) → Tasks 2-3 (+ test in Task 4). §4 (chart) → Task 5; the spec's "server must write privateEgress into helm values" is satisfied automatically — the operator-sdk helm-operator passes the CR `.spec` straight through as Helm values, so `KusoEnvironmentSpec.PrivateEgress` with json tag `privateEgress` *is* `.Values.privateEgress` with zero mapping code (noted in the plan header). §5 (CRDs) → Task 7. §6 (CLI) → Task 8 — with the documented correction that the flag lands on `service set`, not `service add`, because `add` exposes no field flags. §Testing → Tasks 4, 6, 9. §Rollout → the post-merge section.
- **Placeholder scan:** every code step shows the exact code. Tasks 4 and 6 deliberately begin with a "find the existing test" step because the test-harness file/style must be read before mirroring it — the mirror target is concrete (the `Internal`/`Runtime` propagation test; the `kusoenvironment` render test), not a vague "add tests."
- **Type consistency:** `PrivateEgress bool` on both specs; `PrivateEgress bool` on `changedFields`; `PrivateEgress *bool` on both the server patch-request struct and the CLI `kusoApi` request struct (pointer, to distinguish "unset"); chart value + CR json tag both `privateEgress`; the label string `kuso.sislelabs.com/network-egress-public` is identical in the chart (Task 5) and matches what the unchanged `kusoproject` `allow-public-egress` policy already selects.
