import { AddServiceView } from "./view";

export function generateStaticParams() {
  return [{ project: "_" }];
}

export default function AddServicePage() {
  return <AddServiceView />;
}
