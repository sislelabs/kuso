# Frontend build-time environment variables

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
