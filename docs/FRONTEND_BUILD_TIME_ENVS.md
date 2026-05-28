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

### Configure kuso env vars normally

In your kuso service spec or via the dashboard, set the env vars per
environment:

```
production:  NEXT_PUBLIC_API_URL=https://api.example.com
staging:     NEXT_PUBLIC_API_URL=https://api-staging.example.com
```

The runtime script picks them up on container start. No rebuild
needed when values change — `kuso secret set` / `kuso env set` and
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
