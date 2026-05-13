# Security Audit — Post-v0.10.0
**Date:** 2026-05-13  
**Scope:** Single-tenant. Multi-tenant isolation is explicitly out of scope.  
**Auditor:** Claude Security Reviewer (claude-sonnet-4-6)

Previously-fixed issues not re-flagged: Coolify SSRF (httpx.SSRFSafeTransport), signature verification fail-open, BuildKit NetworkPolicy gate, updater admin gate, repo.path shell-injection, X-Forwarded-Host trust, notification-event INSERT+prune race, delta-ops resourceVersion lose-write, label-selector concat, Coolify error body leak, decodeJSON body cap, bell-feed prune race, schema-stale-drift readyz gate.

---

## F-01 — Misleading "fail-open" comment contradicts fail-closed implementation (P2)

**File:** `server-go/internal/auth/middleware.go:72`

The Middleware function contains the comment:
> "Fail-open on checker error so a transient DB outage doesn't 401 every user."

The actual implementation in `cmd/kuso-server/revocation.go:makeRevocationChecker` fails **closed** — on DB error with no cache entry, it returns `true` (treat as revoked) and logs "failing closed". The middleware honours this: when `i.revoked` returns `true`, the request is rejected with 401.

**Impact:** No security regression — the runtime behaviour is correct and safe. However, a future developer reading the comment could write a new `RevocationChecker` that genuinely fails open, believing that is the intended contract. The comment in `middleware.go` is the wrong source of truth and will mislead.

**Fix:** Update `middleware.go:72` to match the actual contract documented in `revocation.go:17-24`:
```go
// Revocation check after signature/expiry. Fails closed on DB outage
// once the per-jti/per-user cache window expires — see revocation.go
// for the detailed fail-closed rationale. Any new RevocationChecker
// MUST also fail closed; failing open would silently un-revoke every
// previously revoked token during a Postgres blip.
```

---

## F-02 — buildcontroller does NOT validate CR namespace is kuso-managed (P1)

**File:** `server-go/internal/buildcontroller/buildcontroller.go:145-210`

The Go build controller watches KusoBuild CRs cluster-wide via the dynamic informer. Its `reconcile` method checks that `spec.done=false`, that `spec.image.repository` and `spec.repo.url` are non-empty, but it never checks whether the CR's namespace carries the `app.kubernetes.io/managed-by=kuso` label that `EnsureNamespace` stamps on legitimate project namespaces.

**Attack scenario:** A user with kubectl access to the cluster (e.g., a compromised build-pod SA that gained minimal kubectl access, or an admin who issued a broad kubeconfig) can create a KusoBuild CR in any namespace — even `kube-system` — by bypassing the kuso API entirely. The controller will faithfully create a ServiceAccount and a Job in that namespace. The Job gets `AutomountServiceAccountToken: false` and the freshly-created SA has no bindings, but:
- The Job still runs in the target namespace, where it may inherit namespace-level permissions via admission webhooks or PSP/PSA exceptions the operator granted to that namespace.
- The namespace may not have PSS-restricted labels.
- If the target namespace is `kuso` itself (the home namespace, which explicitly does NOT have `pod-security.kubernetes.io/enforce=restricted` — see `LabelNamespaceManaged` comments), the build pod runs in a less-restricted context.

**Severity reasoning:** Exploitable only by someone who can already create CRs in arbitrary namespaces (i.e., they have RBAC for `kusobuilds.application.kuso.sislelabs.com create` cluster-wide or in the target ns). The kuso-server SA has this cluster-wide. A compromised kuso-server process (or a compromised admin session that forges a direct API call to the apiserver) is the threat model. Within the "one team per cluster" model this is a meaningful constraint because the blast radius of a kuso-server compromise is already high — but defence-in-depth means the controller should not amplify it.

