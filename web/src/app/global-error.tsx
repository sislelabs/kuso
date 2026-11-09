"use client";

// global-error.tsx is Next.js's last-resort boundary — it catches
// render errors that escape the root layout itself (including errors
// thrown inside the (app)/(auth)/(marketing) layouts). Because it
// REPLACES the whole document when it fires, it has to render its own
// <html>/<body>. Everything else in the tree is gone by the time this
// shows, so we keep it self-contained: inline colours (theme CSS vars
// may not be applied) and no shared components that could re-throw.
export default function GlobalError({
  error,
  reset,
}: {
  error: Error & { digest?: string };
  reset: () => void;
}) {
  return (
    <html lang="en">
      <body
        style={{
          margin: 0,
          minHeight: "100vh",
          display: "flex",
          alignItems: "center",
          justifyContent: "center",
          background: "#0A0C15",
          color: "#ECEAE0",
          fontFamily:
            "ui-sans-serif, system-ui, -apple-system, Segoe UI, Roboto, sans-serif",
        }}
      >
        <div style={{ maxWidth: 420, padding: 24, textAlign: "center" }}>
          <p
            style={{
              margin: 0,
              fontSize: 13,
              color: "#9EA3B5",
            }}
          >
            Something broke
          </p>
          <h1 style={{ margin: "8px 0 0", fontSize: 18, fontWeight: 600 }}>
            kuso couldn&apos;t render this page
          </h1>
          <p
            style={{
              margin: "12px 0 0",
              fontSize: 13,
              color: "#B4B8C6",
              wordBreak: "break-word",
            }}
          >
            {error?.message || "The application failed to render."}
          </p>
          {error?.digest && (
            <p
              style={{
                margin: "6px 0 0",
                fontFamily: "ui-monospace, SFMono-Regular, Menlo, monospace",
                fontSize: 12,
                color: "#9EA3B5",
              }}
            >
              digest: {error.digest}
            </p>
          )}
          <div
            style={{
              marginTop: 20,
              display: "flex",
              gap: 8,
              justifyContent: "center",
            }}
          >
            <button
              type="button"
              onClick={() => reset()}
              style={{
                cursor: "pointer",
                borderRadius: 6,
                border: "1px solid #ACBEA3",
                background: "#ACBEA3",
                color: "#0A0C15",
                fontWeight: 500,
                padding: "6px 14px",
                fontSize: 13,
              }}
            >
              Try again
            </button>
            <a
              href="/projects"
              style={{
                display: "inline-flex",
                alignItems: "center",
                borderRadius: 6,
                border: "1px solid transparent",
                color: "#B4B8C6",
                padding: "6px 14px",
                fontSize: 13,
                textDecoration: "none",
              }}
            >
              Go to projects
            </a>
          </div>
        </div>
      </body>
    </html>
  );
}
