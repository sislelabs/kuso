# server-go/web

Holds the embedded Vue SPA bundle. The directory layout:

```
web/
├── dist/           Vue build output. Source-controlled placeholder
│                   here so `go build` doesn't error; the Dockerfile
│                   replaces it with the real Vite build before running
│                   `go build`.
└── README.md       this file
```

## How the bundle gets here in the image

The kuso Dockerfile is multi-stage. Stage 1 (`web-build`) runs the Vue
build with the Go-server-friendly outDir override:

```dockerfile
FROM node:22-bookworm-slim AS web-build
WORKDIR /web
COPY client ./
RUN yarn install --immutable && \
    yarn build --outDir /tmp/dist
```

Stage 2 (`build`) copies that output into `server-go/web/dist`, then
runs `go build`:

```dockerfile
COPY --from=web-build /tmp/dist /src/web/dist
RUN go build -o /out/kuso-server ./cmd/kuso-server
```

The Go binary then `embed.FS`-includes everything under `web/dist` and
serves it from `/` with a fall-through to `index.html` for SPA routes.

## Local dev

For ad-hoc `go run ./cmd/kuso-server` outside Docker, the placeholder
`index.html` keeps `embed.FS` happy. Set `KUSO_DEV_CORS=1` and run the
Vue dev server (`yarn dev`) on a separate port.
