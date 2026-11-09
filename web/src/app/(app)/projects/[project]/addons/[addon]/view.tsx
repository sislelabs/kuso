"use client";

import { useCallback, useEffect, useState } from "react";
import { usePathname, useRouter, useSearchParams } from "next/navigation";
import { AddonWorkspace } from "@/components/addon/AddonWorkspace";
import { ResourcePageShell } from "@/components/service/workspace/ResourcePageShell";
import { addonPageHref, resourceFromPath } from "@/components/service/workspace/hrefs";
import { Skeleton } from "@/components/ui/skeleton";

// AddonPageView is the full-page addon workspace:
// /projects/<p>/addons/<short name>?env=&tab=.
export function AddonPageView() {
  const [hydrated, setHydrated] = useState(false);
  useEffect(() => setHydrated(true), []);
  const pathname = usePathname();
  const search = useSearchParams();
  const router = useRouter();
  const res = hydrated ? resourceFromPath(pathname) : null;
  const project = res?.project ?? "";
  const short = res ? (res.name.startsWith(`${project}-`) ? res.name.slice(project.length + 1) : res.name) : "";
  const env = search?.get("env") ?? "production";
  const tab = search?.get("tab") ?? undefined;

  useEffect(() => {
    if (short) document.title = `${short} · ${project} · kuso`;
  }, [short, project]);

  const changeTab = useCallback(
    (next: string) => {
      const q = new URLSearchParams(search?.toString() ?? "");
      if (q.get("tab") === next) return;
      q.set("tab", next);
      router.replace(`${pathname}?${q.toString()}`, { scroll: false });
    },
    [search, pathname, router],
  );
  const restore = useCallback(
    (held: string) => router.replace(addonPageHref(project, held, { env, tab }), { scroll: false }),
    [router, project, env, tab],
  );

  if (!res || res.kind !== "addon" || !short) {
    return (
      <div className="space-y-3 p-6 lg:p-8">
        <Skeleton className="h-10 w-64" />
        <Skeleton className="h-64 w-full" />
      </div>
    );
  }

  return (
    <ResourcePageShell project={project} env={env} active={{ kind: "addon", name: short }}>
      <AddonWorkspace
        variant="page"
        project={project}
        // The workspace addresses addons by full CR name.
        addon={`${project}-${short}`}
        defaultTab={tab}
        onTabChange={changeTab}
        onRestore={restore}
      />
    </ResourcePageShell>
  );
}
