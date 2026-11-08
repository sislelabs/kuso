import type { ReactNode } from "react";
import { PageContainer } from "@/components/shared/PageContainer";
import { PageHeader } from "@/components/shared/PageHeader";
import { cn } from "@/lib/utils";

// SettingsPage is the one header + width every /settings/* page uses:
// the shared PageHeader inside a PageContainer. The settings layout
// already pads the column and owns the sidebar, so the container drops
// its own gutters and centring here.
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
    <PageContainer
      width="narrow"
      className={cn("mx-0 max-w-4xl px-0 pt-5 pb-16 sm:px-0 sm:pt-5 lg:px-0 lg:pt-8", className)}
    >
      <PageHeader title={title} description={description} actions={actions} back={back} className="mb-6 sm:mb-6" />
      <div className="space-y-6">{children}</div>
    </PageContainer>
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
