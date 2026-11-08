"use client";

import { useLayoutEffect, useRef, useState } from "react";
import { cn } from "@/lib/utils";
import { isToggleKey } from "./actor";

// One line, ellipsised. When the text doesn't fit, the line becomes a
// toggle (click, Enter or Space) that expands it inline; text that fits
// stays plain so it isn't a pointless tab stop.
export function ExpandableText({ text, className }: { text: string; className?: string }) {
  const ref = useRef<HTMLSpanElement>(null);
  const [expanded, setExpanded] = useState(false);
  const [overflowing, setOverflowing] = useState(false);

  useLayoutEffect(() => {
    const el = ref.current;
    if (!el || expanded) return;
    const check = () => setOverflowing(el.scrollWidth > el.clientWidth + 1);
    check();
    if (typeof ResizeObserver === "undefined") return;
    const ro = new ResizeObserver(check);
    ro.observe(el);
    return () => ro.disconnect();
  }, [text, expanded]);

  const toggleable = overflowing || expanded;
  const toggle = () => setExpanded((v) => !v);

  return (
    <span
      ref={ref}
      role={toggleable ? "button" : undefined}
      tabIndex={toggleable ? 0 : undefined}
      aria-expanded={toggleable ? expanded : undefined}
      title={toggleable && !expanded ? "Show full message" : undefined}
      onClick={toggleable ? toggle : undefined}
      onKeyDown={
        toggleable
          ? (e) => {
              if (!isToggleKey(e.key)) return;
              e.preventDefault();
              toggle();
            }
          : undefined
      }
      className={cn(
        "block min-w-0",
        expanded ? "whitespace-pre-wrap break-words" : "truncate",
        toggleable &&
          "cursor-pointer rounded-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--ring)]",
        className,
      )}
    >
      {text}
    </span>
  );
}
