import { PhasePlaceholder } from "@/components/shared/PhasePlaceholder";

export function generateStaticParams() {
  return [{ project: "_", service: "_" }];
}

export default function ServiceDetailPage() {
  return <PhasePlaceholder title="Service detail" phase="C" />;
}