**Fix:** In `buildcontroller.go:reconcile`, after the validity checks at line 171, add a namespace label check:
```go
// Guard: only reconcile builds whose namespace is kuso-managed.
// An externally-applied CR in an unmanaged namespace (kube-system,
// a tenant's raw namespace) must not trigger Job creation.
nsObj, err := s.Kube.Clientset.CoreV1().Namespaces().Get(ctx, u.GetNamespace(), metav1.GetOptions{})
if err != nil || nsObj.Labels[kube.ManagedByLabel] != kube.ManagedByValue {
    if s.Logger != nil {
        s.Logger.Warn("buildcontroller: CR in unmanaged namespace — skipping",
            "ns", u.GetNamespace(), "name", u.GetName())
    }
    return
}
```

---

## F-03 — Static build: `builderImage`, `runtimeImage`, and `outputDir` are unvalidated and flow into Dockerfile generation (P1)

**Files:** `server-go/internal/projects/services_ops.go:27-46`, `server-go/internal/buildcontroller/render.go:560-632`

The `static` runtime build path allows users with `Deployer` role to set `spec.static.builderImage`, `spec.static.runtimeImage`, `spec.static.buildCmd`, and `spec.static.outputDir`. None of these fields have server-side validation beyond type-checking.

**`runtimeImage` → Dockerfile injection.** In `renderStaticPlanContainer` (render.go:601-603):
```sh
cat > .kuso-static.Dockerfile <<EOF
FROM $RUNTIME_IMAGE
COPY $OUTPUT_DIR /usr/share/nginx/html
EOF
```
`RUNTIME_IMAGE` is set as a container env var (line 628). The heredoc `EOF` terminates the here-document when a line matches `EOF` exactly. If an attacker sets `runtimeImage` to a value containing a newline followed by `EOF` and then arbitrary Dockerfile instructions, the heredoc in the script will be terminated early and those lines will execute as shell commands in the `static-plan` init container, which runs `dropAllCapsRootAllowed()` (no specific user constraint). Example payload: `nginx\nEOF\nmalicious-shell-command`.

**`outputDir` → Dockerfile COPY path.** `$OUTPUT_DIR` in the `COPY` instruction is also unvalidated and could accept values like `../../etc/kuso` — though the actual damage here is limited to files the static-plan init container can access, not the final image.

**`builderImage` / `runtimeImage` → arbitrary image pull.** Any OCI image reference can be injected, which is intentional for custom builds but poses a supply-chain risk if no image policy (Kyverno/OPA) is enforced.

**`buildCmd` is intentionally a free-form shell command** (render.go:562) — this is by design (user-owned build container), but it is meaningful to document that `Deployer` role grants arbitrary code execution in the build container.

**Fix:**
- Validate `runtimeImage` and `builderImage` against an OCI reference regex that rejects newlines and shell metacharacters. A simple allowlist: `^[a-zA-Z0-9_./-]+(:[\w.-]+)?(@sha256:[0-9a-f]{64})?$`.
- Validate `outputDir` against the same `repoPathRE` already used for `repo.path` (`^[a-zA-Z0-9._/-]+$`, no `..`).
- Document in the API spec that `buildCmd` is an arbitrary shell command gated behind `Deployer` role.

---

## F-04 — Admin-minted tokens for target users do NOT trigger watermark invalidation (P2)

**File:** `server-go/internal/http/handlers/tokens_admin.go:68-158`

`POST /api/tokens/user/{userId}` (admin-only via `requireUserWrite`) mints a long-lived JWT for any user, persists a `Token` row, and returns the JWT. The JWT is signed with the full set of permissions the target user currently holds.

**Issue:** When the target user's group membership changes after the token was minted (e.g., the user is demoted from Owner to Viewer), the token's `Permissions` claim continues to reflect the old, higher-privilege set. The revocation path (per-jti `RevokedToken` check + per-user `UserTokenWatermark` check) does cover these tokens — but only if:
1. The admin explicitly revokes the token via `DELETE /api/tokens/{id}`, OR
2. The user's watermark is bumped (via `InvalidateUserTokens`).

`PutTenancy` (groups.go:143) and `RemoveMember` (groups.go:186) correctly call `InvalidateUsersByGroup` / `InvalidateUserTokens` after role changes. However, `IssueForUser` itself does **not** bump the watermark on the target user before signing. This means a token minted just before a demotion carries stale high-privilege claims until either the token's TTL expires or the admin explicitly revokes it. There is no automatic signal to the user that a new token was minted on their behalf (no audit log entry, no notification).

