# syntax=docker/dockerfile:1.7
FROM alpine:3.20

# apk normally hits the network on every build. Persist /var/cache/apk
# across builds so a no-op rebuild is ~3s instead of ~12s. We keep
# --no-cache off the apk call (the buildkit mount handles caching) so
# the layer doesn't pin a specific apk index snapshot.
RUN --mount=type=cache,target=/var/cache/apk \
    apk add \
      bash \
      ca-certificates \
      curl \
      jq \
      kubectl

COPY entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

ENTRYPOINT ["/entrypoint.sh"]
