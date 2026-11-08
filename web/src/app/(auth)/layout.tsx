import { Logo } from "@/components/shared/Logo";
import { ThemeToggle } from "@/components/shared/ThemeToggle";

// Sign-in, invite and GitHub-callback pages: no card chrome, one quiet
// column on the page ground so the form is the only thing to look at.
export default function AuthLayout({ children }: { children: React.ReactNode }) {
  return (
    <div className="flex min-h-dvh flex-col bg-[var(--bg-primary)]">
      <header className="flex h-14 items-center justify-between px-4 sm:px-6">
        <Logo />
        <ThemeToggle />
      </header>
      <main className="flex flex-1 items-start justify-center px-4 pt-[8vh] pb-16 sm:items-center sm:pt-0">
        <div className="w-full max-w-[22rem]">{children}</div>
      </main>
    </div>
  );
}
