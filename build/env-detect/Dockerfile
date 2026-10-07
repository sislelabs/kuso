# Init-container image for the build Jobs' env-detect step: ripgrep + jq
# on alpine, so the step doesn't apk-install on every build.
FROM alpine:3.20

RUN apk add --no-cache \
      bash \
      jq \
      ripgrep

RUN adduser -D -u 1000 -g 1000 envdetect
USER 1000:1000

WORKDIR /workspace
