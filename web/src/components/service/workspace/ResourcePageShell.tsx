"use client";

import Link from "next/link";
import { useMemo, useState } from "react";
import { useRouter } from "next/navigation";
import { ArrowLeft, Check, ChevronDown, Clock, LayoutGrid, Search } from "lucide-react";
import { useAddons, useProject } from "@/features/projects";
import { STATUS_TONE, useLatestBuilds, useProjectCrons } from "@/features/services";
import { RuntimeIcon } from "@/components/service/RuntimeIcon";
import { AddonIcon } from "@/components/addon/AddonIcon";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { Command, CommandEmpty, CommandGroup, CommandInput, CommandItem, CommandList } from "@/components/ui/command";
import { Skeleton } from "@/components/ui/skeleton";
import { cn } from "@/lib/utils";
import { addonPageHref, projectCanvasHref, servicePageHref } from "./hrefs";
import { railCrons, railResources, type RailAddon, type RailCron, type RailService } from "./railResources";
import { StagedChangesBar } from "@/components/staged";

export type ActiveResource = { kind: "service" | "addon"; name: string };

type Resources = { services: RailService[]; addons: RailAddon[]; crons: RailCron[] };

const groupHeading =
  "px-0 [&_[cmdk-group-heading]]:px-2 [&_[cmdk-group-heading]]:py-1.5 [&_[cmdk-group-heading]]:text-xs [&_[cmdk-group-heading]]:font-medium [&_[cmdk-group-heading]]:text-[var(--text-tertiary)]";

// ResourcePageShell frames a full-page service or addon view: a rail of
// the project's resources on the left (switch without going back to the
// canvas) and the workspace on the right. Below lg the rail folds into a
// breadcrumb switcher.
export function ResourcePageShell({
  project,
  env,
  active,
  children,
}: {
  project: string;
  env: string;
  active: ActiveResource;
  children: React.ReactNode;
}) {
  const proj = useProject(project);
  const addons = useAddons(project);
  const latestBuilds = useLatestBuilds(project, env);
  const crons = useProjectCrons(project);
  const resources = useMemo<Resources | null>(
    () =>
      proj.data
        ? {
            ...railResources(project, env, proj.data.services, proj.data.environments, addons.data ?? [], latestBuilds.data),
            crons: railCrons(project, env, crons.data ?? [], proj.data.services),
          }
        : null,
    [proj.data, addons.data, project, env, latestBuilds.data, crons.data],
  );
  const hrefFor = (kind: ActiveResource["kind"], name: string) =>
    kind === "service" ? servicePageHref(project, name, { env }) : addonPageHref(project, name, { env });
  // A cron has no page of its own: a service cron lives on its service's
  // Crons tab, a project cron on the canvas.
  const cronHref = (c: RailCron) =>
    c.service ? servicePageHref(project, c.service, { env, tab: "crons" }) : projectCanvasHref(project, env);

  return (
    <div className="flex h-full min-h-0">
      <aside
        aria-label={`${project} resources`}
        className="hidden w-60 shrink-0 flex-col border-r border-[var(--border-subtle)] bg-[var(--bg-secondary)]/40 lg:flex"
      >
        <div className="flex h-12 shrink-0 items-center border-b border-[var(--border-subtle)] px-2">
          <Link
            href={projectCanvasHref(project, env)}
            className="inline-flex h-8 min-w-0 flex-1 items-center gap-2 rounded-md px-2 text-[13px] font-medium text-[var(--text-secondary)] hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)]"
          >
            <LayoutGrid aria-hidden className="h-3.5 w-3.5 shrink-0" />
            <span className="truncate">Canvas</span>
            <span className="ml-auto truncate font-mono text-xs text-[var(--text-tertiary)]">{project}</span>
          </Link>
        </div>
        <ResourceRail resources={resources} loading={proj.isPending} active={active} hrefFor={hrefFor} cronHref={cronHref} />
      </aside>
      <div className="flex min-w-0 flex-1 flex-col">
        <div className="flex h-11 shrink-0 items-center gap-1 border-b border-[var(--border-subtle)] px-2 sm:px-4 lg:hidden">
          <Link
            href={projectCanvasHref(project, env)}
            aria-label={`Back to the ${project} canvas`}
            className="inline-flex h-8 items-center gap-1.5 rounded-md px-2 text-[13px] text-[var(--text-secondary)] hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)]"
          >
            <ArrowLeft aria-hidden className="h-3.5 w-3.5" />
            <span className="max-w-[30vw] truncate">{project}</span>
          </Link>
          <span aria-hidden className="text-[var(--border-strong)] select-none">/</span>
          <ResourceSwitcher resources={resources} active={active} hrefFor={hrefFor} cronHref={cronHref} />
        </div>
        <div className="min-h-0 flex-1">{children}</div>
      </div>
      <StagedChangesBar project={project} />
    </div>
  );
}

