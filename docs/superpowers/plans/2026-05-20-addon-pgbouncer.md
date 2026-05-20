# Opt-in PgBouncer for Addon Postgres — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add an opt-in PgBouncer connection pooler to `kind: postgres` KusoAddons, toggled per-addon via `spec.pooler.enabled`.

**Architecture:** A new `spec.pooler` block on `KusoAddon` flows through the CRD → Go types → apiv1 wire types → addon helm chart. Single-node addons render a self-managed 1-replica PgBouncer Deployment + Service + ConfigMap (mirroring `deploy/postgres.yaml`'s instance pooler); HA (CNPG) addons render a CNPG-native `Pooler` CRD. The addon's `<name>-conn` Secret gains additive `POOLER_HOST/POOLER_PORT/POOLER_URL` keys when the pooler is on; `DATABASE_URL` stays direct so existing consumers are untouched.

**Tech Stack:** Go (server-go, apiv1), Helm (operator/helm-charts/kusoaddon), CloudNativePG `Pooler` CRD, Next.js/React (web), edoburu/pgbouncer image.

---

## Background for the implementer

- The kuso operator is a **helm-operator**: the `KusoAddon` CR's `spec` is passed verbatim as the helm chart's `.Values`. So adding `spec.pooler.enabled` to the CRD makes `.Values.pooler.enabled` available in the chart with **no Go glue for rendering** — the only Go work is type definitions + the PATCH/POST API surface so the UI can set the field.
- The addon `<name>-conn` Secret is read dynamically by the env-var rewriter (`server-go/internal/addons/secret_keys.go` enumerates `sec.Data` keys with no allowlist). New `POOLER_*` keys therefore work in `${{ <addon>.POOLER_URL }}` automatically — **no rewriter change needed**.
- The instance pooler in `deploy/postgres.yaml` (lines 271–495) is the reference implementation for the single-node PgBouncer Deployment: ConfigMap `pgbouncer.ini`, md5 `render-userlist` init container, `edoburu/pgbouncer:v1.25.1-p0`, tcp `:6432` probes.
- Helm chart conventions live in `operator/helm-charts/kusoaddon/templates/_helpers.tpl`: `kusoaddon.fullname` (= `.Release.Name`), `kusoaddon.connSecretName` (= `<release>-conn`), `kusoaddon.labels`, `kusoaddon.selectorLabels`, `kusoaddon.placement`.
- The single-node Postgres template is `operator/helm-charts/kusoaddon/templates/postgres.yaml`; HA is `postgres-ha.yaml`. Both emit the `<name>-conn` Secret.

## File structure

**Create:**
- `operator/helm-charts/kusoaddon/templates/postgres-pooler.yaml` — single-node PgBouncer Deployment + Service + ConfigMap.

**Modify:**
- `operator/config/crd/bases/application.kuso.sislelabs.com_kusoaddons.yaml` — add `spec.pooler.enabled`.
- `operator/helm-charts/kusoaddon/values.yaml` — add `pooler.enabled: false`.
- `operator/helm-charts/kusoaddon/templates/postgres.yaml` — add `POOLER_*` keys to the conn Secret when `pooler.enabled`.
- `operator/helm-charts/kusoaddon/templates/postgres-ha.yaml` — add CNPG `Pooler` + `POOLER_*` conn-Secret keys when `pooler.enabled`.
- `server-go/internal/kube/types.go` — add `KusoAddonPooler` type + `Pooler` field on `KusoAddonSpec`.
- `api/apiv1/addons.go` — add `Pooler` to `CreateAddonRequest` + `UpdateAddonRequest`.
- `server-go/internal/addons/addons.go` — add `Pooler` to domain `CreateAddonRequest` + `UpdateAddonRequest`; apply it in `Add` and `Update`.
- `server-go/internal/http/handlers/addons.go` — map `Pooler` in `apiv1CreateAddonToDomain` + `apiv1UpdateAddonToDomain`.
- `web/src/types/projects.ts` — add `pooler` to `KusoAddonSpec`.
- `web/src/features/projects/api.ts` — add `pooler` to `UpdateAddonBody`.
- `web/src/components/addon/overlay/SettingsTab.tsx` — add the pooler toggle to `ConfigurationSection`.

---

## Task 1: CRD — add `spec.pooler`

**Files:**
- Modify: `operator/config/crd/bases/application.kuso.sislelabs.com_kusoaddons.yaml`

- [ ] **Step 1: Add the `pooler` property to the addon spec schema**

In `operator/config/crd/bases/application.kuso.sislelabs.com_kusoaddons.yaml`, find the `ha:` property under `spec.properties` (around line 78):

```yaml
                ha:
                  type: boolean
                  default: false
```

Immediately after the `ha:` block (before `storageSize:`), insert:

```yaml
                pooler:
                  type: object
                  description: >-
                    Opt-in PgBouncer connection pooler in front of a
                    kind=postgres addon. Reach it via the
                    POOLER_HOST/POOLER_PORT/POOLER_URL keys in the addon's
                    <name>-conn Secret; DATABASE_URL stays direct. Ignored
                    for non-postgres kinds.
                  properties:
                    enabled:
                      type: boolean
                      default: false
```

- [ ] **Step 2: Verify the YAML parses**

Run: `python3 -c "import yaml,sys; yaml.safe_load(open('operator/config/crd/bases/application.kuso.sislelabs.com_kusoaddons.yaml'))" && echo OK`
Expected: `OK`

- [ ] **Step 3: Commit**

```bash
git add operator/config/crd/bases/application.kuso.sislelabs.com_kusoaddons.yaml
git commit -m "feat(crd): add spec.pooler.enabled to KusoAddon"
```

---

## Task 2: Helm — values default

**Files:**
- Modify: `operator/helm-charts/kusoaddon/values.yaml`

- [ ] **Step 1: Add the `pooler` default**

In `operator/helm-charts/kusoaddon/values.yaml`, after the `useInstanceAddon: ""` block at the end of the file, append:

```yaml

# pooler: opt-in PgBouncer in front of a kind=postgres addon. When
# enabled, single-node addons get a self-managed 1-replica PgBouncer
# Deployment; HA (CNPG) addons get a CNPG-native Pooler CRD. Reach it
# via the POOLER_HOST/POOLER_PORT/POOLER_URL keys in <name>-conn;
# DATABASE_URL stays direct. Ignored for non-postgres kinds.
pooler:
  enabled: false
```

- [ ] **Step 2: Commit**

```bash
git add operator/helm-charts/kusoaddon/values.yaml
git commit -m "feat(chart): add pooler.enabled default to kusoaddon values"
```

---

## Task 3: Helm — single-node PgBouncer template

**Files:**
- Create: `operator/helm-charts/kusoaddon/templates/postgres-pooler.yaml`

- [ ] **Step 1: Create the single-node pooler template**

Create `operator/helm-charts/kusoaddon/templates/postgres-pooler.yaml` with exactly this content:

```yaml
{{- /*
Opt-in single-node PgBouncer for a kind=postgres addon.

Rendered when:
  - kind = postgres
  - pooler.enabled = true
  - ha = false  (the HA path uses a CNPG-native Pooler CRD, in
    postgres-ha.yaml)
  - external / useInstanceAddon both unset (those bypass provisioning)

This mirrors the instance-control-plane pooler in deploy/postgres.yaml:
a PgBouncer Deployment in transaction-pool mode, fronting the addon
StatefulSet's Service. One replica — this is a single-node addon, so
a second pooler replica adds no availability (the DB itself is the
SPOF). A pooler pod restart briefly drops client connections; that's
an accepted cost of this opt-in convenience.

The userlist is rendered at pod start from the <name>-conn Secret's
POSTGRES_USER / POSTGRES_PASSWORD by an init container (PgBouncer's
md5 userlist format is "user" "md5<hash>", hash = md5(password+user)).
Password rotation (kuso project addon repair-password) needs a pooler
pod restart to re-render — rare, operator-driven, acceptable.
*/}}
{{- if and (eq .Values.kind "postgres") .Values.pooler .Values.pooler.enabled (not .Values.ha) (not .Values.external) (not .Values.useInstanceAddon) -}}
{{- $name := include "kusoaddon.fullname" . -}}
{{- $poolerName := printf "%s-pooler" $name -}}
{{- $connSecret := include "kusoaddon.connSecretName" . -}}
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: {{ $poolerName }}-config
  labels:
    {{- include "kusoaddon.labels" . | nindent 4 }}
data:
  pgbouncer.ini: |
    [databases]
    ; * matches whatever dbname the client connects with, routed at
    ; the addon's Postgres StatefulSet Service on :5432.
    * = host={{ $name }} port=5432

    [pgbouncer]
    listen_addr = 0.0.0.0
    listen_port = 6432
    auth_type = md5
    auth_file = /etc/pgbouncer/userlist.txt
    pool_mode = transaction
    max_client_conn = 200
    default_pool_size = 25
    reserve_pool_size = 5
    reserve_pool_timeout = 3
    query_wait_timeout = 30
    server_idle_timeout = 600
    log_connections = 0
    log_disconnections = 0
    log_pooler_errors = 1
    ignore_startup_parameters = extra_float_digits,search_path
    admin_users = kuso
---
apiVersion: v1
kind: Service
metadata:
  name: {{ $poolerName }}
  labels:
    {{- include "kusoaddon.labels" . | nindent 4 }}
spec:
  selector:
    {{- include "kusoaddon.selectorLabels" . | nindent 4 }}
    kuso.sislelabs.com/component: pooler
  ports:
    - name: pgbouncer
      port: 6432
      targetPort: 6432
  type: ClusterIP
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: {{ $poolerName }}
  labels:
    {{- include "kusoaddon.labels" . | nindent 4 }}
spec:
  # 1 replica — single-node addon, the DB is the SPOF anyway.
  replicas: 1
  strategy:
    type: RollingUpdate
    rollingUpdate:
      maxSurge: 1
      maxUnavailable: 0
  selector:
    matchLabels:
      {{- include "kusoaddon.selectorLabels" . | nindent 6 }}
      kuso.sislelabs.com/component: pooler
  template:
    metadata:
      labels:
        {{- include "kusoaddon.labels" . | nindent 8 }}
        kuso.sislelabs.com/component: pooler
    spec:
      {{- include "kusoaddon.placement" . | nindent 6 }}
      # Init container hashes POSTGRES_USER/POSTGRES_PASSWORD from the
      # <name>-conn Secret into PgBouncer's md5 userlist format.
      initContainers:
        - name: render-userlist
          image: alpine/k8s:1.30.4
          command: [/bin/sh, -ec]
          args:
            - |
              set -e
              user="$(cat /etc/pg-conn/POSTGRES_USER)"
              pass="$(cat /etc/pg-conn/POSTGRES_PASSWORD)"
              if [ -z "$user" ] || [ -z "$pass" ]; then
                echo "[render-userlist] FATAL: POSTGRES_USER/POSTGRES_PASSWORD missing from conn secret" >&2
                exit 1
              fi
              hash="$(printf '%s%s' "$pass" "$user" | md5sum | awk '{print $1}')"
              printf '"%s" "md5%s"\n' "$user" "$hash" > /shared/userlist.txt
              chmod 0444 /shared/userlist.txt
          volumeMounts:
            - { name: pg-conn, mountPath: /etc/pg-conn, readOnly: true }
            - { name: shared, mountPath: /shared }
      containers:
        - name: pgbouncer
          image: edoburu/pgbouncer:v1.25.1-p0
          imagePullPolicy: IfNotPresent
          ports:
            - { name: pgbouncer, containerPort: 6432 }
          command: ["/usr/bin/pgbouncer"]
          args: ["/etc/pgbouncer/pgbouncer.ini"]
          resources:
            requests: { cpu: 50m, memory: 64Mi }
            limits:   { cpu: 500m, memory: 256Mi }
          readinessProbe:
            tcpSocket: { port: 6432 }
            initialDelaySeconds: 2
            periodSeconds: 5
          livenessProbe:
            tcpSocket: { port: 6432 }
            initialDelaySeconds: 10
            periodSeconds: 15
          volumeMounts:
            - { name: config, mountPath: /etc/pgbouncer/pgbouncer.ini, subPath: pgbouncer.ini, readOnly: true }
            - { name: shared, mountPath: /etc/pgbouncer/userlist.txt, subPath: userlist.txt, readOnly: true }
      volumes:
        - name: config
          configMap: { name: {{ $poolerName }}-config }
        - name: pg-conn
          secret:
            secretName: {{ $connSecret }}
            items:
              - { key: POSTGRES_USER, path: POSTGRES_USER }
              - { key: POSTGRES_PASSWORD, path: POSTGRES_PASSWORD }
        - name: shared
          emptyDir: {}
{{- end }}
```

- [ ] **Step 2: Render the chart with the pooler on, single-node**

Run:
```bash
helm template t operator/helm-charts/kusoaddon \
  --set project=demo --set kind=postgres --set pooler.enabled=true \
  --show-only templates/postgres-pooler.yaml
```
Expected: a ConfigMap `t-pooler-config`, a Service `t-pooler`, and a Deployment `t-pooler` (replicas: 1) render with no template errors.

- [ ] **Step 3: Render with the pooler off — confirm nothing renders**

Run:
```bash
helm template t operator/helm-charts/kusoaddon \
  --set project=demo --set kind=postgres \
  --show-only templates/postgres-pooler.yaml
```
Expected: error `could not find template ... postgres-pooler.yaml in chart` OR empty output — i.e. the template produced nothing because the `if` guard is false.

- [ ] **Step 4: Render with kind=redis — confirm nothing renders**

Run:
```bash
helm template t operator/helm-charts/kusoaddon \
  --set project=demo --set kind=redis --set pooler.enabled=true \
  --show-only templates/postgres-pooler.yaml
```
Expected: empty / not-found — the `eq .Values.kind "postgres"` guard suppresses it.

- [ ] **Step 5: Commit**

```bash
git add operator/helm-charts/kusoaddon/templates/postgres-pooler.yaml
git commit -m "feat(chart): single-node PgBouncer template for postgres addon"
```

---

## Task 4: Helm — conn Secret POOLER_* keys (single-node)

**Files:**
- Modify: `operator/helm-charts/kusoaddon/templates/postgres.yaml`

- [ ] **Step 1: Add POOLER_* keys to the single-node conn Secret**

In `operator/helm-charts/kusoaddon/templates/postgres.yaml`, find the conn Secret's `stringData` — the `DATABASE_URL` line (around line 173):

```yaml
  DATABASE_URL: "postgres://kuso:{{ $password | urlquery }}@{{ $name }}:5432/{{ $database }}?sslmode=disable"
```

Immediately after that line, insert:

```yaml
  {{- /* Opt-in pooler keys. Additive — DATABASE_URL above stays
         direct at the StatefulSet so existing consumers are
         unaffected. Apps opt into pooling with ${{ <addon>.POOLER_URL }}.
         Rendered only when pooler.enabled; absent otherwise so
         disabling the pooler cleanly drops the keys. */}}
  {{- if and .Values.pooler .Values.pooler.enabled }}
  POOLER_HOST: {{ printf "%s-pooler" $name | quote }}
  POOLER_PORT: "6432"
  POOLER_URL: "postgres://kuso:{{ $password | urlquery }}@{{ $name }}-pooler:6432/{{ $database }}?sslmode=disable"
  {{- end }}
```

- [ ] **Step 2: Render — confirm POOLER_* present when enabled**

Run:
```bash
helm template t operator/helm-charts/kusoaddon \
  --set project=demo --set kind=postgres --set pooler.enabled=true \
  --show-only templates/postgres.yaml | grep -E 'POOLER_|DATABASE_URL'
```
Expected: `POOLER_HOST`, `POOLER_PORT`, `POOLER_URL` all present; `POOLER_HOST` value is `t-pooler`; `DATABASE_URL` still points at `@t:5432`.

- [ ] **Step 3: Render — confirm POOLER_* absent when disabled**

Run:
```bash
helm template t operator/helm-charts/kusoaddon \
  --set project=demo --set kind=postgres \
  --show-only templates/postgres.yaml | grep -E 'POOLER_' || echo "NONE"
```
Expected: `NONE`.

- [ ] **Step 4: Commit**

```bash
git add operator/helm-charts/kusoaddon/templates/postgres.yaml
git commit -m "feat(chart): add POOLER_* conn-secret keys for single-node postgres addon"
```

---

## Task 5: Helm — CNPG Pooler + conn keys (HA)

**Files:**
- Modify: `operator/helm-charts/kusoaddon/templates/postgres-ha.yaml`

- [ ] **Step 1: Inspect the HA conn Secret block**

Run: `grep -n 'POOLER\|conn\|stringData\|DATABASE_URL\|^---\|kind: Cluster\|kind: Secret' operator/helm-charts/kusoaddon/templates/postgres-ha.yaml`

Note the line where the `<name>-conn` Secret's `stringData` (or `data`) ends, and the final `{{- end }}` of the `if` guard. You will append the CNPG `Pooler` before that final `{{- end }}`, and add `POOLER_*` keys into the conn Secret's data block.

- [ ] **Step 2: Add POOLER_* keys to the HA conn Secret**

In the HA conn Secret, locate the line that writes `DATABASE_URL` (the HA chart composes it pointing at `<name>-rw`). Immediately after that line, insert — matching the surrounding indentation and whether the block uses `stringData` or `data` (if `data`, the values must be `b64enc`; if `stringData`, use them raw as shown):

For a `stringData` block:
```yaml
  {{- if and .Values.pooler .Values.pooler.enabled }}
  POOLER_HOST: {{ printf "%s-pooler" $name | quote }}
  POOLER_PORT: "6432"
  POOLER_URL: "postgres://kuso:{{ $password | urlquery }}@{{ $name }}-pooler:6432/{{ $database }}?sslmode=disable"
  {{- end }}
```

If the block uses `data:` (b64-encoded), instead insert:
```yaml
  {{- if and .Values.pooler .Values.pooler.enabled }}
  POOLER_HOST: {{ printf "%s-pooler" $name | b64enc | quote }}
  POOLER_PORT: {{ "6432" | b64enc | quote }}
  POOLER_URL: {{ printf "postgres://kuso:%s@%s-pooler:6432/%s?sslmode=disable" ($password | urlquery) $name $database | b64enc | quote }}
  {{- end }}
```

Use the variable names already in scope in `postgres-ha.yaml` for password / database (the file's header documents `$database` and a `$password`-equivalent; if the password variable has a different name, use that name).

- [ ] **Step 3: Add the CNPG Pooler resource**

Just before the final `{{- end }}` that closes the `if and (eq .Values.kind "postgres") .Values.ha ...` guard at the bottom of `postgres-ha.yaml`, insert:

```yaml
{{- /* Opt-in CNPG-native PgBouncer. CNPG manages the Pooler's own
       Deployment + a <name>-pooler Service and follows primary
       failover natively, so HA addons get pooling without a
       self-rendered Deployment. type: rw fronts the primary. */}}
{{- if and .Values.pooler .Values.pooler.enabled }}
---
apiVersion: postgresql.cnpg.io/v1
kind: Pooler
metadata:
  name: {{ $name }}-pooler
  labels:
    {{- include "kusoaddon.labels" . | nindent 4 }}
spec:
  cluster:
    name: {{ $name }}
  instances: 1
  type: rw
  pgbouncer:
    poolMode: transaction
{{- end }}
```

- [ ] **Step 4: Render — HA with pooler on**

Run:
```bash
helm template t operator/helm-charts/kusoaddon \
  --set project=demo --set kind=postgres --set ha=true --set pooler.enabled=true \
  --show-only templates/postgres-ha.yaml | grep -E 'kind: Pooler|POOLER_|poolMode'
```
Expected: `kind: Pooler` present, `poolMode: transaction` present, `POOLER_HOST/PORT/URL` present.

- [ ] **Step 5: Render — HA with pooler off**

Run:
```bash
helm template t operator/helm-charts/kusoaddon \
  --set project=demo --set kind=postgres --set ha=true \
  --show-only templates/postgres-ha.yaml | grep -E 'kind: Pooler|POOLER_' || echo "NONE"
```
Expected: `NONE`.

- [ ] **Step 6: Render — confirm single-node pooler template does NOT also fire in HA**

Run:
```bash
helm template t operator/helm-charts/kusoaddon \
  --set project=demo --set kind=postgres --set ha=true --set pooler.enabled=true \
  --show-only templates/postgres-pooler.yaml || echo "NONE (correct)"
```
Expected: `NONE (correct)` — the single-node template's `(not .Values.ha)` guard suppresses it, so only the CNPG Pooler renders in HA mode.

- [ ] **Step 7: Commit**

```bash
git add operator/helm-charts/kusoaddon/templates/postgres-ha.yaml
git commit -m "feat(chart): CNPG Pooler + POOLER_* conn keys for HA postgres addon"
```

---

## Task 6: Go — `KusoAddonPooler` type

**Files:**
- Modify: `server-go/internal/kube/types.go`

- [ ] **Step 1: Add the `Pooler` field and type**

In `server-go/internal/kube/types.go`, in `KusoAddonSpec` (starts line 325), add the field after `UseInstanceAddon` (line 365):

```go
	// Pooler enables an opt-in PgBouncer connection pooler in front
	// of a kind=postgres addon. Nil or {Enabled:false} = no pooler.
	// Single-node addons get a self-managed PgBouncer Deployment; HA
	// addons get a CNPG-native Pooler. Consumers reach it via the
	// additive POOLER_HOST/POOLER_PORT/POOLER_URL keys in <name>-conn;
	// DATABASE_URL stays direct. See operator/helm-charts/kusoaddon
	// templates postgres-pooler.yaml and postgres-ha.yaml.
	Pooler *KusoAddonPooler `json:"pooler,omitempty"`
```

Then add the type definition immediately after the `KusoAddonSpec` struct's closing brace:

```go
// KusoAddonPooler is the opt-in connection-pooler block on
// KusoAddonSpec. Only meaningful for kind=postgres.
type KusoAddonPooler struct {
	Enabled bool `json:"enabled,omitempty"`
}
```

- [ ] **Step 2: Verify it compiles**

Run: `cd server-go && go build ./internal/kube/`
Expected: no output (success).

- [ ] **Step 3: Commit**

```bash
git add server-go/internal/kube/types.go
git commit -m "feat: add KusoAddonPooler to KusoAddonSpec"
```

---

## Task 7: Go — apiv1 wire types

**Files:**
- Modify: `api/apiv1/addons.go`

- [ ] **Step 1: Add the wire pooler type and fields**

In `api/apiv1/addons.go`, add `Pooler` to `CreateAddonRequest` (after `UseInstanceAddon`, line 27):

```go
	// Pooler enables an opt-in PgBouncer connection pooler in front
	// of a kind=postgres addon. Nil = no pooler.
	Pooler *AddonPoolerSpec `json:"pooler,omitempty"`
```

Add `Pooler` to `UpdateAddonRequest` (after `Backup`, line 47):

```go
	// Pooler toggles the opt-in PgBouncer pooler. Nil = leave alone.
	Pooler *AddonPoolerSpec `json:"pooler,omitempty"`
```

Add the type at the end of the file:

```go
// AddonPoolerSpec is the opt-in connection-pooler block. Only
// meaningful for kind=postgres.
type AddonPoolerSpec struct {
	Enabled bool `json:"enabled"`
}
```

- [ ] **Step 2: Verify it compiles**

Run: `cd api/apiv1 && go build ./...`
Expected: no output (success).

- [ ] **Step 3: Commit**

```bash
git add api/apiv1/addons.go
git commit -m "feat(apiv1): add pooler field to addon create/update requests"
```

---

## Task 8: Go — domain request types + apply logic

**Files:**
- Modify: `server-go/internal/addons/addons.go`
- Test: `server-go/internal/addons/addons_test.go`

- [ ] **Step 1: Write the failing test for Update applying Pooler**

In `server-go/internal/addons/addons_test.go`, add a test. First inspect the file for an existing `Update` test to copy the harness setup (`grep -n "func Test.*Update" server-go/internal/addons/addons_test.go`). Mirror that test's fake-kube setup, then assert pooler toggling:

```go
func TestUpdate_TogglesPooler(t *testing.T) {
	// Copy the Add+Update fixture setup from the existing Update test
	// in this file (same Service, fake Kube, project, addon name).
	// After creating a postgres addon, call Update with Pooler set:
	enabled := true
	got, err := svc.Update(ctx, project, addonName, UpdateAddonRequest{
		Pooler: &AddonPoolerPatch{Enabled: &enabled},
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if got.Spec.Pooler == nil || !got.Spec.Pooler.Enabled {
		t.Fatalf("expected spec.pooler.enabled=true, got %+v", got.Spec.Pooler)
	}

	// Toggling it back off must persist enabled=false.
	disabled := false
	got, err = svc.Update(ctx, project, addonName, UpdateAddonRequest{
		Pooler: &AddonPoolerPatch{Enabled: &disabled},
	})
	if err != nil {
		t.Fatalf("Update off: %v", err)
	}
	if got.Spec.Pooler == nil || got.Spec.Pooler.Enabled {
		t.Fatalf("expected spec.pooler.enabled=false, got %+v", got.Spec.Pooler)
	}
}
```

- [ ] **Step 2: Run the test — verify it fails**

Run: `cd server-go && go test ./internal/addons/ -run TestUpdate_TogglesPooler`
Expected: FAIL — `AddonPoolerPatch` undefined / `UpdateAddonRequest` has no `Pooler` field.

- [ ] **Step 3: Add the domain types**

In `server-go/internal/addons/addons.go`:

Add `Pooler` to `CreateAddonRequest` (after `UseInstanceAddon`, in the struct around line 60–82):

```go
	// Pooler enables the opt-in PgBouncer pooler at create time.
	// Nil = no pooler.
	Pooler *kube.KusoAddonPooler `json:"pooler,omitempty"`
```

Add `Pooler` to `UpdateAddonRequest` (after `Backup`, line 277):

```go
	// Pooler toggles the opt-in PgBouncer pooler. Nil = leave alone.
	Pooler *AddonPoolerPatch `json:"pooler,omitempty"`
```

Add the patch type after `UpdateBackupPatch` (after line 287):

```go
// AddonPoolerPatch toggles the opt-in PgBouncer pooler. Enabled is a
// pointer so a nil AddonPoolerPatch and a {Enabled: nil} are both
// "leave alone"; callers send {Enabled: &true/&false} to set it.
type AddonPoolerPatch struct {
	Enabled *bool `json:"enabled,omitempty"`
}
```

- [ ] **Step 4: Apply Pooler in `Update`**

In `server-go/internal/addons/addons.go`, inside the `UpdateKusoAddonWithRetry` closure in `Update` (after the `req.Backup` block, before the closing `return nil`, around line 363):

```go
		if req.Pooler != nil && req.Pooler.Enabled != nil {
			// Lazy-init so toggling the pooler doesn't disturb other
			// spec fields. The chart treats a nil pooler block and
			// {enabled:false} identically (no pooler rendered).
			if addon.Spec.Pooler == nil {
				addon.Spec.Pooler = &kube.KusoAddonPooler{}
			}
			addon.Spec.Pooler.Enabled = *req.Pooler.Enabled
		}
```

- [ ] **Step 5: Apply Pooler in `Add`**

Find where `Add` builds the `KusoAddon` from `CreateAddonRequest` (`grep -n "func (s \*Service) Add" server-go/internal/addons/addons.go`, then read the spec assembly). Where the other spec fields (`Version`, `Size`, `HA`, etc.) are copied onto the new CR's `Spec`, add:

```go
		Pooler: req.Pooler,
```

(If `Add` assigns the spec field-by-field rather than as a struct literal, instead add `addon.Spec.Pooler = req.Pooler` alongside the sibling assignments.)

- [ ] **Step 6: Run the test — verify it passes**

Run: `cd server-go && go test ./internal/addons/ -run TestUpdate_TogglesPooler`
Expected: PASS.

- [ ] **Step 7: Run the full addons package tests**

Run: `cd server-go && go test ./internal/addons/`
Expected: PASS (no regressions).

- [ ] **Step 8: Commit**

```bash
git add server-go/internal/addons/addons.go server-go/internal/addons/addons_test.go
git commit -m "feat(addons): apply spec.pooler in Add and Update"
```

---

## Task 9: Go — HTTP handler wiring

**Files:**
- Modify: `server-go/internal/http/handlers/addons.go`

- [ ] **Step 1: Map Pooler in the create converter**

In `server-go/internal/http/handlers/addons.go`, in `apiv1CreateAddonToDomain` (line 28), after the `if in.External != nil { ... }` block and before `return out`:

```go
	if in.Pooler != nil {
		out.Pooler = &kube.KusoAddonPooler{Enabled: in.Pooler.Enabled}
	}
```

- [ ] **Step 2: Map Pooler in the update converter**

In `apiv1UpdateAddonToDomain` (line 50), after the `if in.Backup != nil { ... }` block and before `return out`:

```go
	if in.Pooler != nil {
		out.Pooler = &addons.AddonPoolerPatch{Enabled: &in.Pooler.Enabled}
	}
```

- [ ] **Step 3: Verify the server builds**

Run: `cd server-go && go build ./...`
Expected: no output (success).

- [ ] **Step 4: Run handler tests**

Run: `cd server-go && go test ./internal/http/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add server-go/internal/http/handlers/addons.go
git commit -m "feat(http): wire pooler field through addon create/update handlers"
```

---

## Task 10: Web — types

**Files:**
- Modify: `web/src/types/projects.ts`
- Modify: `web/src/features/projects/api.ts`

- [ ] **Step 1: Add `pooler` to `KusoAddonSpec`**

In `web/src/types/projects.ts`, in `interface KusoAddonSpec` (line 117), after the `backup` field block (after line 132):

```typescript
  // pooler: opt-in PgBouncer in front of a kind=postgres addon.
  // When enabled the addon's <name>-conn Secret gains
  // POOLER_HOST/POOLER_PORT/POOLER_URL keys; DATABASE_URL stays
  // direct. Ignored for non-postgres kinds.
  pooler?: {
    enabled?: boolean;
  };
```

- [ ] **Step 2: Add `pooler` to `UpdateAddonBody`**

In `web/src/features/projects/api.ts`, in `interface UpdateAddonBody` (line 172), after the `backup` field block (after line 184):

```typescript
  // pooler.enabled toggles the opt-in PgBouncer pooler (postgres
  // addons only). Omit to leave the current setting unchanged.
  pooler?: {
    enabled: boolean;
  };
```

- [ ] **Step 3: Update the `updateAddon` doc comment**

In `web/src/features/projects/api.ts`, the comment above `UpdateAddonBody` (lines 169–171) reads `spec.{version,size,ha,storageSize,database,backup}`. Change it to `spec.{version,size,ha,storageSize,database,backup,pooler}`.

- [ ] **Step 4: Verify types compile**

Run: `cd web && npx tsc --noEmit`
Expected: no errors.

- [ ] **Step 5: Commit**

```bash
git add web/src/types/projects.ts web/src/features/projects/api.ts
git commit -m "feat(web): add pooler to KusoAddonSpec and UpdateAddonBody types"
```

---

## Task 11: Web — pooler toggle in SettingsTab

**Files:**
- Modify: `web/src/components/addon/overlay/SettingsTab.tsx`

- [ ] **Step 1: Add pooler state to `ConfigurationSection`**

In `web/src/components/addon/overlay/SettingsTab.tsx`, in `ConfigurationSection`:

In the `initial` object (after `database: cr?.spec.database ?? ""`):
```typescript
    pooler: !!cr?.spec.pooler?.enabled,
```

Add the state hook after `const [database, setDatabase] = useState(initial.database);`:
```typescript
  const [pooler, setPooler] = useState(initial.pooler);
```

In the re-baseline `useEffect`, add `setPooler(initial.pooler);` alongside the other setters, and add `initial.pooler` to the dependency array.

In the `dirty` expression, add a clause:
```typescript
    pooler !== initial.pooler ||
```

In the `save` mutation's `body` assembly, after the `database` line:
```typescript
      if (pooler !== initial.pooler) body.pooler = { enabled: pooler };
```

In the `reset` function and in the footer Reset button's `onClick`, add `setPooler(initial.pooler);`.

- [ ] **Step 2: Render the pooler toggle**

In `ConfigurationSection`'s JSX, immediately after the "High availability" `<label>` block (the one with `checked={ha}`), and only when the addon is postgres, add:

```tsx
        {(cr?.spec.kind ?? "").toLowerCase() === "postgres" && (
          <label className="flex cursor-pointer items-center gap-2 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-primary)] px-3 py-2">
            <input
              type="checkbox"
              checked={pooler}
              onChange={(e) => setPooler(e.target.checked)}
              className="h-3.5 w-3.5 accent-[var(--accent)]"
            />
            <span className="text-[12px] font-medium">Connection pooling (PgBouncer)</span>
            <span className="ml-auto font-mono text-[10px] text-[var(--text-tertiary)]">
              point apps at ${"{{"} {"<addon>"}.POOLER_URL {"}}"}
            </span>
          </label>
        )}
```

- [ ] **Step 3: Verify it compiles**

Run: `cd web && npx tsc --noEmit`
Expected: no errors.

- [ ] **Step 4: Lint the changed file**

Run: `cd web && npx eslint src/components/addon/overlay/SettingsTab.tsx`
Expected: no errors. (The `react-hooks/exhaustive-deps` line for the re-baseline `useEffect` is already disabled in the file — keep it.)

- [ ] **Step 5: Commit**

```bash
git add web/src/components/addon/overlay/SettingsTab.tsx
git commit -m "feat(web): pooler toggle in addon configuration settings"
```

---

## Task 12: Full build + verification

**Files:** none (verification only)

- [ ] **Step 1: Build the server**

Run: `cd server-go && go build ./... && go vet ./internal/addons/ ./internal/http/...`
Expected: no output.

- [ ] **Step 2: Build the CLI**

Run: `cd cli && go build -o /tmp/kuso ./cmd`
Expected: no output. (The CLI surfaces `spec.pooler` automatically via the addon JSON passthrough — no CLI code change needed.)

- [ ] **Step 3: Build the web bundle**

Run: `cd web && npm run build`
Expected: build succeeds (Next.js static export).

- [ ] **Step 4: Run the full server test suite for touched packages**

Run: `cd server-go && go test ./internal/addons/ ./internal/kube/ ./internal/http/...`
Expected: PASS.

- [ ] **Step 5: Helm lint the chart**

Run: `helm lint operator/helm-charts/kusoaddon`
Expected: `1 chart(s) linted, 0 chart(s) failed`.

- [ ] **Step 6: Commit anything outstanding**

```bash
git status
# if the web build produced an embedded bundle under server-go/internal/web/dist/
git add -A
git commit -m "chore: rebuild web bundle with pooler toggle" || echo "nothing to commit"
```

---

## Task 13: Live-cluster validation (manual, via the kuso CLI)

**Files:** none — this is the e2e gate. Run against the test target in `agent-target.local.json`.

> Per CLAUDE.md, the `kuso` CLI is the contract. CRD schema changes are NOT applied by the release auto-updater — they must be `kubectl apply`-ed via ssh.

- [ ] **Step 1: Apply the updated CRD to the test cluster**

```bash
scp -i ~/.ssh/keys/hetzner \
  operator/config/crd/bases/application.kuso.sislelabs.com_kusoaddons.yaml \
  root@kuso.sislelabs.com:/tmp/kusoaddons-crd.yaml
ssh -i ~/.ssh/keys/hetzner root@kuso.sislelabs.com \
  "kubectl apply -f /tmp/kusoaddons-crd.yaml"
```
Expected: `customresourcedefinition.apiextensions.k8s.io/kusoaddons.application.kuso.sislelabs.com configured`.

- [ ] **Step 2: Ship the release so the operator + server pick up the new chart**

Run `make ship VERSION=vX.Y.Z` per the release flow in CLAUDE.md (bump `server-go/internal/version/VERSION`, `deploy/server-go.yaml` tag, `hack/install.sh` `KUSO_SERVER_VERSION`/`KUSO_VERSION` first). Wait for the test instance's updater tick. Restart the operator pod so its informer refreshes the CRD schema.

- [ ] **Step 3: Create a Postgres addon and enable the pooler**

Use the disposable project/service from `agent-target.local.json`. Create a `kind: postgres` addon, then enable the pooler (via the UI Settings tab toggle, or `kuso` if it exposes addon update).

- [ ] **Step 4: Confirm the pooler keys + pod**

```bash
dist/kuso-darwin-arm64 get addons <project> -o json
```
Expected: the addon's spec shows `pooler.enabled: true`, and its conn secret exposes `POOLER_HOST/POOLER_PORT/POOLER_URL`.

```bash
dist/kuso-darwin-arm64 shell <project> <service>
# inside the pod:
pg_isready -h <addon>-pooler -p 6432
```
Expected: `<addon>-pooler:6432 - accepting connections`.

- [ ] **Step 5: Confirm DATABASE_URL is unchanged**

Confirm via `kuso get addons <project> -o json` that `DATABASE_URL` still points at `<addon>:5432` (direct), not the pooler — proving the change is non-destructive for existing consumers.

- [ ] **Step 6: Disable the pooler and confirm teardown**

Toggle the pooler off. Re-run `kuso get addons <project> -o json` and confirm `POOLER_*` keys are gone and the `<addon>-pooler` Deployment no longer exists.

---

## Self-review notes

- **Spec coverage:** CRD (T1), values (T2), single-node template (T3), single-node conn keys (T4), HA CNPG Pooler + conn keys (T5), Go types (T6–T7), domain apply (T8), HTTP wiring (T9), web types (T10), UI toggle (T11), build/test (T12), live e2e (T13). The spec's "rewriter needs no change" claim is confirmed in the Background section (`secret_keys.go` enumerates Secret keys with no allowlist). The spec's "CLI surfaces it via passthrough" is covered by T12 step 2.
- **Type consistency:** wire type `apiv1.AddonPoolerSpec{Enabled bool}`; kube type `kube.KusoAddonPooler{Enabled bool}`; domain create uses `*kube.KusoAddonPooler`; domain update uses `*addons.AddonPoolerPatch{Enabled *bool}` (pointer, to distinguish leave-alone). The create handler maps `apiv1.AddonPoolerSpec` → `kube.KusoAddonPooler`; the update handler maps `apiv1.AddonPoolerSpec` → `addons.AddonPoolerPatch` taking the address of `Enabled`. Consistent across T7/T8/T9.
- **HA conn-Secret encoding:** T5 step 2 deliberately branches on whether `postgres-ha.yaml`'s conn Secret uses `stringData` vs `data` — the implementer must check the file (step 1) before choosing, since b64 encoding differs.
