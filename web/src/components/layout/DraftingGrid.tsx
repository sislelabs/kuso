import { useId } from "react";
import { cn } from "@/lib/utils";

// The canvas ground as a standalone backdrop: a dot on every 24px point
// and a cross every fifth, in the same tokens React Flow's Background
// uses on the project canvas. Sign-in, welcome and empty states sit on it
// so the first screens already look like the place you'll work in.
export function DraftingGrid({ className, fade = false }: { className?: string; fade?: boolean }) {
  const id = useId().replace(/[^a-zA-Z0-9_-]/g, "");
  return (
    <svg
      aria-hidden
      className={cn(
        "pointer-events-none absolute inset-0 h-full w-full",
        fade && "[mask-image:radial-gradient(ellipse_at_center,black_35%,transparent_80%)]",
        className,
      )}
    >
      <defs>
        <pattern id={`${id}-minor`} width="24" height="24" patternUnits="userSpaceOnUse">
          <circle cx="12" cy="12" r="0.9" style={{ fill: "var(--grid-minor)" }} />
        </pattern>
        <pattern id={`${id}-major`} width="120" height="120" patternUnits="userSpaceOnUse">
          <path d="M60 55.5v9M55.5 60h9" strokeWidth="1" style={{ stroke: "var(--grid-major)" }} />
        </pattern>
      </defs>
      <rect width="100%" height="100%" fill={`url(#${id}-minor)`} />
      <rect width="100%" height="100%" fill={`url(#${id}-major)`} />
    </svg>
  );
}
