import { Fragment } from "react";
import { SERVICE_STATUS, TONE_CLASSES } from "@/lib/status";
import { cn } from "@/lib/utils";
import { projectStatusParts, type ProjectCardModel } from "./projectCardModel";

// One status line per card ("1 of 3 live · 1 failing"). Colours come from
// the shared status map, so the card, canvas and palette agree on what
// "Deploying" or "Failing" looks like.
export function ProjectStatusBadge({ model, className }: { model: ProjectCardModel; className?: string }) {
  const parts = projectStatusParts(model.counts, model.serviceCount);
  const sentence = parts.map((p) => p.text).join(" · ");
  const hollow = model.status === "pending" || model.status === "empty";
  return (
    <span
      className={cn("inline-flex min-w-0 items-center gap-1.5 text-xs", className)}
      title={model.detail ?? undefined}
    >
      <span
        aria-hidden
        className={cn(
          "h-2 w-2 shrink-0 rounded-full",
          hollow ? "border border-[var(--text-tertiary)] bg-transparent" : TONE_CLASSES[model.tone].dot,
          model.status === "building" && "motion-safe:animate-pulse",
        )}
      />
      <span className="sr-only">Status: {sentence}</span>
      <span aria-hidden className="truncate">
        {parts.map((p, i) => {
          const meta = p.status ? SERVICE_STATUS[p.status] : undefined;
          const quiet = !meta || meta.tone === "neutral" || meta.tone === "info";
          return (
            <Fragment key={p.text}>
              {i > 0 && <span className="text-[var(--text-tertiary)]"> · </span>}
              <span
                className={cn(
                  i === 0 && "font-medium",
                  quiet ? (i === 0 ? "text-[var(--text-primary)]" : "text-[var(--text-secondary)]") : TONE_CLASSES[meta.tone].text,
                )}
              >
                {p.text}
              </span>
            </Fragment>
          );
        })}
      </span>
    </span>
  );
}
