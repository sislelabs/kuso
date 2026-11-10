# server-go/internal/web

Holds the embedded Next.js SPA bundle. Layout:

```
web/
├── dist/      Next.js static export. Source-controlled `.gitkeep`
│              keeps `go:embed` happy in fresh checkouts; the real
│              bundle is dropped in by the build pipeline before
│              `go build` runs.
├── web.go     embed.FS + Dist() helper.
└── README.md  this file
```

## How the bundle gets here

`scripts/build-frontend.sh` runs the Next build and copies `web/out/`
into `server-go/internal/web/dist/` for local builds. The Dockerfile's
`web-build` stage does the same thing itself (`pnpm build`, then copies
`web/out` into `dist/`); it doesn't call the script.

## Local dev

For ad-hoc `go run ./cmd/kuso-server` outside Docker, the placeholder
`.gitkeep` keeps `embed.FS` happy. Set `KUSO_DEV_CORS=1` and run the
Next dev server (`pnpm dev` in `web/`) on port 3000 — it proxies
`/api/*` and `/ws/*` to the Go server via `next.config.ts` rewrites.
