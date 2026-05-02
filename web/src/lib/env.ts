// Frontend env helper. The static export is served same-origin by the Go
// server in production, so we never need an absolute base URL at runtime.
// In dev, Next's rewrites in next.config.ts proxy /api and /ws to :8080.
export const env = {
  apiBase: typeof window === "undefined" ? "" : "",
};
