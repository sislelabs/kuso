# Frontend build-time environment variables

> **Scope: this doc is about NON-SECRET, public values** (`NEXT_PUBLIC_*`,
> `VITE_*`, …) that a framework inlines into the browser bundle. **Secrets are a
> different rule entirely** — as of v0.23.0 kuso withholds `secretKeyRef`-sourced
> env vars from the build so they can't be baked into image layers, so a build
> step that reads a secret gets an empty value. If you need a secret at build
> time see [Build-time secrets](#build-time-secrets) below. If you need a
> non-secret *constant* only at build time (not in runtime env), use the
> service `buildArgs` map (v0.23.1+). The sentinel-substitution technique in this
> doc is specifically for public values the browser must see.

Some frontend frameworks **inline environment variables into the
browser bundle at build time** rather than reading them at runtime.
This breaks the kuso "build once, deploy to many envs" model unless
you plan for it.

Frameworks that do this and the prefix they expose:

| Framework | Prefix |
|-----------|--------|
| Next.js   | `NEXT_PUBLIC_*` |
| Vite      | `VITE_*` |
| Create React App | `REACT_APP_*` |
| SvelteKit | `PUBLIC_*` |
| Astro     | `PUBLIC_*` |
| Nuxt      | `NUXT_PUBLIC_*` |

## The problem

Kuso runs one image per commit and deploys it to every environment
(production, staging, preview-PRs). If your build script reads
`NEXT_PUBLIC_API_URL` at `npm run build` time, the value is hard-coded
into every browser chunk. At runtime, setting `NEXT_PUBLIC_API_URL`
on the pod does nothing — the value has already been serialised into
`.next/static/chunks/*.js`.

Symptom: browser DevTools shows requests to a stale URL (the build-
time default, often `http://localhost:8080` or whatever you set when
you first hacked the Dockerfile). The CSP correctly blocks them.

## The fix: placeholder substitution at startup

Build the image with **opaque sentinel strings** in place of the
real values. At container startup, sed the placeholders to whatever
the env provides. One image, many envs.

### Dockerfile

```dockerfile
FROM node:20-alpine AS builder
WORKDIR /app
COPY package.json package-lock.json* ./
RUN npm ci
COPY . .

# Sentinels chosen so a blind grep finds only our placeholders, never
# an accidental match elsewhere in the bundle.
ENV NEXT_PUBLIC_API_URL=__KUSO_RUNTIME_NEXT_PUBLIC_API_URL__
ENV NEXT_PUBLIC_SITE_URL=__KUSO_RUNTIME_NEXT_PUBLIC_SITE_URL__
# ... one ENV line per NEXT_PUBLIC_* var the app reads

RUN npm run build

FROM node:20-alpine AS runner
WORKDIR /app
COPY --from=builder /app/.next/standalone ./
COPY --from=builder /app/.next/static ./.next/static
COPY --from=builder /app/public ./public
COPY scripts/runtime-substitute.sh /app/runtime-substitute.sh
RUN chmod 0755 /app/runtime-substitute.sh
CMD ["/app/runtime-substitute.sh"]
```

### scripts/runtime-substitute.sh

```sh
#!/bin/sh
set -e
cd /app

# List every NEXT_PUBLIC_* var your app reads. The build must bake a
# placeholder for each; the runtime swaps it for the container env.
VARS="NEXT_PUBLIC_API_URL NEXT_PUBLIC_SITE_URL"

for var in $VARS; do
  val=$(printenv "$var" || true)
  if [ -n "$val" ]; then
    placeholder="__KUSO_RUNTIME_${var}__"
    # '|' as sed separator so URL slashes pass through unescaped;
    # escape sed metachars inside $val itself.
    esc=$(printf '%s' "$val" | sed -e 's/[\\&|]/\\&/g')
    find .next -type f \( -name '*.js' -o -name '*.json' -o -name '*.html' \) \
      -exec sed -i "s|${placeholder}|${esc}|g" {} +
  fi
done

exec node server.js
```

### Configure kuso env vars per environment

`kuso env set` writes to the service spec and propagates to every
env, so it can't differentiate prod from staging. For per-env
overrides use `kuso secret set --env <name>` — the value is stored
in a kube Secret scoped to that env only, and your container reads
it via `process.env.NEXT_PUBLIC_API_URL` exactly the same way:

```
# baseline (applied to every env)
kuso env set hello web NEXT_PUBLIC_API_URL=https://api.example.com

# staging override (per-env secret, takes precedence)
kuso secret set hello web NEXT_PUBLIC_API_URL https://api-staging.example.com --env staging
```

The "secret" label is a wire-format detail — there's nothing actually
secret about a public URL. Treat per-env secrets as "per-env env
vars" for the purposes of NEXT_PUBLIC_*. The pod env-merge order
puts env-scoped Secret values after service-level env vars, so the
override wins. The runtime substitute script doesn't care which
source provided the value; it just reads `printenv`.

No rebuild needed when values change — `kuso secret set --env …` and
let the pod restart.

## Why not pass `--build-arg` per env?

Two reasons:

1. Forces a fresh image per environment. Build time doubles. With
   preview PRs (one env per pull request) the cost explodes.
2. Loses the kuso promise of "the artifact deployed to staging is
   the same artifact deployed to production." Different bundles can
   misbehave differently.

The placeholder dance keeps both properties.

## Static-build frameworks (no server.js)

For pure static builds (Vite, CRA, plain `next export`), use kuso's
`strategy: static` and add a `kuso-static.dockerfile` step that runs
the substitution on the static output before nginx ships it. The
same script works — point `cd` and `find` at your build output
directory (`dist/`, `build/`, `out/`) instead of `.next/`.

## Secrets

**Never** put secrets in `NEXT_PUBLIC_*` (or any of the prefixes
above). Whatever you put there gets shipped to every browser that
loads your site. Use server-side env vars + a Next.js API route /
SvelteKit endpoint / etc. that proxies the call from the server.

<a id="build-time-secrets"></a>
### Build-time secrets (v0.23.0+)

kuso **withholds `secretKeyRef`-sourced env vars from the build.**
Rationale: build-time values persist in the published image — as
`--build-arg` values recoverable via `docker history`, or as `ENV`
layers in a nixpacks-generated Dockerfile — so anyone with registry
read access could recover them. To close that leak, secret-sourced
vars (`kuso secret set`, addon-conn secrets, any `valueFrom.secretKeyRef`)
are simply not present during the build. A build step that reads one
gets an **empty value**, and the build log prints a WARNING:
`secret-sourced build env vars are no longer passed as build-args`.

**If your build genuinely needs a secret, pick by build strategy:**

| Strategy | How to get a secret at build time |
|----------|-----------------------------------|
| `dockerfile` | BuildKit **secret mount** — the value is mounted for one RUN and never lands in a layer: `RUN --mount=type=secret,id=DATABASE_URL DATABASE_URL="$(cat /run/secrets/DATABASE_URL)" ./build-step`. Migrate any `ARG <SECRET>` usage to this. |
| `nixpacks` / `static` | **No build-time escape hatch** (these bake env into layers). Move the work to **runtime**. |

**The common case — DB migrations.** Don't run `prisma migrate deploy`
(or any migrate) in the build script; it needs a live `DATABASE_URL`
which is now empty. Move it to a **release hook** (`kuso.yml`
`services[].release: { command: [...] }`), which runs post-build /
pre-promote with the runtime secrets and last-green semantics — a
failed migration blocks the promotion instead of half-deploying.
Keep schema-only steps (`prisma generate` — reads the schema file, no
DB connection) in the build; they're unaffected.

```yaml
# kuso.yml — migrations run as a release hook, not in the build
services:
  - name: web
    runtime: nixpacks
    release:
      command: ['npx', 'prisma', 'migrate', 'deploy']
# and in the repo: build script is `prisma generate && next build`
# (NOT `prisma generate && prisma migrate deploy && next build`)
```

### Non-secret build-time constants: `buildArgs` (v0.23.1+)

For a value you need at **build time** that is **not a secret** and is
**not** part of the app's runtime env (a build toggle, a version
string, a public feature flag), use the service `buildArgs` map:

```yaml
services:
  - name: web
    buildArgs:
      BUILD_PROFILE: production
      SENTRY_RELEASE: v1.4.2
```

`buildArgs` flows to `--build-arg` (dockerfile) and build-time `ENV`
(nixpacks). It accepts **plain literals only** — a `secretKeyRef` can
never be routed through it (the server drops any secret-shaped value),
so it's safe to persist in image layers by design. Don't put secrets
here; use a release hook or a `--mount=type=secret` RUN for those.
This is distinct from `env` (runtime) and from `publicEnv` (the
browser-inlined sentinel mechanism described above).
