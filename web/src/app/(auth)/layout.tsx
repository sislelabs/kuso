import { Logo } from "@/components/shared/Logo";
import { ThemeToggle } from "@/components/shared/ThemeToggle";
import { DraftingGrid } from "@/components/layout/DraftingGrid";
import { AuthIllustration } from "@/components/layout/AuthIllustration";

// Sign-in, invite and GitHub-callback pages. Wide screens get a drafting
// table on the left showing what's behind the door (a small project
// canvas); the form column stays one quiet column on the page ground.
export default function AuthLayout({ children }: { children: React.ReactNode }) {
  return (
    <div className="grid min-h-dvh bg-[var(--bg-primary)] lg:grid-cols-[minmax(0,1.15fr)_minmax(0,1fr)]">
      <aside className="relative hidden overflow-hidden border-r border-[var(--border-subtle)] bg-[var(--canvas-bg)] lg:flex lg:flex-col">
        <DraftingGrid />
        <div className="relative flex h-14 items-center px-8">
          <Logo />
        </div>
        <div className="relative flex flex-1 items-center justify-center px-8">
          <AuthIllustration className="origin-center scale-[1.2] xl:scale-[1.35]" />
        </div>
        <div className="relative max-w-lg px-10 pb-12">
          <p className="font-display text-[2rem] leading-[1.1] font-bold text-[var(--text-primary)]">
            Your apps, on your own cluster.
          </p>
          <p className="mt-3 max-w-md text-[15px] leading-relaxed text-[var(--text-secondary)]">
            Connect a repo and every push builds, deploys and stays up, with databases, logs and
            previews next to it.
          </p>
        </div>
      </aside>
      <div className="flex min-h-dvh flex-col">
        <header className="flex h-14 items-center justify-between px-4 sm:px-6">
          <Logo className="lg:invisible" />
          <ThemeToggle />
        </header>
        <main className="flex flex-1 items-start justify-center px-4 pt-[8vh] pb-16 sm:items-center sm:pt-0">
          <div className="w-full max-w-[22rem]">{children}</div>
        </main>
      </div>
    </div>
  );
}
