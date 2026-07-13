import { Box, Package, FileCode2, Layers, Container, Cog } from "lucide-react";
import { cn } from "@/lib/utils";

const map = {
  dockerfile: { Icon: Box, label: "Dockerfile" },
  nixpacks: { Icon: Package, label: "Nixpacks" },
  buildpacks: { Icon: Layers, label: "Buildpacks" },
  static: { Icon: FileCode2, label: "Static" },
  // Non-build runtimes. Without these, image services rendered with
  // the Dockerfile icon+label (misleading — they never build) and
  // workers fell through to Dockerfile too.
  image: { Icon: Container, label: "Image" },
  worker: { Icon: Cog, label: "Worker" },
} as const;

export type Runtime = keyof typeof map;

export function RuntimeIcon({
  runtime,
  className,
  withLabel,
}: {
  runtime?: string;
  className?: string;
  withLabel?: boolean;
}) {
  const k = (runtime ?? "dockerfile") as Runtime;
  const entry = map[k] ?? map.dockerfile;
  const { Icon, label } = entry;
  return (
    <span
      className={cn(
        "inline-flex items-center gap-1.5 text-[var(--text-secondary)]",
        className
      )}
    >
      <Icon className="h-3.5 w-3.5" />
      {withLabel && <span className="font-mono text-[11px]">{label}</span>}
    </span>
  );
}
