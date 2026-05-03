import { cn } from "@/lib/utils";

// Logo renders the kuso pinwheel mark — four curved blades (navy /
// orange / navy / sage) around a small white center pin. The SVG is
// inlined so it inherits CSS sizing without a network round-trip and
// works with currentColor where useful (the pin stroke). Source of
// truth lives at web/public/logo.svg too, so static-export contexts
// (favicon, OG image) reference the file.
export function Logo({
  showText = true,
  className,
}: {
  showText?: boolean;
  className?: string;
}) {
  return (
    <span className={cn("inline-flex items-center gap-2", className)}>
      <KusoMark className="h-6 w-6 shrink-0" />
      {showText && (
        <span className="font-heading text-base font-semibold tracking-tight text-[var(--text-primary)]">
          kuso
        </span>
      )}
    </span>
  );
}

export function KusoMark({ className }: { className?: string }) {
  return (
    <svg
      viewBox="0 0 340 340"
      xmlns="http://www.w3.org/2000/svg"
      role="img"
      aria-label="kuso"
      className={className}
    >
      <title>kuso</title>
      <g transform="translate(170 170)">
        {/* Top-right: navy */}
        <g>
          <path
            d="M 0 0 C 90 -8, 148 -52, 145 -148 C 60 -132, 14 -88, 0 0 Z"
            fill="#40476D"
          />
          <path
            d="M 0 0 L 145 -148 C 60 -132, 14 -88, 0 0 Z"
            fill="#2D3454"
            opacity="0.45"
          />
        </g>
        {/* Bottom-right: orange */}
        <g transform="rotate(90)">
          <path
            d="M 0 0 C 90 -8, 148 -52, 145 -148 C 60 -132, 14 -88, 0 0 Z"
            fill="#EB6534"
          />
          <path
            d="M 0 0 L 145 -148 C 60 -132, 14 -88, 0 0 Z"
            fill="#B8421A"
            opacity="0.4"
          />
        </g>
        {/* Bottom-left: navy */}
        <g transform="rotate(180)">
          <path
            d="M 0 0 C 90 -8, 148 -52, 145 -148 C 60 -132, 14 -88, 0 0 Z"
            fill="#40476D"
          />
          <path
            d="M 0 0 L 145 -148 C 60 -132, 14 -88, 0 0 Z"
            fill="#2D3454"
            opacity="0.45"
          />
        </g>
        {/* Top-left: sage */}
        <g transform="rotate(270)">
          <path
            d="M 0 0 C 90 -8, 148 -52, 145 -148 C 60 -132, 14 -88, 0 0 Z"
            fill="#ACBEA3"
          />
          <path
            d="M 0 0 L 145 -148 C 60 -132, 14 -88, 0 0 Z"
            fill="#7E957E"
            opacity="0.45"
          />
        </g>
        {/* Center pin */}
        <circle r="14" fill="#FFFFFF" />
        <circle r="14" fill="none" stroke="#40476D" strokeWidth="3" />
        <circle r="4" fill="#40476D" />
      </g>
    </svg>
  );
}
