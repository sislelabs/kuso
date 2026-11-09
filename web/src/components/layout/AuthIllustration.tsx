import { AddonIcon } from "@/components/addon/AddonIcon";
import { RuntimeIcon } from "@/components/service/RuntimeIcon";
import { nodeCardClass, nodeRailClass } from "@/components/canvas/nodeStyle";
import { cn } from "@/lib/utils";

// A still life of the project canvas — two services wired to a database —
// drawn with the real node recipe so the sign-in page previews the
// product instead of decorating it. Purely decorative.
export function AuthIllustration({ className }: { className?: string }) {
  return (
    <div aria-hidden className={cn("relative h-[248px] w-[440px] select-none", className)}>
      <svg className="absolute inset-0 h-full w-full overflow-visible" fill="none">
        <path d="M200 69 H222 Q236 69 236 83 V110 Q236 124 250 124 H262" strokeWidth="1.5" strokeLinecap="round" style={{ stroke: "var(--edge-addon)" }} />
        <path d="M200 186 H222 Q236 186 236 172 V138 Q236 124 250 124 H262" strokeWidth="1.5" strokeLinecap="round" style={{ stroke: "var(--edge-addon)" }} />
        <path d="M100 122 V148" strokeWidth="1.5" strokeLinecap="round" style={{ stroke: "var(--edge-ref)" }} />
      </svg>
      <MiniService className="left-0 top-4" name="web" host="shop.example.com" meta="deployed 2m ago" runtime="nixpacks" />
      <MiniService className="left-0 top-[148px]" name="api" meta="3/3 ready" runtime="dockerfile" />
      <div className={cn(nodeCardClass, "absolute left-[262px] top-[94px] flex h-[60px] w-[178px] cursor-default items-center gap-2.5 border-[var(--border-default)] px-3")}>
        <span className={cn(nodeRailClass, "bg-[var(--success)]")} />
        <AddonIcon kind="postgres" className="h-5 w-5 shrink-0" />
        <div className="min-w-0">
          <p className="font-display text-sm font-semibold [font-stretch:100%] text-[var(--text-primary)]">db</p>
          <p className="flex items-center gap-1.5 text-xs text-[var(--text-tertiary)]">
            <span className="size-1.5 rounded-full bg-[var(--success)]" />
            <span className="text-[var(--text-secondary)]">Live</span> Postgres 17
          </p>
        </div>
      </div>
    </div>
  );
}

function MiniService({
  className,
  name,
  host,
  meta,
  runtime,
}: {
  className: string;
  name: string;
  host?: string;
  meta: string;
  runtime: string;
}) {
  return (
    <div className={cn(nodeCardClass, "absolute flex w-[200px] cursor-default flex-col gap-2 border-[var(--border-default)] p-3", className)}>
      <span className={cn(nodeRailClass, "bg-[var(--success)]")} />
      <div className="flex items-center gap-2">
        <RuntimeIcon runtime={runtime} />
        <span className="font-display text-sm font-semibold [font-stretch:100%] text-[var(--text-primary)]">{name}</span>
      </div>
      {host && (
        <span className="w-fit rounded-md border border-[var(--border-subtle)] bg-[var(--bg-tertiary)] px-2 py-0.5 font-mono text-xs text-[var(--text-secondary)]">
          {host}
        </span>
      )}
      <span className="flex items-center gap-1.5 border-t border-[var(--border-subtle)] pt-2 text-xs">
        <span className="size-2 rounded-full bg-[var(--success)]" />
        <span className="font-medium text-[var(--success)]">Live</span>
        <span className="text-[var(--text-tertiary)]">{meta}</span>
      </span>
    </div>
  );
}