function StatusDot({ tone, label }: { tone: keyof typeof STATUS_TONE; label: string }) {
  return (
    <span
      aria-label={label}
      title={label}
      className={cn("h-2 w-2 shrink-0 rounded-full ring-2 ring-[var(--bg-secondary)]", STATUS_TONE[tone].dot)}
    />
  );
}

function ResourceRail({
  resources,
  loading,
  active,
  hrefFor,
  cronHref,
}: {
  resources: Resources | null;
  loading: boolean;
  active: ActiveResource;
  hrefFor: (kind: ActiveResource["kind"], name: string) => string;
  cronHref: (c: RailCron) => string;
}) {
  const [q, setQ] = useState("");
  const match = (s: string) => s.toLowerCase().includes(q.trim().toLowerCase());
  const services = (resources?.services ?? []).filter((s) => match(s.display) || match(s.name));
  const addons = (resources?.addons ?? []).filter((a) => match(a.name) || match(a.kind));
  const crons = (resources?.crons ?? []).filter((c) => match(c.display) || match(c.name) || match(c.service ?? ""));
  const total = (resources?.services.length ?? 0) + (resources?.addons.length ?? 0) + (resources?.crons.length ?? 0);

  return (
    <nav aria-label="Switch resource" className="flex min-h-0 flex-1 flex-col">
      {total > 6 ? (
        <div className="px-2 pt-2">
          <label className="relative block">
            <span className="sr-only">Filter resources</span>
            <Search aria-hidden className="pointer-events-none absolute left-2 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-[var(--text-tertiary)]" />
            <input
              value={q}
              onChange={(e) => setQ(e.target.value)}
              placeholder="Filter…"
              className="h-8 w-full rounded-md border border-[var(--border-subtle)] bg-[var(--bg-primary)] pl-7 pr-2 text-[13px] outline-none placeholder:text-[var(--text-tertiary)] focus:border-[var(--border-strong)]"
            />
          </label>
        </div>
      ) : null}
      <div className="min-h-0 flex-1 overflow-y-auto px-2 py-2">
        {loading ? (
          <div className="space-y-1.5 p-1">
            <Skeleton className="h-7 w-full" />
            <Skeleton className="h-7 w-full" />
            <Skeleton className="h-7 w-3/4" />
          </div>
        ) : (
          <>
            <RailGroup title="Services" count={services.length}>
              {services.map((s) => (
                <RailRow
                  key={`s:${s.name}`}
                  href={hrefFor("service", s.name)}
                  active={active.kind === "service" && active.name === s.name}
                  icon={<RuntimeIcon runtime={s.runtime} className="h-3.5 w-3.5" />}
                  label={s.display}
                  status={s.status}
                />
              ))}
            </RailGroup>
            {addons.length > 0 ? (
              <RailGroup title="Databases & addons" count={addons.length}>
                {addons.map((a) => (
                  <RailRow
                    key={`a:${a.name}`}
                    href={hrefFor("addon", a.name)}
                    active={active.kind === "addon" && active.name === a.name}
                    icon={<AddonIcon kind={a.kind} className="h-3.5 w-3.5" />}
                    label={a.name}
                    status={a.status}
                  />
                ))}
              </RailGroup>
            ) : null}
            {crons.length > 0 ? (
              <RailGroup title="Cron jobs" count={crons.length}>
                {crons.map((c) => (
                  <RailRow
                    key={`c:${c.name}`}
                    href={cronHref(c)}
                    active={false}
                    icon={<Clock aria-hidden className="h-3.5 w-3.5" />}
                    label={c.display}
                    hint={[c.schedule, c.service ? `in ${c.service}` : ""].filter(Boolean).join(" · ")}
                    status={c.status}
                  />
                ))}
              </RailGroup>
            ) : null}
            {q && services.length + addons.length + crons.length === 0 ? (
              <p className="px-2 py-4 text-xs text-[var(--text-tertiary)]">Nothing matches “{q}”.</p>
            ) : null}
          </>
        )}
      </div>
    </nav>
  );
}

function RailGroup({ title, count, children }: { title: string; count: number; children: React.ReactNode }) {
  return (
    <div className="mb-3">
      <p className="flex items-center justify-between px-2 pb-1 pt-1 text-xs font-medium text-[var(--text-tertiary)]">
        {title}
        <span className="tabular-nums">{count}</span>
      </p>
      <ul className="space-y-px">{children}</ul>
    </div>
  );
}

