"use client";

import { MotionConfig } from "motion/react";
import type { ReactNode } from "react";

// Makes every `motion` animation honour the OS "reduce motion" setting
// (CSS animations are handled in globals.css).
export function ReducedMotion({ children }: { children: ReactNode }) {
  return <MotionConfig reducedMotion="user">{children}</MotionConfig>;
}

// For scrollIntoView/scrollTo: an explicit behavior: "smooth" ignores the
// CSS scroll-behavior override, so script-driven scrolls ask here.
export function smoothScroll(): ScrollBehavior {
  return typeof window !== "undefined" && window.matchMedia?.("(prefers-reduced-motion: reduce)").matches
    ? "auto"
    : "smooth";
}