**Fix:**
1. Emit an audit log entry in `IssueForUser` naming the actor, the target user, and the token name.
2. Consider bumping the target user's watermark immediately after minting (invalidating all prior tokens) rather than adding one more long-lived token to an existing pile.

---

## F-05 — OAuth avatar URL from provider written to DB without sanitization (P2)

**File:** `server-go/internal/http/handlers/oauth.go:274-281`

On every OAuth login, the `prof.Image` URL returned by GitHub/generic OAuth is written directly to the `User.Image` field:
```go
if prof.Image != "" && (!user.Image.Valid || user.Image.String != prof.Image) {
    img := prof.Image
    if err := h.DB.UpdateUser(ctx, user.ID, db.UpdateUserInput{Image: &img}); err != nil { ... }
}
```

This field is subsequently served to the web client. If the OAuth provider is a self-hosted or compromised generic OAuth2 endpoint (configured via `KUSO_OAUTH2_*` env vars), it can supply an arbitrary URL or `javascript:` / `data:` URI as the image. The web client renders the avatar via `<img src={user.image}>` (standard HTML attribute) — `javascript:` URIs are safe in `img src` but `data:` URIs in `img src` could serve arbitrary content in older browsers, and in some CSP configurations this bypasses `img-src` restrictions.

More importantly: the `data:` URI case could be used to inflate the DB row size (multi-megabyte data URI), and an `http://internal.cluster.local/...` image URL would cause the **browser** to fetch from cluster-internal IPs when it renders the avatar (client-side SSRF via the browser, not server-side — the server never fetches the image).

**Fix:** Validate `prof.Image` against an `https://` scheme-only allowlist before persisting. Reject empty scheme, `javascript:`, `data:`, and `http:` URIs:
```go
if u, err := url.Parse(prof.Image); err != nil || u.Scheme != "https" || u.Host == "" {
    // ignore unsafe image URL
}
```

---

## F-06 — Webhook notification dispatcher: DNS-rebinding window (P2)

**File:** `server-go/internal/http/handlers/notifications.go:430-458` (`validateWebhookURL`)

`validateWebhookURL` correctly rejects RFC1918 IP literals and cluster-internal DNS suffixes at save time. However, the comment at line 429 explicitly notes:
> "We deliberately don't resolve DNS at validation time (race with 'DNS rebinding' + the lookup happens later anyway when notify dispatches). The dispatcher should also enforce an SSRF-safe dialer in a follow-up."

The dispatcher does use `httpx.SSRFSafeTransport()` (confirmed in `notify/notify.go:184`), which re-checks the resolved IP at dial time. So the SSRF protection at dispatch time is present.

However, `validateWebhookURL` does **not** block hostnames that resolve to RFC1918 addresses at registration time (only IP literals in that range). An attacker who registers a webhook URL pointing at a domain they control can arrange for it to resolve to `10.x.x.x` at dispatch time if the SSRF-safe transport's dial-time check uses `net.Dial` without `DialContext` (allowing the check to race with the TCP connect). The security depends entirely on `httpx.SSRFSafeTransport()` being correct and race-proof.

**Assessment:** Currently protected by the transport, but the defence is a single layer with no belt. This is the expected single-tenant posture — admins are trusted — but is worth tracking.

**Recommendation:** No immediate action required. Add a note to `validateWebhookURL` that the transport-level guard is the load-bearing check, so a future refactor that swaps transports doesn't silently drop the protection.

---

## F-07 — kuso-server ClusterRole: `secrets:get/list/watch` cluster-wide is overbroad (P1)

**File:** `deploy/server-go.yaml:42-44`

```yaml
- apiGroups: [""]
  resources: [secrets]
  verbs: [get, list, watch, create, update, patch, delete]
```

This is a cluster-wide binding (ClusterRoleBinding at line 100-111). The kuso-server SA can read, write, and delete **all** Secrets in **all** namespaces on the cluster. On a shared k3s node, this includes `kube-system` Secrets (kubeconfig, node certificates, bootstrap tokens) and any other workloads' Secrets.

