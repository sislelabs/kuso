"use client";

import { usePathname, useRouter } from "next/navigation";
import { useEffect } from "react";
import { Skeleton } from "@/components/ui/skeleton";
import { useSession, usePending } from "../hooks";

export function AuthGate({ children }: { children: React.ReactNode }) {
  const pathname = usePathname();
  const router = useRouter();
  const { data, isPending, isError } = useSession();
  const pending = usePending();

  useEffect(() => {
    if (isPending) return;
    if (data === null || isError) {
      const next = encodeURIComponent(pathname);
      router.replace(`/login?next=${next}`);
      return;
    }
    // Pending users are authenticated but have zero perms — funnel
    // them to a single "awaiting access" page so they don't bounce
    // off every guarded route. Skip the redirect when they're
    // already on the page.
    if (pending && pathname !== "/awaiting-access") {
      router.replace("/awaiting-access");
    }
  }, [data, isPending, isError, pathname, router, pending]);

  // Loading — session not yet resolved.
  if (isPending) return <AuthSkeleton />;

  // Unauthenticated → effect above redirects to /login. Render
  // nothing in the meantime so we don't flash the app shell.
  if (data === null || isError) return null;

  // Pending users have a session but zero perms. The effect above is
  // pushing them to /awaiting-access, but useEffect runs AFTER render
  // — if we let {children} mount here we'd flash the entire app shell
  // (sidebar, top-nav popovers, settings pages) for one paint. Block
  // synchronously instead. Exception: when they're already on the
  // awaiting-access page, render so it can show.
  if (pending && pathname !== "/awaiting-access") {
    return <AuthSkeleton />;
  }

  return <>{children}</>;
}

// AuthSkeleton renders a sidebar+content placeholder while the auth
// gate is in flight. Same shape regardless of which gate state we're
// in (loading, redirecting unauth, redirecting pending) so the user
// sees a consistent "still figuring it out" frame instead of a blink.
function AuthSkeleton() {
  return (
    <div className="flex h-screen">
      <div className="hidden w-[260px] border-r border-[var(--border-subtle)] bg-[var(--bg-secondary)] lg:block">
        <div className="space-y-3 p-4">
          <Skeleton className="h-8 w-32" />
          <Skeleton className="h-6 w-full" />
          <Skeleton className="h-6 w-full" />
          <Skeleton className="h-6 w-3/4" />
        </div>
      </div>
      <div className="flex-1 p-8">
        <Skeleton className="mb-4 h-8 w-48" />
        <Skeleton className="h-32 w-full" />
      </div>
    </div>
  );
}
