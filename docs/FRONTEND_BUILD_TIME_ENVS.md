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

If you list the names in the service's `publicEnv` (`kuso service set
<project> <service> --public-env NEXT_PUBLIC_API_URL`, or `publicEnv:` in
`kuso.yml`), kuso sets `KUSO_PUBLIC_ENV` on the pod to the
space-separated list, so the script can use `VARS="$KUSO_PUBLIC_ENV"`
instead of a hard-coded list.

kuso also bakes the sentinels into the build for every `publicEnv` name:

| Strategy | What the build sees |
|----------|---------------------|
| `dockerfile` | `--build-arg KEY=__KUSO_RUNTIME_KEY__`. Declare `ARG KEY` (or keep the `ENV` line above) before the build step. |
| `nixpacks` | `ENV KEY="__KUSO_RUNTIME_KEY__"` in the generated Dockerfile. |
| `static` | `KEY=__KUSO_RUNTIME_KEY__` in the build command's env. |
| `buildpacks` | Nothing; keep the sentinel in your own build config. |

The sentinel wins over a `buildArgs` entry or a runtime env value with the
same name, so the real value never gets inlined at build time. Swapping
the sentinels at startup is still your image's job. kuso doesn't ship a
substitution script, so you need one like the one above for every strategy.

### Configure kuso env vars per environment

`kuso env set` without `--env` writes to the service spec and
propagates to every env. Add `--env <name>` to scope a value to one
environment; your container reads it via `process.env.NEXT_PUBLIC_API_URL`
exactly the same way:

```
# baseline (applied to every env)
kuso env set hello web NEXT_PUBLIC_API_URL=https://api.example.com

# staging override (scoped to the staging env, takes precedence)
kuso env set hello web NEXT_PUBLIC_API_URL=https://api-staging.example.com --env staging
```

The runtime substitute script doesn't care which source provided the
value; it just reads `printenv`.

No rebuild needed when values change — `kuso env set --env …` and
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

For pure static builds (Vite, CRA, plain `next export`), kuso's
`runtime: static` generates its own Dockerfile (output copied into
`nginx:1.30-alpine`) with no hook for a startup script. To substitute
at startup, use `runtime: dockerfile` with an nginx stage whose
entrypoint runs the substitution on the static output before nginx
starts (or point `static.runtimeImage` at such an image). The same
script works — point `cd` and `find` at the served directory instead
of `.next/`, and `exec nginx -g 'daemon off;'` instead of `node`.

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

`buildArgs` flows to `--build-arg` (dockerfile), build-time `ENV`
(nixpacks) and the build command's env (static). It accepts **plain
literals only** — a `secretKeyRef` can never be routed through it (the server drops any secret-shaped value),
so it's safe to persist in image layers by design. Don't put secrets
here; use a release hook or a `--mount=type=secret` RUN for those.
This is distinct from `env` (runtime) and from `publicEnv` (the
browser-inlined sentinel mechanism described above).