**Impact:** A single successful exploit of kuso-server (RCE via a vulnerability in the Go HTTP stack, dependency CVE, or a logic bug) gives an attacker the full cluster's Secrets store. This includes k3s's service-account signing key (`k3s-serving` secret in `kube-system`), which can be used to sign arbitrary service-account tokens.

**Context:** The comment at line 11-12 notes this is known and tracked for v0.8's namespace-scoped split. This is a known architectural debt, not a regression.

**Recommended scope for a near-term tighten:**
- Secrets in `kube-system`: only read `kuso-buildkitd-tls` and `kuso-*` prefixed secrets (or none at all — the buildkitd TLS is managed by cert-manager, not kuso-server).
- Cross-namespace `list/watch`: needed for addon `conn` secrets; consider limiting to namespaces labelled `app.kubernetes.io/managed-by=kuso`.
- Full CRUD on all namespaces: overly broad given kuso only manages `kuso` and project namespaces.

---

## F-08 — `pods/exec` cluster-wide on kuso-server ClusterRole (P1)

**File:** `deploy/server-go.yaml:70-71`

```yaml
- apiGroups: [""]
  resources: [pods/log, pods/exec, pods/portforward]
  verbs: [get, list, create]
```

The kuso-server SA has `pods/exec create` cluster-wide. This is needed for `kuso shell` (the CLI-driven `kubectl exec` wrapper), which proxies the user's shell session. However, the exec is **not proxied through kuso-server** — the CLAUDE.md architecture note explains that `kuso shell` merely calls `GET /api/projects/{p}/services/{s}/pods` to discover a pod name, then the CLI user runs `kubectl exec` locally. The server itself does not exec into pods.

Yet the SA has the `pods/exec` verb. This means a compromised kuso-server process can exec into any pod cluster-wide — including `kube-system` pods (etcd, kube-apiserver, k3s-agent) — using its own credentials.

**Fix:** Remove `pods/exec` and `pods/portforward` from the ClusterRole. The server does not need them. The `kuso shell` flow reads pod names via `pods:list` and the actual exec runs on the user's local machine.

---

## F-09 — GitHub App private key: blast-radius extends to all GitHub App installations (P2)

**Files:** `deploy/server-go.yaml:296`, `server-go/internal/installscripts/scripts/install.sh:737`

The GitHub App private key (`GITHUB_APP_PRIVATE_KEY`) is loaded from the `kuso-github-app` Secret and mounted into kuso-server's environment. The key signs GitHub App installation access tokens — these tokens grant read/write access to every repository the App is installed on.

**Blast radius of kuso-server compromise:** An attacker who achieves RCE on the kuso-server pod can extract `GITHUB_APP_PRIVATE_KEY` from the process environment and mint unlimited installation tokens for any GitHub repository the App can see, with whatever permissions the App was granted (typically: `contents:write`, `pull_requests:write`, `checks:write`).

**Mitigation in place:** The key is in a Kubernetes Secret (not hardcoded), mounted via `optional: true`, and the App permissions are set at GitHub App creation time.

**Gap:** There is no rotation story. If the key is compromised, the path is: revoke the old key in the GitHub App settings and regenerate a new `.pem`, then re-run `install.sh` fragments or manually update the Secret. This is not documented.

**Recommendation:**
1. Document the key rotation procedure in `docs/GITHUB_APP_SETUP.md`.
2. Treat the GitHub App private key as a rotation-required secret distinct from JWT signing key and session key.

---

## F-10 — Updater allows downgrade to any prior signed version (P2)

**File:** `server-go/internal/updater/updater.go:328-440`

`fetchVersion` fetches an arbitrary version tag from GitHub releases, verifies the release.json signature (when `KUSO_REQUIRE_SIGNATURES=true`), and returns the manifest. `StartUpdate` calls this path when the caller passes an explicit `version` field in the POST body. Because any signed release passes verification, an admin can deliberately downgrade to a version with known vulnerabilities.

**Impact:** Within the single-tenant model, an admin triggering a downgrade to a known-vulnerable version is an admin-class action — this requires `PermSystemUpdate` and an active session. The risk is: a compromised or malicious admin, or a confused operator who thinks they're rolling back a bad upgrade but actually exposes a CVE.

