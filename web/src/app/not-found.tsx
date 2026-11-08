import Link from "next/link";
import { Logo } from "@/components/shared/Logo";
import { Button } from "@/components/ui/button";

export default function NotFound() {
  return (
    <main className="flex min-h-dvh flex-col bg-[var(--bg-primary)] px-4">
      <header className="flex h-12 items-center">
        <Link href="/projects" aria-label="kuso — all projects" className="rounded-md p-1">
          <Logo />
        </Link>
      </header>
      <div className="flex flex-1 items-center justify-center pb-24">
        <div className="max-w-md text-center">
          <p className="text-sm font-medium tabular-nums text-[var(--text-tertiary)]">Error 404</p>
          <h1 className="mt-2 text-3xl font-semibold tracking-tight text-[var(--text-primary)]">
            This page doesn&apos;t exist
          </h1>
          <p className="mt-3 text-sm text-[var(--text-secondary)]">
            The link may be mistyped, or what it pointed to was renamed or deleted.
          </p>
          <div className="mt-6 flex flex-wrap items-center justify-center gap-2">
            <Button render={<Link href="/projects" />} nativeButton={false}>
              Go to projects
            </Button>
            <Button variant="secondary" render={<Link href="/settings" />} nativeButton={false}>
              Settings
            </Button>
          </div>
        </div>
      </div>
    </main>
  );
}
