import Link from "next/link";
import { FolderX } from "lucide-react";

// ProjectNotFound replaces the generic load error when the API says the
// project doesn't exist: retrying can't help, so there's no Retry.
export function ProjectNotFound({ project }: { project: string }) {
  return (
    <div className="flex min-h-[60vh] items-center justify-center px-4 py-12">
      <div className="max-w-md text-center">
        <span className="mx-auto inline-flex h-12 w-12 items-center justify-center rounded-full bg-[var(--bg-tertiary)] text-[var(--text-tertiary)]">
          <FolderX className="h-5 w-5" aria-hidden />
        </span>
        <h1 className="mt-4 font-heading text-xl font-semibold tracking-tight text-[var(--text-primary)]">
          Project not found
        </h1>
        <p className="mt-2 text-sm text-[var(--text-secondary)]">
          There&apos;s no project called <span className="font-mono text-[var(--text-primary)]">{project}</span>, or
          you don&apos;t have access to it. It may have been renamed or deleted.
        </p>
        <Link
          href="/projects"
          className="mt-6 inline-flex h-9 items-center rounded-md border border-[var(--btn-primary-border)] bg-[var(--btn-primary-bg)] px-4 text-sm font-medium text-[var(--btn-primary-fg)] shadow-[var(--shadow-sm)] transition-colors hover:bg-[var(--btn-primary-bg-hover)]"
        >
          Back to projects
        </Link>
      </div>
    </div>
  );
}