**Mitigation absent:** The manifest's `Breaking` flag and `canAutoUpgrade` logic prevent *automatic* downgrades but do not block a user from forcing one via the explicit version field.

**Fix:** Add a `MinVersion` field to the `Manifest` struct. If the running version is greater than the requested target version AND the target manifest has no `AllowDowngrade: true` flag (or the running version is above `MinVersion`), refuse with a 400 and a clear message. This closes the admin-confusion attack surface without preventing deliberate rollbacks by an informed operator (who can set `KUSO_REQUIRE_SIGNATURES=false` or use `AllowDowngrade`).

---

## F-11 — Admin-minted tokens for other users: no audit trail (P2)

**File:** `server-go/internal/http/handlers/tokens_admin.go:68-158`

`IssueForUser` creates a long-lived JWT for an arbitrary user and returns the token once in the HTTP response. There is no audit log entry for this action. An attacker who compromises an admin session can mint a permanent token for any user, log out, and retain access indefinitely — with no record in the audit log of the token creation.

**Contrast:** Group membership changes (`AddMember`, `RemoveMember`, `PutTenancy`) are logged in the system logger at warn severity. Token creation for self (via `AdminHandler`) does log. Admin-for-user creation does not.

**Fix:**
```go
if h.Audit != nil {
    actor := ""
    if claims, ok := auth.ClaimsFromContext(r.Context()); ok {
        actor = claims.UserID
    }
    h.Audit.Log(r.Context(), audit.Entry{
        User:     actor,
        Severity: "warn",
        Action:   "token.admin_issue",
        Resource: "token",
        Message:  fmt.Sprintf("admin issued token %q for user %s (expires %s)", req.Name, user.ID, expiresAt.Format(time.RFC3339)),
    })
}
```

---

## F-12 — Group delete / tenancy change: no audit trail (P2)

**File:** `server-go/internal/http/handlers/groups.go`

`Delete` (line 192), `Create` (line 49), `Update` (line 74), and `PutTenancy` (line 121) all go through `requireUserWrite` (user:write perm, which admins hold). None emit an `audit.Entry`. `PutTenancy` is the most sensitive — it changes which projects and at what privilege level every member of a group can act on. A compromised admin account could silently reassign groups and there would be no audit record.

**Contrast:** `project.delete` and `service.delete` are logged at `critical`/`warn` severity. Group mutations are not logged at all.

**Fix:** Wire `h.Audit` into `GroupsHandler` and emit entries for `Create`, `Update`, `Delete`, and `PutTenancy`. Suggested severity: `warn` for create/update, `critical` for delete (irreversible), `warn` for tenancy changes.

---

## F-13 — Notification webhook config leaks in `Config` map (P2)

**File:** `server-go/internal/http/handlers/notifications.go:276-313`

The `notifBody.Config` field is a `map[string]any` that carries the webhook URL (and for Slack: the channel). On `Get` (line 256) and `List` (line 242) these are returned verbatim — including any secrets that an operator might have put in `config` beyond what the schema expects (e.g., a Slack API token if they use the generic webhook path instead of the Slack-native one).

The `Config` is stored as JSON in the DB and returned as-is. There is no masking of sensitive fields. The admin-only gate (`AdminOnly` middleware via `r.Group`) prevents non-admins from reading these, but any admin can read any webhook token from the API.

**Impact:** Within the single-tenant/admin model this is acceptable — admins configure and can read their own webhook secrets. However, an audit trail for reads of notification configs does not exist, and the response JSON is served over TLS with no additional masking. If a webhook token has been rotated, the old value remains in the DB and will be returned on next `GET /api/notifications/{id}`.

**Recommendation:** Mask the `url` field in `List` responses (return only the host/scheme, not the full URL including path which may encode a token). Keep the full URL accessible only in single-item `Get` (admin must request the specific record). This reduces the information density of a bulk list enumeration.

---

## F-14 — `static-plan` container `WorkingDir` is built from unvalidated `repoPath` (P1)

**File:** `server-go/internal/buildcontroller/render.go:622`

