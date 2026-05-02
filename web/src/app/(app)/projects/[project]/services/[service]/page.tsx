import { ServiceDetailView } from "./view";

export function generateStaticParams() {
  return [{ project: "_", service: "_" }];
}

export default function ServiceDetailPage() {
  return <ServiceDetailView />;
}
