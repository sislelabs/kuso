"use client";

import { memo } from "react";
import type { CSSProperties } from "react";
import { parseAnsi, splitMatches } from "./logParse";
import type { AnsiStyle } from "./logParse";
import { cn } from "@/lib/utils";

// Light mode gets the darker step so coloured output stays readable on
// the light surface; dark mode the lighter one.
const FG: Record<string, string> = {
  black: "text-zinc-800 dark:text-zinc-500",
  red: "text-red-700 dark:text-red-400",
  green: "text-green-700 dark:text-green-400",
  yellow: "text-amber-700 dark:text-yellow-300",
  blue: "text-blue-700 dark:text-blue-400",
  magenta: "text-fuchsia-700 dark:text-fuchsia-400",
  cyan: "text-cyan-700 dark:text-cyan-400",
  white: "text-zinc-600 dark:text-zinc-200",
  "bright-black": "text-zinc-500 dark:text-zinc-400",
  "bright-red": "text-red-600 dark:text-red-300",
  "bright-green": "text-green-600 dark:text-green-300",
  "bright-yellow": "text-amber-600 dark:text-yellow-200",
  "bright-blue": "text-blue-600 dark:text-blue-300",
  "bright-magenta": "text-fuchsia-600 dark:text-fuchsia-300",
  "bright-cyan": "text-cyan-600 dark:text-cyan-300",
  "bright-white": "text-zinc-900 dark:text-white",
};

const BG: Record<string, string> = {
  black: "bg-zinc-500/20",
  red: "bg-red-500/20",
  green: "bg-green-500/20",
  yellow: "bg-yellow-500/20",
  blue: "bg-blue-500/20",
  magenta: "bg-fuchsia-500/20",
  cyan: "bg-cyan-500/20",
  white: "bg-zinc-400/20",
};

function styleOf(s: AnsiStyle): { className: string; style?: CSSProperties } {
  const fgRgb = s.fg?.startsWith("rgb") ? s.fg : undefined;
  const bgRgb = s.bg?.startsWith("rgb") ? s.bg : undefined;
  const bgName = s.bg && !bgRgb ? s.bg.replace(/^bright-/, "") : undefined;
  return {
    className: cn(
      s.fg && !fgRgb && FG[s.fg],
      bgName && BG[bgName],
      s.bold && "font-semibold",
      s.dim && "opacity-70",
      s.italic && "italic",
      s.underline && "underline",
    ),
    style: fgRgb || bgRgb ? { color: fgRgb, backgroundColor: bgRgb } : undefined,
  };
}

// AnsiText renders a log line with its ANSI colours and the search
// matches marked. Escape sequences never reach the DOM as text.
export const AnsiText = memo(function AnsiText({ text, highlight }: { text: string; highlight?: RegExp | null }) {
  const segs = parseAnsi(text);
  return (
    <>
      {segs.map((seg, i) => {
        const { className, style } = styleOf(seg.style);
        const parts = splitMatches(seg.text, highlight ?? null);
        return (
          <span key={i} className={className || undefined} style={style}>
            {parts.map((p, j) =>
              p.match ? (
                <mark
                  key={j}
                  className="rounded-sm bg-[var(--building-subtle)] px-px text-[var(--text-primary)] ring-1 ring-[var(--building)]/40"
                >
                  {p.text}
                </mark>
              ) : (
                p.text
              ),
            )}
          </span>
        );
      })}
    </>
  );
});
