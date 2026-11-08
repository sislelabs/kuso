"use client";

import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { LogViewer } from "@/components/logs/LogViewer";

// ProjectLogsSheet searches the log archive across every service in the
// project (GET /api/projects/{p}/logs/search) — "what crashed in this
// project in the last hour" without opening each service. Same viewer as
// the service Logs tab, plus a service column.
export function ProjectLogsSheet({
  project,
  open,
  onOpenChange,
  defaultEnv,
}: {
  project: string;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  defaultEnv: string;
}) {
  const prefix = project + "-";
  const serviceLabel = (fqn: string) => (fqn.startsWith(prefix) ? fqn.slice(prefix.length) : fqn);

  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent side="right" className="data-[side=right]:w-full data-[side=right]:sm:max-w-4xl">
        <SheetHeader>
          <SheetTitle className="text-base">Logs · {project}</SheetTitle>
          <SheetDescription className="text-[13px]">
            History across every service in the project. Open a service for its live tail.
          </SheetDescription>
        </SheetHeader>
        <div className="flex min-h-0 flex-1 flex-col px-4 pb-4">
          {open && <LogViewer project={project} defaultEnv={defaultEnv} serviceLabel={serviceLabel} className="min-h-0 flex-1" />}
        </div>
      </SheetContent>
    </Sheet>
  );
}
