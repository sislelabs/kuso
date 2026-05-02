# kuso web

Next.js 16 frontend for kuso. Static export embedded into the Go server.

## Dev

```bash
# Terminal 1: backend
cd ../server-go && JWT_SECRET=dev go run ./cmd/kuso-server

# Terminal 2: frontend (proxies /api and /ws to :3000 via next.config.ts rewrites)
cd web && npm run dev
```

Open http://localhost:3000 (Next dev). The dev server runs on port 3000, the
Go server runs on port 3000 too by default — set `KUSO_HTTP_ADDR=:8080` on
the Go server and `NEXT_PUBLIC_KUSO_API_URL=http://localhost:8080` for the
frontend if you want them simultaneous.

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
Better-Auth-shaped object so robiv0 components port unmodified. JWT is
stored in localStorage (`kuso.jwt`) and a cookie (`kuso.JWT_TOKEN`) for
backend middleware that reads the cookie on browser-driven routes.
