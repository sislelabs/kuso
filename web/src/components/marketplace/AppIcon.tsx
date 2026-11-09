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

// Text tint per token, spelled out so Tailwind sees them.
const TINT_CLASS: Record<(typeof LETTERMARK_TINTS)[number], string> = {
  "--chart-1": "text-[var(--chart-1)]",
  "--chart-2": "text-[var(--chart-2)]",
  "--chart-3": "text-[var(--chart-3)]",
  "--chart-4": "text-[var(--chart-4)]",
  "--chart-5": "text-[var(--chart-5)]",
};

// AppIcon puts every app on the same neutral tile: the catalog's SVG
// brand mark inset with padding, or, when the app has no icon (the
// endpoint 404s), its initials. Same tile, same border, so a template
// without artwork doesn't look like a different kind of thing.
export function AppIcon({ app, title, className }: { app: string; title: string; className?: string }) {
  const [loaded, setLoaded] = useState(false);
  const [failed, setFailed] = useState(false);
  return (
    <span
      aria-hidden
      className={cn(
        "relative inline-flex shrink-0 items-center justify-center overflow-hidden rounded-lg border border-[var(--border-subtle)] bg-[var(--bg-tertiary)] font-heading font-semibold select-none [container-type:size]",
        TINT_CLASS[lettermarkTint(app)],
        className,
      )}
    >
      {!loaded && <span className="text-[42cqh] leading-none">{lettermark(title)}</span>}
      {!failed && (
        <img
          src={`${env.apiBase}/api/marketplace/${encodeURIComponent(app)}/icon?v=4`}
          alt=""
          className={cn("absolute inset-0 h-full w-full object-contain p-[14%]", !loaded && "opacity-0")}
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
