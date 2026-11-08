"use client";

import { useState } from "react";
import { Package } from "lucide-react";
import { env } from "@/lib/env";
import { cn } from "@/lib/utils";

const CATEGORY_LABELS: Record<string, string> = {
  "dev-tools": "Developer tools",
};

export function categoryLabel(c: string): string {
  if (CATEGORY_LABELS[c]) return CATEGORY_LABELS[c];
  const s = c.replace(/-/g, " ");
  return s.charAt(0).toUpperCase() + s.slice(1);
}

// AppIcon renders the catalog's SVG brand mark, falling back to a neutral
// glyph so a missing icon never leaves a hole in the card.
export function AppIcon({ app, className }: { app: string; className?: string }) {
  const [failed, setFailed] = useState(false);
  return (
    <span
      aria-hidden
      className={cn(
        "inline-flex shrink-0 items-center justify-center overflow-hidden rounded-lg bg-[var(--bg-tertiary)] text-[var(--text-tertiary)]",
        className,
      )}
    >
      {failed ? (
        <Package className="h-1/2 w-1/2" />
      ) : (
        <img
          src={`${env.apiBase}/api/marketplace/${encodeURIComponent(app)}/icon?v=3`}
          alt=""
          className="h-full w-full"
          onError={() => setFailed(true)}
        />
      )}
    </span>
  );
}
