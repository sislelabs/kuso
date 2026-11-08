import Link from "next/link";
import type { ReactNode } from "react";
import { ChevronLeft } from "lucide-react";
import { cn } from "@/lib/utils";

// SettingsPage is the one header + width every /settings/* page uses.
// Kept local to settings so the integrator can later point it at the
// shared PageHeader/PageContainer without touching each page.
export function SettingsPage({
  title,
  description,
  actions,
  back,
  children,
  className,
}: {
  title: ReactNode;
  description?: ReactNode;
  actions?: ReactNode;
  back?: { href: string; label: string };
  children?: ReactNode;
  className?: string;
}) {
  return (
    <div className={cn("w-full max-w-4xl pb-16 pt-5 lg:pt-8", className)}>
      {back && (
        <Link
          href={back.href}
          className="mb-3 inline-flex items-center gap-1 text-sm text-[var(--text-secondary)] hover:text-[var(--text-primary)]"
        >
          <ChevronLeft className="h-4 w-4" />
          {back.label}
        </Link>
      )}
      <header className="mb-6 flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between">
        <div className="min-w-0">
          <h1 className="font-heading text-2xl font-semibold tracking-tight text-[var(--text-primary)]">
            {title}
          </h1>
          {description && (
            <p className="mt-1 max-w-2xl text-sm leading-relaxed text-[var(--text-secondary)]">
              {description}
            </p>
          )}
        </div>
        {actions && <div className="flex shrink-0 flex-wrap items-center gap-2">{actions}</div>}
      </header>
      <div className="space-y-6">{children}</div>
    </div>
  );
}

// SettingsPageSkeleton keeps the loading state on the same width as the
// loaded page so content doesn't jump sideways when data lands.
export function SettingsPageLoading({ title, rows = 2 }: { title: ReactNode; rows?: number }) {
  return (
    <SettingsPage title={title}>
      {Array.from({ length: rows }, (_, i) => (
        <div
          key={i}
          className="h-40 animate-pulse rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)]"
        />
      ))}
    </SettingsPage>
  );
}
