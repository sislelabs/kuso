import { toast } from "sonner";
import type { CreateServiceOutcome } from "@/features/services/createServiceFlow";

// reportCreateOutcome turns the post-create best-effort steps into
// toasts. Shared by the add-service and new-project flows.
export function reportCreateOutcome(out: CreateServiceOutcome, label: string, branch?: string) {
  if (out.envFailed.length > 0) {
    toast.warning(
      `${label} created, but ${out.envFailed.length === 1 ? "this variable" : "these variables"} didn't save: ${out.envFailed
        .map((f) => `${f.name} (${f.error})`)
        .join(", ")}. Add ${out.envFailed.length === 1 ? "it" : "them"} on the Variables tab.`,
      { duration: Infinity, closeButton: true },
    );
  }
  if (out.build === "failed") {
    toast.warning(
      `${label} created, but the first build didn't start: ${out.buildError ?? "unknown error"}. Trigger one from the Deployments tab.`,
      { duration: Infinity, closeButton: true },
    );
  } else if (out.build === "skipped") {
    toast.success(`${label} created — not deployed yet. Deploy it from the Deployments tab when it's ready.`);
  } else if (out.build === "started") {
    toast.success(branch ? `${label} created — building from ${branch}` : `${label} created — first build started`);
  } else {
    toast.success(`${label} created`);
  }
}
