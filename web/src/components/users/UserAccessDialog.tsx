"use client";

import Link from "next/link";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Skeleton } from "@/components/ui/skeleton";
import { QueryErrorState } from "@/components/shared/QueryErrorState";
import { cn } from "@/lib/utils";
import { useUserAccess, type AccessSource, type ProjectGrantSource, type UserAccess } from "@/features/users";

const ROLE_LABEL: Record<string, string> = { admin: "Admin", editor: "Editor", viewer: "Viewer" };

function roleName(role: string): string {
  return ROLE_LABEL[role] ?? (role || "None");
}

function viaLabel(s: AccessSource): string {
  return s.via === "group" ? `group ${s.groupName || s.groupId || "?"}` : "direct";
}

function grantLabel(s: ProjectGrantSource): string {
  const how = s.roleOverride ? `${roleName(s.roleOverride)} (set on grant)` : `${roleName(s.role)} (inherited)`;
  return `${how} via ${viaLabel(s)}`;
}

export function UserAccessDialog({
  userId,
  username,
  onClose,
}: {
  userId: string;
  username: string;
  onClose: () => void;
}) {
  const access = useUserAccess(userId);
  return (
    <Dialog
      open
      onOpenChange={(next) => {
        if (!next) onClose();
      }}
    >
      <DialogContent className="block gap-0 rounded-md p-0 sm:max-w-lg">
        <DialogHeader className="border-b border-[var(--border-subtle)] px-4 py-3 pr-10">
          <DialogTitle className="text-base">What {username} can access</DialogTitle>
          <DialogDescription className="text-xs">
            Resolved by the same rules the server enforces on every request.
          </DialogDescription>
        </DialogHeader>
        <div className="space-y-5 p-4">
          {access.isPending ? (
            <Skeleton className="h-40 rounded-md" />
          ) : access.isError ? (
            <QueryErrorState what="access" error={access.error} onRetry={() => void access.refetch()} />
          ) : (
            <AccessBody a={access.data} />
          )}
        </div>
      </DialogContent>
    </Dialog>
  );
}

function AccessBody({ a }: { a: UserAccess }) {
  return (
    <>
      {a.pending && (
        <p className="rounded-sm bg-[var(--warning-subtle)] px-3 py-2 text-xs text-[var(--warning)]">
          No usable access yet: not an admin and no project grants. Add them to a group with project grants, grant
          them a project, or make them an admin.
        </p>
      )}

      <Section title="Instance role">
        <div className="flex items-center gap-2">
          <Chip strong>{roleName(a.instanceRole)}</Chip>
          {a.instanceRole === "admin" && (
            <span className="text-xs text-[var(--text-secondary)]">every project and all instance settings</span>
          )}
        </div>
        {a.instanceRoleSources.length > 0 ? (
          <ul className="mt-2 space-y-1 text-xs text-[var(--text-secondary)]">
            {a.instanceRoleSources.map((s, i) => (
              <li key={i}>
                {roleName(s.role)} via {viaLabel(s)}
              </li>
            ))}
          </ul>
        ) : (
          <p className="mt-2 text-xs text-[var(--text-tertiary)]">No direct role and no group carries one.</p>
        )}
      </Section>

      <Section title="Groups">
        {a.groups.length === 0 ? (
          <p className="text-xs text-[var(--text-tertiary)]">Not in any group.</p>
        ) : (
          <div className="flex flex-wrap gap-1.5">
            {a.groups.map((g) => (
              <Chip key={g.id} title={g.instanceRole ? `Group role: ${roleName(g.instanceRole)}` : "Group has no instance role"}>
                {g.name}
                {ROLE_LABEL[g.instanceRole] && (
                  <span className="ml-1 text-[var(--text-tertiary)]">· {roleName(g.instanceRole)}</span>
                )}
              </Chip>
            ))}
          </div>
        )}
      </Section>

      <Section title="Projects">
        {a.allProjects && (
          <p className="mb-2 text-xs text-[var(--text-secondary)]">
            All projects, as admin (instance admin). Grants below don&apos;t limit that.
          </p>
        )}
        {a.projects.length === 0 ? (
          !a.allProjects && <p className="text-xs text-[var(--text-tertiary)]">No project grants.</p>
        ) : (
          <ul className="divide-y divide-[var(--border-subtle)] rounded-sm border border-[var(--border-subtle)]">
            {a.projects.map((p) => (
              <li key={p.project} className="flex items-start justify-between gap-3 px-3 py-2">
                <div className="min-w-0">
                  <Link
                    href={`/projects/${encodeURIComponent(p.project)}`}
                    className="truncate text-sm font-medium text-[var(--text-primary)] hover:underline"
                  >
                    {p.project}
                  </Link>
                  <ul className="mt-0.5 space-y-0.5 text-xs text-[var(--text-secondary)]">
                    {p.sources.map((s, i) => (
                      <li key={i}>{grantLabel(s)}</li>
                    ))}
                  </ul>
                </div>
                <Chip strong>{roleName(p.role)}</Chip>
              </li>
            ))}
          </ul>
        )}
      </Section>
    </>
  );
}

function Section({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <section>
      <h3 className="mb-2 text-xs font-medium text-[var(--text-secondary)]">{title}</h3>
      {children}
    </section>
  );
}

function Chip({ children, strong = false, title }: { children: React.ReactNode; strong?: boolean; title?: string }) {
  return (
    <span
      title={title}
      className={cn(
        "inline-flex shrink-0 items-center rounded-sm px-1.5 py-0.5 text-xs leading-none",
        strong
          ? "bg-[var(--accent-subtle)] font-medium text-[var(--text-primary)]"
          : "bg-[var(--bg-tertiary)] text-[var(--text-secondary)]",
      )}
    >
      {children}
    </span>
  );
}