function RailRow({
  href,
  active,
  icon,
  label,
  hint,
  status,
}: {
  href: string;
  active: boolean;
  icon: React.ReactNode;
  label: string;
  // Shown on hover; the row stays one line.
  hint?: string;
  status: RailService["status"];
}) {
  return (
    <li>
      <Link
        href={href}
        aria-current={active ? "page" : undefined}
        title={hint ? `${label} · ${hint}` : undefined}
        className={cn(
          "group relative flex h-8 items-center gap-2 rounded-md pl-2.5 pr-2 text-[13px] transition-colors",
          active
            ? "bg-[var(--bg-elevated)] font-medium text-[var(--text-primary)] shadow-[var(--shadow-sm)] ring-1 ring-[var(--border-subtle)]"
            : "text-[var(--text-secondary)] hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)]",
        )}
      >
        {active ? <span aria-hidden className="absolute inset-y-1.5 left-0 w-0.5 rounded-full bg-[var(--accent)]" /> : null}
        <span className="inline-flex h-4 w-4 shrink-0 items-center justify-center text-[var(--text-secondary)]">{icon}</span>
        <span className="min-w-0 flex-1 truncate">{label}</span>
        <StatusDot tone={status.tone} label={status.label} />
      </Link>
    </li>
  );
}

function ResourceSwitcher({
  resources,
  active,
  hrefFor,
  cronHref,
}: {
  resources: Resources | null;
  active: ActiveResource;
  hrefFor: (kind: ActiveResource["kind"], name: string) => string;
  cronHref: (c: RailCron) => string;
}) {
  const router = useRouter();
  const [open, setOpen] = useState(false);
  const current =
    active.kind === "service"
      ? resources?.services.find((s) => s.name === active.name)?.display
      : resources?.addons.find((a) => a.name === active.name)?.name;
  const go = (kind: ActiveResource["kind"], name: string) => {
    setOpen(false);
    router.push(hrefFor(kind, name));
  };
  const goCron = (c: RailCron) => {
    setOpen(false);
    router.push(cronHref(c));
  };
  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger
        aria-label={`${current ?? active.name}. Switch resource`}
        className="inline-flex h-8 min-w-0 items-center gap-1.5 rounded-md px-2 text-[13px] font-medium text-[var(--text-primary)] hover:bg-[var(--bg-tertiary)] data-[popup-open]:bg-[var(--bg-tertiary)]"
      >
        <span className="truncate">{current ?? active.name}</span>
        <ChevronDown aria-hidden className="size-3.5 shrink-0 text-[var(--text-tertiary)]" />
      </PopoverTrigger>
      <PopoverContent align="start" className="w-72 gap-0 rounded-md p-0">
        <Command>
          <CommandInput placeholder="Find a service or addon…" className="h-9 text-[13px]" />
          <CommandList className="p-1">
            <CommandEmpty className="py-6 text-sm">Nothing matches.</CommandEmpty>
            {resources && resources.services.length > 0 ? (
              <CommandGroup heading="Services" className={groupHeading}>
                {resources.services.map((s) => (
                  <CommandItem key={s.name} value={`service ${s.display} ${s.name}`} onSelect={() => go("service", s.name)} className="px-2 py-1.5">
                    <StatusDot tone={s.status.tone} label={s.status.label} />
                    <span className="truncate text-[13px]">{s.display}</span>
                    {active.kind === "service" && active.name === s.name ? <Check className="ml-auto h-3 w-3 text-[var(--accent)]" /> : null}
                  </CommandItem>
                ))}
              </CommandGroup>
            ) : null}
            {resources && resources.addons.length > 0 ? (
              <CommandGroup heading="Databases & addons" className={groupHeading}>
                {resources.addons.map((a) => (
                  <CommandItem key={a.name} value={`addon ${a.name} ${a.kind}`} onSelect={() => go("addon", a.name)} className="px-2 py-1.5">
                    <StatusDot tone={a.status.tone} label={a.status.label} />
                    <span className="truncate text-[13px]">{a.name}</span>
                    {active.kind === "addon" && active.name === a.name ? <Check className="ml-auto h-3 w-3 text-[var(--accent)]" /> : null}
                  </CommandItem>
                ))}
              </CommandGroup>
            ) : null}
            {resources && resources.crons.length > 0 ? (
              <CommandGroup heading="Cron jobs" className={groupHeading}>
                {resources.crons.map((c) => (
                  <CommandItem key={c.name} value={`cron ${c.display} ${c.name} ${c.service ?? ""}`} onSelect={() => goCron(c)} className="px-2 py-1.5">
                    <StatusDot tone={c.status.tone} label={c.status.label} />
                    <span className="truncate text-[13px]">{c.display}</span>
                    <span className="ml-auto truncate text-xs text-[var(--text-tertiary)]">{c.schedule}</span>
                  </CommandItem>
                ))}
              </CommandGroup>
            ) : null}
          </CommandList>
        </Command>
      </PopoverContent>
    </Popover>
  );
}
