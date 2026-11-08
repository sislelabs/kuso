"use client";

import { useState } from "react";
import { env } from "@/lib/env";
import { cn } from "@/lib/utils";
import { LETTERMARK_TINTS, lettermark, lettermarkTint } from "@/features/marketplace";

const CATEGORY_LABELS: Record<string, string> = {
  "dev-tools": "Developer tools",
  ai: "AI",
  comms: "Communication",
  content: "Content & CMS",
};

export function categoryLabel(c: string): string {
  if (CATEGORY_LABELS[c]) return CATEGORY_LABELS[c];
  const s = c.replace(/-/g, " ");
  return s.charAt(0).toUpperCase() + s.slice(1);
}

// Tint classes per token, spelled out so Tailwind sees them.
const TINT_CLASS: Record<(typeof LETTERMARK_TINTS)[number], string> = {
  "--chart-1": "bg-[var(--chart-1)]/25 text-[var(--chart-1)]",
  "--chart-2": "bg-[var(--chart-2)]/25 text-[var(--chart-2)]",
  "--chart-3": "bg-[var(--chart-3)]/25 text-[var(--chart-3)]",
  "--chart-4": "bg-[var(--chart-4)]/25 text-[var(--chart-4)]",
  "--chart-5": "bg-[var(--chart-5)]/25 text-[var(--chart-5)]",
};

// AppIcon shows the catalog's SVG brand mark over a tinted lettermark.
// The lettermark is what you see until the image loads, and for good
// when the app has no icon (the endpoint 404s).
export function AppIcon({ app, title, className }: { app: string; title: string; className?: string }) {
  const [loaded, setLoaded] = useState(false);
  const [failed, setFailed] = useState(false);
  return (
    <span
      aria-hidden
      className={cn(
        "relative inline-flex shrink-0 items-center justify-center overflow-hidden rounded-lg font-heading font-semibold select-none [container-type:size]",
        TINT_CLASS[lettermarkTint(app)],
        className,
      )}
    >
      {!loaded && <span className="text-[50cqh] leading-none">{lettermark(title)}</span>}
      {!failed && (
        <img
          src={`${env.apiBase}/api/marketplace/${encodeURIComponent(app)}/icon?v=4`}
          alt=""
          className={cn("absolute inset-0 h-full w-full", !loaded && "opacity-0")}
          // A cached image can finish before onLoad is attached.
          ref={(img) => {
            if (img?.complete && img.naturalWidth > 0) setLoaded(true);
          }}
          onLoad={() => setLoaded(true)}
          onError={() => setFailed(true)}
        />
      )}
    </span>
  );
}
