import Link from "next/link";

export default function NotFound() {
  return (
    <div className="flex min-h-screen items-center justify-center bg-[var(--bg-secondary)]">
      <div className="text-center">
        <p className="font-mono text-xs uppercase tracking-widest text-[var(--text-tertiary)]">404</p>
        <h1 className="mt-2 text-section-heading">page not found</h1>
        <Link href="/" className="mt-4 inline-block text-sm text-[var(--text-secondary)] underline">
          back home
        </Link>
      </div>
    </div>
  );
}