```go
WorkingDir: "/workspace/src/" + repoPath(b),
```

`repoPath(b)` returns `b.Spec.Repo.Path`. The `repo.path` field IS validated by `validateRepoPath` on the kuso-server `AddService`/`PatchService` path, but the buildcontroller reads the value from the CR **as stored** — it trusts the CR's stored value without re-validating. As noted in `buildcontroller.go:169-171`:
> "kuso-server Create path validates these before stamping the CR, so seeing one here means an external apply."

An admin (or a compromised kuso-server process that can patch CRs) who writes a KusoBuild CR directly with `spec.repo.path` containing shell metacharacters will cause `WorkingDir` to be set to a path like `/workspace/src/$(evil)`. Kubernetes doesn't evaluate shell in `workingDir` — it's passed to the container runtime as-is — so this specific vector only results in a container startup error, not arbitrary code execution.

However, the path `/workspace/src/../../etc` (path traversal) would resolve inside the container to `/etc`. With `dropAllCapsRootAllowed()` this could expose the init container's `/etc` as the working directory, allowing relative path escapes in the subsequent `sh -c "$BUILD_CMD"` execution.

**Fix:** Call `validateRepoPath(repoPath(b))` inside `reconcile` before creating the Job, and return an error (skip the reconcile) if validation fails. This provides defense-in-depth for externally-applied CRs.

---

## Summary Table

| ID   | Severity | Area                         | Brief Description                                                         |
|------|----------|------------------------------|---------------------------------------------------------------------------|
| F-01 | P2       | Auth/Documentation           | Misleading "fail-open" comment on revocation checker — actually fails closed |
| F-02 | P1       | Build controller             | No namespace ownership check — CR in any namespace triggers Job creation  |
| F-03 | P1       | Build controller / Input val | Static build: `runtimeImage`, `outputDir` unvalidated → Dockerfile injection |
| F-04 | P2       | AuthN/JWT                    | Admin-minted user tokens carry stale permissions after role change        |
| F-05 | P2       | OAuth                        | Provider avatar URL persisted without scheme validation                   |
| F-06 | P2       | Notifications / SSRF         | DNS-rebinding window partially mitigated by transport; single-layer defence |
| F-07 | P1       | RBAC / Cluster               | kuso-server ClusterRole: Secrets cluster-wide is overbroad                |
| F-08 | P1       | RBAC / Cluster               | `pods/exec` cluster-wide not needed by server code path                   |
| F-09 | P2       | Secrets / GitHub             | GitHub App private key: no rotation procedure documented                  |
| F-10 | P2       | Updater                      | No minimum-version guard: admin can downgrade to known-vulnerable version  |
| F-11 | P2       | Audit trail                  | Admin issuing token for another user leaves no audit log entry            |
| F-12 | P2       | Audit trail                  | Group create/update/delete/tenancy changes leave no audit log entry       |
| F-13 | P2       | Info disclosure              | Webhook config URLs (may embed tokens) returned verbatim in list response |
| F-14 | P1       | Build controller / Input val | `repoPath` not re-validated in buildcontroller before use as WorkingDir   |

---

## Severity Definitions

- **P0 — Ship blocker:** Remotely exploitable without authentication, or by any authenticated user. Leads to data exfiltration, code execution, or cluster compromise without attacker-controlled infra.
- **P1 — High:** Exploitable by an authenticated user (any role), or by an admin with indirect consequences (cluster-wide privilege escalation, persistent unauthorized access). Should be fixed before next release.
- **P2 — Medium:** Requires admin compromise or elevated preconditions. Audit/visibility gaps, single-layer SSRF defences, documentation debt that could cause a future security regression.

No P0 findings. The codebase shows strong security engineering in the auth stack (fail-closed revocation, CSRF middleware, project-scope checks, rate limiting) and the build pipeline (PSS=restricted on project namespaces, per-build SA with no bindings, seccomp RuntimeDefault). The highest-impact issues (F-02, F-03, F-07, F-08, F-14) are all architectural or input-validation gaps in recently-added code (buildcontroller) and in the ClusterRole scope which is documented technical debt.
