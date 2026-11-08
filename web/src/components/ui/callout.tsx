import type { ReactNode } from "react";
import { AlertTriangle, CheckCircle2, Info, OctagonAlert } from "lucide-react";
import { cn } from "@/lib/utils";

export type CalloutTone = "info" | "neutral" | "success" | "warning" | "danger";

// Severity discipline: danger is only for destructive or failing things,
// warning for "needs attention soon", info for "worth knowing / not set up
// yet", neutral for plain context. Pick the lowest tone that's honest.
const TONES: Record<CalloutTone, { box: string; icon: string; Icon: typeof Info }> = {
  neutral: {
    box: "border-[var(--border-subtle)] bg-[var(--bg-secondary)]",
    icon: "text-[var(--text-tertiary)]",
    Icon: Info,
  },
  info: {
    box: "border-[var(--info)]/30 bg-[var(--info-subtle)]",
    icon: "text-[var(--info)]",
    Icon: Info,
  },
  success: {
    box: "border-[var(--success)]/30 bg-[var(--success-subtle)]",
    icon: "text-[var(--success)]",
    Icon: CheckCircle2,
  },
  warning: {
    box: "border-[var(--warning)]/35 bg-[var(--warning-subtle)]",
    icon: "text-[var(--warning)]",
    Icon: AlertTriangle,
  },
  danger: {
    box: "border-[var(--danger)]/35 bg-[var(--danger-subtle)]",
    icon: "text-[var(--danger)]",
    Icon: OctagonAlert,
  },
};

export function Callout({
  tone = "neutral",
  title,
  children,
  action,
  icon,
  role,
  className,
}: {
  tone?: CalloutTone;
  title?: ReactNode;
  children?: ReactNode;
  action?: ReactNode;
  // Override the tone's default icon; null hides it.
  icon?: ReactNode | null;
  role?: "status" | "alert";
  className?: string;
}) {
  const t = TONES[tone];
  return (
    <div
      role={role ?? (tone === "danger" ? "alert" : undefined)}
      className={cn("flex items-start gap-3 rounded-md border px-3.5 py-3 text-sm", t.box, className)}
    >
      {icon === undefined ? <t.Icon aria-hidden className={cn("mt-0.5 size-4 shrink-0", t.icon)} /> : icon}
      <div className="min-w-0 flex-1 text-[var(--text-secondary)]">
        {title && <div className="font-medium text-[var(--text-primary)]">{title}</div>}
        {children && <div className={cn(title ? "mt-0.5" : undefined)}>{children}</div>}
      </div>
      {action && <div className="shrink-0 self-center">{action}</div>}
    </div>
  );
}
