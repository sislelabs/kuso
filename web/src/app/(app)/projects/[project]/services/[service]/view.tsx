"use client";

import { useCallback, useEffect, useState } from "react";
import { usePathname, useRouter, useSearchParams } from "next/navigation";
import { ServiceWorkspace } from "@/components/service/workspace/ServiceWorkspace";
import { ResourcePageShell } from "@/components/service/workspace/ResourcePageShell";
import { resourceFromPath, servicePageHref } from "@/components/service/workspace/hrefs";
import { Skeleton } from "@/components/ui/skeleton";

// ServicePageView is the full-page service workspace. The URL is the
// whole state: /projects/<p>/services/<s>?env=&tab=&kind=&highlight=.
// Tab switches replace the entry; switching service (rail, ⌘K) pushes.
export function ServicePageView() {
  // The prerendered HTML is the "_" placeholder; read the real path only
  // once hydrated so the first client render matches it.
  const [hydrated, setHydrated] = useState(false);
  useEffect(() => setHydrated(true), []);
  const pathname = usePathname();
  const search = useSearchParams();
  const router = useRouter();
  const res = hydrated ? resourceFromPath(pathname) : null;
  const project = res?.project ?? "";
  // Notifications have written both short and "<project>-" names.
  const service = res ? (res.name.startsWith(`${project}-`) ? res.name.slice(project.length + 1) : res.name) : null;
  const env = search?.get("env") ?? "production";
  const tab = search?.get("tab") ?? undefined;
  const kind = search?.get("kind") ?? undefined;

  useEffect(() => {
    if (service) document.title = `${service} · ${project} · kuso`;
  }, [service, project]);

  const changeTab = useCallback(
    (next: string) => {
      const q = new URLSearchParams(search?.toString() ?? "");
      if (q.get("tab") === next) return;
      q.set("tab", next);
      // A tab switch dismisses the failure banner and any highlight.
      q.delete("kind");
      q.delete("highlight");
      router.replace(`${pathname}?${q.toString()}`, { scroll: false });
    },
    [search, pathname, router],
  );
  const restore = useCallback(
    (held: string, heldEnv?: string) => {
      router.replace(servicePageHref(project, held, { env: heldEnv, tab }), { scroll: false });
    },
    [router, project, tab],
  );

  if (!res || res.kind !== "service" || !service) {
    return (
      <div className="space-y-3 p-6 lg:p-8">
        <Skeleton className="h-10 w-64" />
        <Skeleton className="h-8 w-full" />
        <Skeleton className="h-64 w-full" />
      </div>
    );
  }

  return (
    <ResourcePageShell project={project} env={env} active={{ kind: "service", name: service }}>
      <ServiceWorkspace
        variant="page"
        project={project}
        service={service}
        env={env}
        defaultTab={tab}
        failureKind={kind}
        onTabChange={changeTab}
        onRestore={restore}
      />
    </ResourcePageShell>
  );
}
