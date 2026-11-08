import type { ReactNode, SelectHTMLAttributes } from "react";
import { forwardRef } from "react";
import { AlertTriangle, ChevronDown, Info, XCircle } from "lucide-react";
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

// SettingsNotice replaces the old 10px mono banners: sans, readable,
// tone-coloured with an icon.
export function SettingsNotice({
  tone = "info",
  title,
  children,
  action,
  className,
}: {
  tone?: "info" | "warning" | "error";
  title?: ReactNode;
  children?: ReactNode;
  action?: ReactNode;
  className?: string;
}) {
  const Icon = tone === "error" ? XCircle : tone === "warning" ? AlertTriangle : Info;
  const color =
    tone === "error"
      ? "border-[var(--error)]/35 bg-[var(--error-subtle)] [&_svg]:text-[var(--error)]"
      : tone === "warning"
        ? "border-[var(--warning)]/35 bg-[var(--warning-subtle)] [&_svg]:text-[var(--warning)]"
        : "border-[var(--border-subtle)] bg-[var(--bg-secondary)] [&_svg]:text-[var(--text-tertiary)]";
  return (
    <div className={cn("flex items-start gap-3 rounded-md border px-4 py-3 text-sm", color, className)}>
      <Icon className="mt-0.5 h-4 w-4 shrink-0" />
      <div className="min-w-0 flex-1 text-[var(--text-secondary)]">
        {title && <div className="font-medium text-[var(--text-primary)]">{title}</div>}
        {children && <div className={cn(title && "mt-0.5")}>{children}</div>}
      </div>
      {action && <div className="shrink-0">{action}</div>}
    </div>
  );
}

// One look for every native <select> in settings until the shared
// Select primitive lands; the integrator swaps SettingsSelect over.
export const settingsSelectClass =
  "h-9 w-full min-w-0 appearance-none rounded-sm border border-border bg-input pl-3 pr-8 text-sm text-[var(--text-primary)] outline-none transition-colors focus-visible:border-ring focus-visible:ring-3 focus-visible:ring-ring/50 disabled:cursor-not-allowed disabled:opacity-50";

export const SettingsSelect = forwardRef<
  HTMLSelectElement,
  SelectHTMLAttributes<HTMLSelectElement> & { wrapperClassName?: string }
>(function SettingsSelect({ className, wrapperClassName, children, ...props }, ref) {
  return (
    <div className={cn("relative w-full", wrapperClassName)}>
      <select ref={ref} className={cn(settingsSelectClass, className)} {...props}>
        {children}
      </select>
      <ChevronDown className="pointer-events-none absolute right-2.5 top-1/2 h-4 w-4 -translate-y-1/2 text-[var(--text-tertiary)]" />
    </div>
  );
});
