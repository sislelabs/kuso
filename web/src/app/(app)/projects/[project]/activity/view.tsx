"use client";

import { useParams } from "next/navigation";
import { ActivityFeed } from "@/components/activity/ActivityFeed";

export function ActivityView() {
  const params = useParams<{ project: string }>();
  const project = params?.project ?? "";
  return (
    <div className="mx-auto max-w-3xl p-6 lg:p-8">
      <h1 className="mb-1 font-heading text-2xl font-semibold tracking-tight">
        Activity
      </h1>
      <p className="mb-6 text-sm text-[var(--text-secondary)]">
        Events for {project}. Project-scoped filtering goes server-side in Phase E.
      </p>
      <ActivityFeed
        filter={(e) =>
          (e.pipelineName ?? "").toString() === project || !e.pipelineName
        }
      />
    </div>
  );
}
