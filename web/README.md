# kuso web

Next.js 16 frontend for kuso. Static export embedded into the Go server.

## Dev

```bash
# From the repo root.
# Terminal 1: backend on :8080 (its default, :3000, collides with Next dev)
cd server-go && JWT_SECRET=dev KUSO_HTTP_ADDR=:8080 go run ./cmd/kuso-server

# Terminal 2: frontend
cd web && pnpm install && pnpm dev
```

Open http://localhost:3000. `pnpm dev` runs `next dev --turbopack -p 3000`, and
the dev-only rewrites in `next.config.ts` proxy `/api`, `/ws` and `/healthz` to
`http://localhost:8080`. Set `NEXT_PUBLIC_KUSO_API_URL` to point them at a
different backend.

## Build

```bash
./scripts/build-frontend.sh   # from repo root
```

Output goes to `server-go/internal/web/dist/`. The Go server embeds it via
`//go:embed` and serves it from `/` with an `index.html` fallback for SPA
routes. The Dockerfile's `web-build` stage runs the same script.

## Stack

- Next.js 16 + React 19 (App Router, static export via `output: "export"`)
- Tailwind 4 + shadcn/ui (`base-nova`) + Base UI primitives
- TanStack Query for server state
- next-themes, sonner, lucide-react, cmdk, zod
- Visual reference: [biznesguys/robiv0](https://github.com/biznesguys/robiv0)
- Spec: `docs/superpowers/specs/2026-05-02-frontend-rewrite-nextjs-design.md`

## Auth shim

The existing Go backend issues JWTs against `/api/auth/login`. The frontend
uses a `useSession()` hook in `src/features/auth/hooks.ts` that calls
`/api/auth/session` + `/api/users/profile` and reshapes them into a
Better-Auth-shaped object so robiv0 components port unmodified. The session
lives in the HttpOnly `kuso.JWT_TOKEN` cookie the server sets on login; the
frontend never reads or stores the JWT itself.
