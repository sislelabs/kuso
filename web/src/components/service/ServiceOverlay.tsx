"use client";

import { PeekFrame } from "./workspace/PeekFrame";
import { ServiceWorkspace } from "./workspace/ServiceWorkspace";

// Panels inside the workspace import the dirty registry from here.
export { useOverlayDirty } from "./workspace/dirtyContext";

interface Props {
  project: string;
  service: string | null;
  env?: string;
  defaultTab?: string;
  failureKind?: string;
  onTabChange?: (tab: string) => void;
  onRestore?: (service: string, env?: string) => void;
  // "Open full page" (button or O) with the tab currently shown.
  onOpenPage?: (service: string, tab: string) => void;
  onClose: () => void;
}

// ServiceOverlay is the "peek": the ServiceWorkspace in a side panel over
// the canvas. Single click on a node opens it; double click, the "Open
// full page" button or O go to /projects/<p>/services/<s>. Clicking the
// backdrop or Escape closes it.
export function ServiceOverlay({ onOpenPage, service, ...props }: Props) {
  return (
    <ServiceWorkspace
      {...props}
      variant="peek"
      service={service}
      onOpenPage={onOpenPage && service ? (tab) => onOpenPage(service, tab) : undefined}
      frame={({ open, label, content, requestClose }) => (
        <PeekFrame open={open} label={label} requestClose={requestClose}>
          {content}
        </PeekFrame>
      )}
    />
  );
}
