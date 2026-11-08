import type { ReactNode } from "react";
import { Callout, type CalloutTone } from "@/components/ui/callout";
import { cn } from "@/lib/utils";

// SettingsSection is a Vercel-style settings card: title + description
// up top, fields in the body, an optional footer bar holding the save
// action and a short hint on the left.
export function SettingsSection({
  title,
  description,
  actions,
  footer,
  footerHint,
  tone = "default",
  flush = false,
  id,
  children,
  className,
}: {
  title?: ReactNode;
  description?: ReactNode;
  actions?: ReactNode;
  footer?: ReactNode;
  footerHint?: ReactNode;
  tone?: "default" | "danger";
  // flush drops the body padding for full-bleed tables/lists.
  flush?: boolean;
  id?: string;
  children?: ReactNode;
  className?: string;
}) {
  const danger = tone === "danger";
  return (
    <section
      id={id}
      className={cn(
        "overflow-hidden rounded-md border bg-[var(--bg-secondary)]",
        danger ? "border-[var(--error)]/40" : "border-[var(--border-subtle)]",
        className,
      )}
    >
      {(title || description || actions) && (
        <header className="flex flex-col gap-3 px-4 pt-4 sm:flex-row sm:items-start sm:justify-between sm:px-5 sm:pt-5">
          <div className="min-w-0">
            {title && (
              <h2
                className={cn(
                  "text-base font-semibold tracking-tight",
                  danger ? "text-[var(--error)]" : "text-[var(--text-primary)]",
                )}
              >
                {title}
              </h2>
            )}
            {description && (
              <div className="mt-1 text-sm leading-relaxed text-[var(--text-secondary)]">{description}</div>
            )}
          </div>
          {actions && <div className="flex shrink-0 flex-wrap items-center gap-2">{actions}</div>}
        </header>
      )}
      {children !== undefined && children !== null && (
        <div className={cn(flush ? "mt-4 border-t border-[var(--border-subtle)]" : "space-y-5 px-4 py-4 sm:px-5 sm:py-5")}>
          {children}
        </div>
      )}
      {(footer || footerHint) && (
        <footer
          className={cn(
            "flex flex-col gap-2 border-t px-4 py-3 sm:flex-row sm:items-center sm:justify-between sm:px-5",
            danger
              ? "border-[var(--error)]/30 bg-[var(--error)]/5"
              : "border-[var(--border-subtle)] bg-[var(--bg-tertiary)]/40",
          )}
        >
          <div className="min-w-0 text-xs leading-relaxed text-[var(--text-secondary)]">{footerHint}</div>
          {footer && <div className="flex shrink-0 flex-wrap items-center justify-end gap-2">{footer}</div>}
        </footer>
      )}
    </section>
  );
}

// SettingsField stacks label + hint above a full-width control.
export function SettingsField({
  label,
  hint,
  htmlFor,
  error,
  aside,
  children,
  className,
}: {
  label: ReactNode;
  hint?: ReactNode;
  htmlFor?: string;
  error?: ReactNode;
  // aside renders right of the label (a toggle, a "reset" link).
  aside?: ReactNode;
  children?: ReactNode;
  className?: string;
}) {
  return (
    <div className={cn("space-y-2", className)}>
      <div className="flex items-start justify-between gap-3">
        <div className="min-w-0">
          <label htmlFor={htmlFor} className="block text-sm font-medium text-[var(--text-primary)]">
            {label}
          </label>
          {hint && <div className="mt-0.5 text-xs leading-relaxed text-[var(--text-secondary)]">{hint}</div>}
        </div>
        {aside && <div className="shrink-0">{aside}</div>}
      </div>
      {children}
      {error && <p className="text-xs text-[var(--error)]">{error}</p>}
    </div>
  );
}

// SettingsNotice is the settings pages' name for the shared Callout.
// "error" is kept as an alias of "danger" for existing call sites.
export function SettingsNotice({
  tone = "neutral",
  title,
  children,
  action,
  className,
}: {
  tone?: CalloutTone | "error";
  title?: ReactNode;
  children?: ReactNode;
  action?: ReactNode;
  className?: string;
}) {
  return (
    <Callout tone={tone === "error" ? "danger" : tone} title={title} action={action} className={className}>
      {children}
    </Callout>
  );
}

// Settings pages' select: the shared ui Select, kept under this name so
// the settings call sites read consistently.
export { Select as SettingsSelect } from "@/components/ui/select";
