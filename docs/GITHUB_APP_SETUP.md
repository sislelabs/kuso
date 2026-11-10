# GitHub App registration

kuso uses a GitHub App for repo access — OAuth for sign-in, an installation token for repo reads, and webhooks for push/pull-request events that drive deploys and previews.

This is a **one-time, click-through step.** You need an admin account on the org/user that will own the App. For SisleLabs, that's https://github.com/organizations/sislelabs.

**Shortcut:** in the kuso UI, Settings → GitHub → **Create GitHub App** runs GitHub's App-manifest flow: kuso pre-fills every field, permission and event below, receives the credentials back, writes them to the `kuso-github-app` Secret and restarts kuso-server. Use it and skip to step 5. The manual steps below are for registering the App by hand.

## 1. Register the App

Open https://github.com/organizations/sislelabs/settings/apps/new and fill in:

| Field | Value |
| --- | --- |
| **GitHub App name** | `kuso-sislelabs` (or any unique name; this becomes the App's slug) |
| **Description** | "kuso PaaS — connects repos to deploys" |
| **Homepage URL** | `https://kuso.sislelabs.com` |
| **Identifying and authorizing users** → **Callback URL** | `https://kuso.sislelabs.com/api/auth/github/callback` (GitHub sign-in) |
| **Post installation** → **Setup URL (optional)** | `https://kuso.sislelabs.com/api/github/setup-callback` |
| **Identifying and authorizing users** → **Request user authorization (OAuth) during installation** | unchecked (the manifest flow leaves it off; if checked, GitHub skips the Setup URL and kuso forwards the install redirect from the Callback URL) |
| **Webhook** → **Active** | ✅ checked |
| **Webhook URL** | `https://kuso.sislelabs.com/api/github/webhook` (`/api/webhooks/github` is an alias) |
| **Webhook secret** | Generate a random 32+ char string. Save it — you'll paste it into the kuso secret. |
| **SSL verification** | Enabled |

### Repository permissions

| Permission | Access |
| --- | --- |
| Contents | Read & write (the incident agent pushes fix branches) |
| Metadata | Read-only |
| Pull requests | Read & write |
| Commit statuses | Read & write (`kuso/<service>` status on every build) |
| Checks | Read-only (wait-for-CI gate) |
| Deployments | Read & write |

kuso flags installations that are missing `statuses:write` or `checks:read` — permission changes on the App only apply once each installation accepts them.

### Account permissions

| Permission | Access |
| --- | --- |
| Email addresses | Read-only |

### Subscribe to events

- ✅ Push
- ✅ Pull request

`installation` and `installation_repositories` events are delivered to every GitHub App without a checkbox; kuso handles both to refresh its installation cache.

### Where can this GitHub App be installed?

kuso is single-tenant by design (one team per cluster, like Coolify). Either choice works; **Only on this account** is the typical pick — it locks the App to your org and prevents accidental cross-account installs.

Click **Create GitHub App.**

## 2. Generate a private key

Right after creation you land on the App's settings page. Scroll to **Private keys** → **Generate a private key**. A PEM file downloads. **You won't be shown it again**, so save it.

## 3. Note the values you need

From the App settings page, collect:

- **App ID** (top of the page, e.g. `1234567`)
- **Client ID** (under "About")
- **Client secret** → click **Generate a new client secret** and save the value
- **App slug** (the URL `https://github.com/apps/<slug>` — derived from the name; e.g. `kuso-sislelabs`)
- **Webhook secret** — the value you set in step 1
- **Private key** — the PEM file from step 2

## 4. Wire the values into the kuso install

Easiest: paste them into Settings → GitHub (manual form), or use the CLI:

```bash
kuso github configure \
  --app-id 1234567 --app-slug kuso-sislelabs \
  --client-id Iv1.abc... --client-secret abc... \
  --webhook-secret the-32-char-string-from-step-1 --org sislelabs \
  --pem ~/Downloads/kuso-sislelabs.pem
```

Both post to `/api/github/configure`, which writes the `kuso-github-app` Secret in the `kuso` namespace and restarts kuso-server (~30s). kuso-server loads that Secret via `envFrom`.

By hand over ssh, the same Secret carries these keys:

```yaml
stringData:
  GITHUB_APP_ID: "1234567"
  GITHUB_APP_SLUG: "kuso-sislelabs"
  GITHUB_APP_CLIENT_ID: "Iv1.abc..."
  GITHUB_APP_CLIENT_SECRET: "abc..."
  GITHUB_APP_WEBHOOK_SECRET: "the-32-char-string-from-step-1"
  GITHUB_APP_ORG: "sislelabs"   # optional, informational
  # Paste the entire PEM. Newlines in the YAML are preserved by the
  # | block-literal syntax.
  GITHUB_APP_PRIVATE_KEY: |
    -----BEGIN RSA PRIVATE KEY-----
    MIIEogIBAAKCAQEA...
    ...
    -----END RSA PRIVATE KEY-----
```

then `kubectl rollout restart deployment/kuso-server -n kuso`.

## 5. Verify

```bash
kuso github status
```

It should report the App as configured, with install URL `https://github.com/apps/kuso-sislelabs/installations/new` (`GET /api/github/install-url` returns `{"configured":true,"url":"…"}`).

Visit that URL, install the App on the org/user that owns the repos you want to deploy, then:

```bash
kuso github refresh
kuso github installations
```

You should see your installation; `kuso github repos <installation-id>` lists the repos it can reach. The project create flow reads from this cache.
