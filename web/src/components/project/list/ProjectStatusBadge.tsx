import { cn } from "@/lib/utils";
import type { ProjectCardModel, ProjectStatus } from "./projectCardModel";

export const STATUS_META: Record<ProjectStatus, { label: string; dot: string; text: string }> = {
  live: { label: "Live", dot: "bg-[var(--success)]", text: "text-[var(--success)]" },
  failing: { label: "Failing", dot: "bg-[var(--error)]", text: "text-[var(--error)]" },
  building: { label: "Deploying", dot: "bg-[var(--building)] motion-safe:animate-pulse", text: "text-[var(--building)]" },
  sleeping: { label: "Sleeping", dot: "bg-[var(--info)]", text: "text-[var(--info)]" },
  stopped: { label: "Stopped", dot: "bg-[var(--text-tertiary)]", text: "text-[var(--text-secondary)]" },
  pending: { label: "Not deployed", dot: "border border-[var(--text-tertiary)] bg-transparent", text: "text-[var(--text-secondary)]" },
  empty: { label: "No services", dot: "border border-[var(--text-tertiary)] bg-transparent", text: "text-[var(--text-secondary)]" },
};

// statusDetail is the secondary text after the label, e.g. "3/3 ready".
function statusDetail(m: ProjectCardModel): string | null {
  switch (m.status) {
    case "live":
    case "building":
      return m.readyOf > 0 ? `${m.liveCount}/${m.readyOf} ready` : null;
    case "failing":
      return `${m.failingCount} of ${m.serviceCount} service${m.serviceCount === 1 ? "" : "s"}`;
    case "stopped":
      return `${m.serviceCount} service${m.serviceCount === 1 ? "" : "s"}`;
    default:
      return null;
  }
}

export function ProjectStatusBadge({ model, className }: { model: ProjectCardModel; className?: string }) {
  const meta = STATUS_META[model.status];
  const detail = statusDetail(model);
  const partialStop = model.status !== "stopped" && model.stoppedCount > 0;
  const sentence = [meta.label, detail, partialStop ? `${model.stoppedCount} stopped` : null]
    .filter(Boolean)
    .join(", ");
  return (
    <span
      className={cn("inline-flex min-w-0 items-center gap-1.5 text-xs", className)}
      title={model.detail ?? undefined}
    >
      <span aria-hidden className={cn("h-2 w-2 shrink-0 rounded-full", meta.dot)} />
      <span className="sr-only">Status: {sentence}</span>
      <span aria-hidden className={cn("font-medium", meta.text)}>
        {meta.label}
      </span>
      {detail && (
        <span aria-hidden className="truncate text-[var(--text-secondary)]">
          · {detail}
        </span>
      )}
      {partialStop && (
        <span aria-hidden className="truncate text-[var(--text-tertiary)]">
          · {model.stoppedCount} stopped
        </span>
      )}
    </span>
  );
}
