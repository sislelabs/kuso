# Build kuso-server image (NestJS backend + bundled Vue frontend).
# Two-stage: build with dev deps, then a slim release stage with prod deps only.
#
#   docker build -t ghcr.io/sislelabs/kuso-server:v0.1.0-dev .

FROM node:22-alpine AS build
ENV NODE_ENV=development

WORKDIR /build

# Server first so its dist/ exists before client writes its bundle into it.
COPY server ./server
RUN cd /build/server && yarn install --frozen-lockfile
RUN cd /build/server && yarn build

# Client builds into ../server/dist/public per its vite.config.ts outDir.
COPY client ./client
RUN cd /build/client && yarn install --frozen-lockfile
RUN cd /build/client && yarn build

FROM node:22-alpine AS release
ARG VERSION=v0.1.0-dev

LABEL maintainer="https://kuso.sislelabs.com"
LABEL version=$VERSION
LABEL org.opencontainers.image.source="https://github.com/sislelabs/kuso"
LABEL org.opencontainers.image.licenses="GPL-3.0"

ENV NODE_ENV=production

WORKDIR /app/

COPY --from=build /build/server/dist /app/server
COPY --from=build /build/server/package.json /app/server/package.json
COPY --from=build /build/server/src/deployments/templates /app/server/deployments/templates
COPY --from=build /build/server/node_modules /app/server/node_modules
COPY server/prisma /app/server/prisma

# Bundled Vue frontend ends up at server/dist/public (vite outDir).
COPY --from=build /build/server/dist/public /app/server/public

ENV DATABASE_URL=file:/app/server/db/kuso.sqlite
ENV DATABASE_TYPE=sqlite

RUN echo -n $VERSION > /app/server/VERSION

WORKDIR /app/server

CMD ["node", "main"]
