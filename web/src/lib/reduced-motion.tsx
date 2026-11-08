"use client";

import { MotionConfig } from "motion/react";
import type { ReactNode } from "react";

// Makes every `motion` animation honour the OS "reduce motion" setting
// (CSS animations are handled in globals.css).
export function ReducedMotion({ children }: { children: ReactNode }) {
  return <MotionConfig reducedMotion="user">{children}</MotionConfig>;
}
