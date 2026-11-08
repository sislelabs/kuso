import Link from "next/link";
import { Fragment, type ReactNode } from "react";
import { ArrowLeft, ChevronRight } from "lucide-react";
import { cn } from "@/lib/utils";

export type Crumb = { label: string; href?: string };

// The one page header: optional back link or breadcrumb, title,
// description, right-aligned actions. Actions wrap under the title on
// narrow screens instead of squeezing it.
//
//   <PageHeader title="Projects" description="…" actions={<Button>New project</Button>} />
//   <PageHeader back={{ href: "/settings", label: "Settings" }} title="Users" />
export function PageHeader({
  title,
  description,
  actions,
  back,
  breadcrumb,
  meta,
  className,
}: {
  title: ReactNode;
  description?: ReactNode;
  actions?: ReactNode;
  back?: { href: string; label: string };
  breadcrumb?: Crumb[];
  /** Small status row under the title (badges, timestamps). */
  meta?: ReactNode;
  className?: string;
}) {
  return (
    <header data-slot="page-header" className={cn("mb-6 sm:mb-8", className)}>
      {back && (
        <Link
          href={back.href}
          className="mb-3 inline-flex items-center gap-1 rounded-sm text-[13px] text-[var(--text-tertiary)] hover:text-[var(--text-primary)]"
        >
          <ArrowLeft aria-hidden className="size-3.5" />
          {back.label}
        </Link>
      )}
      {breadcrumb && breadcrumb.length > 0 && (
        <nav aria-label="Breadcrumb" className="mb-3">
          <ol className="flex flex-wrap items-center gap-1 text-[13px] text-[var(--text-tertiary)]">
            {breadcrumb.map((c, i) => (
              <Fragment key={`${c.label}-${i}`}>
                {i > 0 && <ChevronRight aria-hidden className="size-3.5 opacity-60" />}
                <li>
                  {c.href ? (
                    <Link href={c.href} className="rounded-sm hover:text-[var(--text-primary)]">
                      {c.label}
                    </Link>
                  ) : (
                    <span aria-current="page" className="text-[var(--text-secondary)]">
                      {c.label}
                    </span>
                  )}
                </li>
              </Fragment>
            ))}
          </ol>
        </nav>
      )}
      <div className="flex flex-col gap-4 sm:flex-row sm:items-start sm:justify-between">
        <div className="min-w-0">
          <h1 className="text-title break-words">{title}</h1>
          {description && (
            <p className="mt-1.5 max-w-2xl text-sm text-[var(--text-secondary)]">{description}</p>
          )}
          {meta && <div className="mt-2.5 flex flex-wrap items-center gap-2">{meta}</div>}
        </div>
        {actions && <div className="flex shrink-0 flex-wrap items-center gap-2">{actions}</div>}
      </div>
    </header>
  );
}
