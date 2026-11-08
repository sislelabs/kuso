"use client";

import { useState } from "react";
import { useRouter } from "next/navigation";
import {
  ArrowUpRight,
  Check,
  ExternalLink,
  Folder,
  FolderMinus,
  FolderPlus,
  GitBranch,
  MoreHorizontal,
  Pause,
  Power,
  Settings,
} from "lucide-react";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { cn } from "@/lib/utils";
import type { ProjectCardModel } from "./projectCardModel";

export interface ProjectCardActions {
  canManage: boolean;
  canStop: boolean;
  powerPending: boolean;
  knownFolders: ReadonlyArray<string>;
  folder: string;
  onMoveToFolder: (folder: string) => void;
  onStart: () => void;
  onRequestStop: () => void;
}

const itemClass =
  "flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-left text-[13px] text-[var(--text-primary)] outline-none hover:bg-[var(--bg-tertiary)] focus-visible:bg-[var(--bg-tertiary)] disabled:opacity-50";

// ProjectCardMenu is the card's single overflow menu (base-ui Popover —
// never a Menu primitive, see CLAUDE.md). It holds every action the card
// used to show as a row of icon buttons.
export function ProjectCardMenu({ model, actions }: { model: ProjectCardModel; actions: ProjectCardActions }) {
  const router = useRouter();
  const [open, setOpen] = useState(false);
  const [newFolder, setNewFolder] = useState("");
  const allStopped = model.status === "stopped";
  const close = () => {
    setOpen(false);
    setNewFolder("");
  };
  const move = (f: string) => {
    actions.onMoveToFolder(f);
    close();
  };

  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger
        aria-label={`More actions for ${model.name}`}
        className="relative z-30 inline-flex h-7 w-7 items-center justify-center rounded-md text-[var(--text-tertiary)] outline-none hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)] focus-visible:ring-2 focus-visible:ring-[var(--ring)] data-[popup-open]:bg-[var(--bg-tertiary)] data-[popup-open]:text-[var(--text-primary)]"
      >
        <MoreHorizontal className="h-4 w-4" />
      </PopoverTrigger>
      <PopoverContent align="end" className="w-60 gap-0 rounded-lg p-1">
        <button type="button" className={itemClass} onClick={() => { close(); router.push(`/projects/${model.name}`); }}>
          <ArrowUpRight className="h-3.5 w-3.5 text-[var(--text-tertiary)]" />
          Open project
        </button>
        {model.publicURL && (
          <a href={model.publicURL} target="_blank" rel="noopener noreferrer" className={itemClass} onClick={close}>
            <ExternalLink className="h-3.5 w-3.5 text-[var(--text-tertiary)]" />
            Visit site
          </a>
        )}
        {model.repoHref && (
          <a href={model.repoHref} target="_blank" rel="noopener noreferrer" className={itemClass} onClick={close}>
            <GitBranch className="h-3.5 w-3.5 text-[var(--text-tertiary)]" />
            View repository
          </a>
        )}
        {actions.canManage && (
          <button
            type="button"
            className={itemClass}
            onClick={() => { close(); router.push(`/projects/${model.name}/settings`); }}
          >
            <Settings className="h-3.5 w-3.5 text-[var(--text-tertiary)]" />
            Project settings
          </button>
        )}

        <div className="my-1 border-t border-[var(--border-subtle)]" />
        <p className="px-2 pb-1 pt-1.5 text-xs text-[var(--text-tertiary)]">Move to folder</p>
        {actions.knownFolders.map((f) => (
          <button key={f} type="button" className={itemClass} onClick={() => move(f)}>
            <Folder className="h-3.5 w-3.5 text-[var(--text-tertiary)]" />
            <span className="truncate">{f}</span>
            {f === actions.folder && <Check className="ml-auto h-3.5 w-3.5 text-[var(--text-secondary)]" aria-label="current folder" />}
          </button>
        ))}
        {actions.folder && (
          <button type="button" className={itemClass} onClick={() => move("")}>
            <FolderMinus className="h-3.5 w-3.5 text-[var(--text-tertiary)]" />
            Remove from folder
          </button>
        )}
        <form
          className="flex items-center gap-1.5 px-1 py-1"
          onSubmit={(e) => {
            e.preventDefault();
            const v = newFolder.trim();
            if (v) move(v);
          }}
        >
          <FolderPlus aria-hidden className="ml-1 h-3.5 w-3.5 shrink-0 text-[var(--text-tertiary)]" />
          <input
            value={newFolder}
            onChange={(e) => setNewFolder(e.target.value)}
            placeholder="New folder name"
            aria-label="New folder name"
            maxLength={60}
            className="h-7 min-w-0 flex-1 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-primary)] px-2 text-[13px] text-[var(--text-primary)] outline-none placeholder:text-[var(--text-tertiary)] focus:border-[var(--ring)]"
          />
        </form>

        {actions.canStop && model.allServiceCount > 0 && (
          <>
            <div className="my-1 border-t border-[var(--border-subtle)]" />
            {allStopped ? (
              <button
                type="button"
                className={itemClass}
                disabled={actions.powerPending}
                onClick={() => { close(); actions.onStart(); }}
              >
                <Power className="h-3.5 w-3.5 text-[var(--success)]" />
                Start project
              </button>
            ) : (
              <button
                type="button"
                className={cn(itemClass, "text-[var(--error)]")}
                disabled={actions.powerPending}
                onClick={() => { close(); actions.onRequestStop(); }}
              >
                <Pause className="h-3.5 w-3.5" />
                Stop project…
              </button>
            )}
          </>
        )}
      </PopoverContent>
    </Popover>
  );
}
