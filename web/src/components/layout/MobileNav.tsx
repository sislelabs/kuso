"use client";

import Link from "next/link";
import { useParams } from "next/navigation";
import {
  Sheet,
  SheetContent,
  SheetHeader,
  SheetTitle,
} from "@/components/ui/sheet";
import { Logo } from "@/components/shared/Logo";
import { useProjects } from "@/features/projects";
import { useSession } from "@/features/auth";
import { Plus } from "lucide-react";
import { cn } from "@/lib/utils";

interface Props {
  open: boolean;
  onOpenChange: (v: boolean) => void;
}

export function MobileNav({ open, onOpenChange }: Props) {
  const params = useParams<{ project?: string }>();
  const currentProject = params?.project;
  const projects = useProjects();
  const { data: session } = useSession();
  const user = session?.user;

  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent side="left" className="w-72 p-0">
        <SheetHeader className="border-b border-[var(--border-subtle)] p-4">
          <SheetTitle>
            <Logo />
          </SheetTitle>
        </SheetHeader>
        <nav className="p-3">
          <h3 className="mb-2 px-3 text-[10px] font-semibold text-[var(--text-tertiary)] uppercase tracking-[0.08em]">
            Projects
          </h3>
          <ul className="space-y-0.5">
            {(projects.data ?? []).map((p) => {
              const name = p.metadata.name;
              const isActive = currentProject === name;
              return (
                <li key={p.metadata.uid ?? name}>
                  <Link
                    href={`/projects/${name}`}
                    onClick={() => onOpenChange(false)}
                    className={cn(
                      "flex items-center gap-3 rounded-lg px-3 py-2 text-sm font-medium transition-all",
                      isActive
                        ? "bg-[var(--accent-subtle)] text-[var(--text-primary)]"
                        : "text-[var(--text-secondary)] hover:bg-[var(--bg-tertiary)]"
                    )}
                  >
                    <span className="h-2 w-2 rounded-full bg-[var(--text-tertiary)]/50" />
                    <span className="truncate">{name}</span>
                  </Link>
                </li>
              );
            })}
            <li>
              <Link
                href="/projects/new"
                onClick={() => onOpenChange(false)}
                className="flex items-center gap-3 rounded-lg px-3 py-2 text-sm font-medium text-[var(--text-secondary)] hover:bg-[var(--bg-tertiary)]"
              >
                <Plus className="h-4 w-4" />
                New project
              </Link>
            </li>
          </ul>
        </nav>
        {user && (
          <div className="absolute bottom-0 left-0 right-0 border-t border-[var(--border-subtle)] bg-[var(--bg-secondary)] p-4">
            <div className="text-sm font-medium text-[var(--text-primary)]">
              {user.name}
            </div>
            <div className="font-mono text-[11px] text-[var(--text-tertiary)]">
              {user.email}
            </div>
          </div>
        )}
      </SheetContent>
    </Sheet>
  );
}
