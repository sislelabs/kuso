import Link from "next/link";
import type { ReactNode } from "react";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";

export type StateAction = {
  label: string;
  href?: string;
  onClick?: () => void;
  icon?: ReactNode;
};

// EmptyState: an empty screen is an invitation to act. Say what goes
// here and offer the next step.
//
//   <EmptyState icon={<Boxes />} title="No services yet"
//     description="Deploy from a GitHub repo, a template, or add a database."
//     primaryAction={{ label: "Add service", onClick: … }}
//     secondaryAction={{ label: "Browse templates", href: "/marketplace" }} />
//
// `action` (any node) is kept for existing call sites.
export function EmptyState({
  icon,
  title,
  description,
  action,
  primaryAction,
  secondaryAction,
  size = "md",
  variant = "dashed",
  className,
}: {
  icon?: ReactNode;
  title: string;
  description?: ReactNode;
  action?: ReactNode;
  primaryAction?: StateAction;
  secondaryAction?: StateAction;
  size?: "sm" | "md";
  /** dashed = an area waiting to be filled; plain = no frame (inside a card). */
  variant?: "dashed" | "plain";
  className?: string;
}) {
  const hasActions = !!(action || primaryAction || secondaryAction);
  return (
    <div
      data-slot="empty-state"
      className={cn(
        "flex flex-col items-center justify-center text-center",
        variant === "dashed" && "rounded-xl border border-dashed border-[var(--border-default)] bg-[var(--bg-secondary)]/60",
        size === "sm" ? "px-4 py-8" : "px-6 py-12",
        className,
      )}
    >
      {icon && (
        <div
          aria-hidden
          className={cn(
            "mb-4 flex items-center justify-center rounded-xl border border-[var(--border-default)] bg-[var(--bg-elevated)] text-[var(--accent)] shadow-node [&_svg]:size-5",
            size === "sm" ? "size-9" : "size-11",
          )}
        >
          {icon}
        </div>
      )}
      <h3 className={cn("font-display font-semibold text-[var(--text-primary)]", size === "sm" ? "text-sm" : "text-base")}>
        {title}
      </h3>
      {description && <p className="mt-1.5 max-w-md text-sm text-[var(--text-secondary)]">{description}</p>}
      {hasActions && (
        <div className="mt-5 flex flex-wrap items-center justify-center gap-2">
          {primaryAction && <ActionButton action={primaryAction} variant="primary" />}
          {secondaryAction && <ActionButton action={secondaryAction} variant="secondary" />}
          {action}
        </div>
      )}
    </div>
  );
}

export function ActionButton({ action, variant }: { action: StateAction; variant: "primary" | "secondary" }) {
  if (action.href) {
    return (
      <Button variant={variant} size="sm" render={<Link href={action.href} />} nativeButton={false}>
        {action.icon}
        {action.label}
      </Button>
    );
  }
  return (
    <Button variant={variant} size="sm" onClick={action.onClick}>
      {action.icon}
      {action.label}
    </Button>
  );
}
