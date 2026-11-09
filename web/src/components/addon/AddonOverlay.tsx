"use client";

import { PeekFrame } from "@/components/service/workspace/PeekFrame";
import { AddonWorkspace } from "./AddonWorkspace";

// Addon panels import the dirty registry from here.
export { useAddonOverlayDirty } from "./AddonWorkspace";

interface Props {
  project: string;
  addon: string | null;
  defaultTab?: string;
  onTabChange?: (tab: string) => void;
  onRestore?: (addon: string) => void;
  onOpenPage?: (addon: string, tab: string) => void;
  onClose: () => void;
}

// AddonOverlay is the addon "peek": the AddonWorkspace in a side panel
// over the canvas, with "Open full page" (button or O) to
// /projects/<p>/addons/<a>.
export function AddonOverlay({ addon, onOpenPage, ...props }: Props) {
  return (
    <AddonWorkspace
      {...props}
      variant="peek"
      addon={addon}
      onOpenPage={onOpenPage && addon ? (tab) => onOpenPage(addon, tab) : undefined}
      frame={({ open, label, content, requestClose, tab }) => (
        <PeekFrame open={open} label={label} requestClose={requestClose} wide={tab === "sql"}>
          {content}
        </PeekFrame>
      )}
    />
  );
}
