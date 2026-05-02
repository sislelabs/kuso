import { cn } from "@/lib/utils";
import type { ReactNode } from "react";

export function EmptyState({
  icon,
  title,
  description,
  action,
  className,
}: {
  icon?: ReactNode;
  title: string;
  description?: string;
  action?: ReactNode;
  className?: string;
}) {
  return (
    <div
      className={cn(
        "flex flex-col items-center justify-center rounded-2xl border border-dashed border-[var(--border-subtle)] bg-[var(--bg-secondary)] p-12 text-center",
        className
      )}
    >
      {icon && (
        <div className="mb-3 flex h-12 w-12 items-center justify-center rounded-full bg-[var(--bg-tertiary)] text-[var(--text-tertiary)]">
          {icon}
        </div>
      )}
      <h3 className="font-heading text-lg font-semibold tracking-tight text-[var(--text-primary)]">
        {title}
      </h3>
      {description && (
        <p className="mt-1 max-w-md text-sm text-[var(--text-secondary)]">
          {description}
        </p>
      )}
      {action && <div className="mt-4">{action}</div>}
    </div>
  );
}
