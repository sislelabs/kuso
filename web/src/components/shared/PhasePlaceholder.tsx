import { Construction } from "lucide-react";
import { EmptyState } from "./EmptyState";

export function PhasePlaceholder({
  title,
  phase,
  description,
}: {
  title: string;
  phase: "B" | "C" | "D" | "E" | "F" | "G" | "H";
  description?: string;
}) {
  return (
    <div className="p-6 lg:p-8">
      <EmptyState
        icon={<Construction className="h-5 w-5" />}
        title={title}
        description={
          description ??
          `This page lands in Phase ${phase} of the frontend rewrite.`
        }
      />
    </div>
  );
}
