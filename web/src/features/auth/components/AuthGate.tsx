"use client";

import { usePathname, useRouter } from "next/navigation";
import { useEffect } from "react";
import { Skeleton } from "@/components/ui/skeleton";
import { useSession } from "../hooks";

export function AuthGate({ children }: { children: React.ReactNode }) {
  const pathname = usePathname();
  const router = useRouter();
  const { data, isPending, isError } = useSession();

  useEffect(() => {
    if (isPending) return;
    if (data === null || isError) {
      const next = encodeURIComponent(pathname);
      router.replace(`/login?next=${next}`);
    }
  }, [data, isPending, isError, pathname, router]);

  if (isPending) {
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

  if (data === null || isError) return null;

  return <>{children}</>;
}
