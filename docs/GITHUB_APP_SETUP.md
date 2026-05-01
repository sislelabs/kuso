# GitHub App registration

kuso uses a GitHub App for repo access — OAuth for sign-in, an installation token for repo reads, and webhooks for push/pull-request events that drive deploys and previews.

This is a **one-time, click-through step.** You need an admin account on the org/user that will own the App. For SisleLabs, that's https://github.com/organizations/sislelabs.

## 1. Register the App

Open https://github.com/organizations/sislelabs/settings/apps/new and fill in:

| Field | Value |
| --- | --- |
| **GitHub App name** | `kuso-sislelabs` (or any unique name; this becomes the App's slug) |
| **Description** | "kuso PaaS — connects repos to deploys" |
| **Homepage URL** | `https://kuso.sislelabs.com` |
| **Identifying and authorizing users** → **Callback URL** | `https://kuso.sislelabs.com/api/github/setup-callback` |
| **Post installation** → **Setup URL (optional)** | `https://kuso.sislelabs.com/api/github/setup-callback` |
| **Identifying and authorizing users** → **Request user authorization (OAuth) during installation** | ✅ checked |
| **Webhook** → **Active** | ✅ checked |
| **Webhook URL** | `https://kuso.sislelabs.com/api/webhooks/github` |
| **Webhook secret** | Generate a random 32+ char string. Save it — you'll paste it into the kuso secret. |
| **SSL verification** | Enabled |

### Repository permissions

| Permission | Access |
| --- | --- |
| Contents | Read-only |
| Metadata | Read-only |
| Pull requests | Read & write |
| Webhooks | Read-only (the App self-installs hooks; we don't need to manage them) |

### Account permissions

| Permission | Access |
| --- | --- |
| Email addresses | Read-only |

### Subscribe to events

- ✅ Push
- ✅ Pull request
- ✅ Installation
- ✅ Installation repositories

### Where can this GitHub App be installed?

- Choose **Any account** if you want users from outside SisleLabs to install it (multi-tenant kuso).
- Choose **Only on this account** if kuso is for SisleLabs only.

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

Edit the `kuso-server-secrets` Secret on the cluster:

```bash
export KUBECONFIG=~/.kube/kuso-hetzner.yaml
kubectl edit secret kuso-server-secrets -n kuso
```

Set these `stringData` keys (the file shows them base64-encoded under `data` after first save; the editor lets you paste plaintext under `stringData` instead):

```yaml
stringData:
  GITHUB_APP_ID: "1234567"
  GITHUB_APP_SLUG: "kuso-sislelabs"
  GITHUB_APP_CLIENT_ID: "Iv1.abc..."
  GITHUB_APP_CLIENT_SECRET: "abc..."
  GITHUB_APP_WEBHOOK_SECRET: "the-32-char-string-from-step-1"
  # Paste the entire PEM. Newlines in the YAML are preserved by the
  # | block-literal syntax.
  GITHUB_APP_PRIVATE_KEY: |
    -----BEGIN RSA PRIVATE KEY-----
    MIIEogIBAAKCAQEA...
    ...
    -----END RSA PRIVATE KEY-----
```

Roll the kuso-server pod so it picks up the new env:

```bash
kubectl rollout restart deployment/kuso-server -n kuso
```

## 5. Verify

```bash
TOKEN=$(curl -s -X POST -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"kuso-admin"}' \
  https://kuso.sislelabs.com/api/auth/login | jq -r .access_token)

curl -s -H "Authorization: Bearer $TOKEN" \
  https://kuso.sislelabs.com/api/github/install-url
```

You should get `{"configured":true,"url":"https://github.com/apps/kuso-sislelabs/installations/new"}`.

Visit that URL, install the App on the org/user that owns the repos you want to deploy, then:

```bash
curl -s -X POST -H "Authorization: Bearer $TOKEN" \
  https://kuso.sislelabs.com/api/github/installations/refresh

curl -s -H "Authorization: Bearer $TOKEN" \
  https://kuso.sislelabs.com/api/github/installations | jq
```

You should see your installation with a list of accessible repos. Project create flow (Phase 4) reads from this cache.
