"use client";

import { useEffect, useMemo, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import {
  ReactFlow,
  Background,
  Controls,
  Panel,
  type Node,
  type Edge,
  type NodeMouseHandler,
  type OnNodesChange,
  type Connection,
  applyNodeChanges,
} from "@xyflow/react";
import "@xyflow/react/dist/style.css";

import type { KusoAddon, KusoEnvironment, KusoService } from "@/types/projects";
import { api } from "@/lib/api-client";
import {
  type BuildSummary,
  getServiceEnv,
  setServiceEnv,
} from "@/features/services/api";
import { ServiceNode, type ServiceNodeData } from "./ServiceNode";
import { AddonNode, type AddonNodeData } from "./AddonNode";
import { CronNode, type CronNodeData } from "./CronNode";
import {
  applyStoredLayout,
  autoLayout,
  loadStoredLayout,
  saveStoredLayout,
} from "./layout";
import {
  CanvasContextMenu,
  type ContextMenuItem,
  type ContextMenuEntry,
} from "./CanvasContextMenu";
import { planConnection } from "./connect";
import { servicesQueryKey } from "@/features/projects";
import { AddAddonDialog } from "@/components/addon/AddAddonDialog";
import { AddCronDialog } from "@/components/cron/AddCronDialog";
import { EditCronDialog } from "@/components/cron/EditCronDialog";
import { ConfirmDialog } from "@/components/shared/ConfirmDialog";
import { serviceShortName } from "@/lib/utils";
import { useTriggerBuild } from "@/features/services";
import {
  ExternalLink,
  ScrollText,
  RotateCcw,
  Trash2,
  Eye,
  Plus,
  LayoutGrid,
  Database,
  HardDrive,
  Settings as SettingsIcon,
} from "lucide-react";
import { toast } from "sonner";

const nodeTypes = {
  service: ServiceNode,
  addon: AddonNode,
  cron: CronNode,
};

interface Props {
  project: string;
  services: KusoService[];
  addons: KusoAddon[];
  envs: KusoEnvironment[];
  // Both selectors take an optional `tab` so canvas right-click items
  // can land the user directly on the right overlay tab (Logs, SQL,
  // Backups, …) instead of the default Deployments. Empty/undefined
  // means "use the overlay's own default."
  onSelectService?: (svcName: string, tab?: string) => void;
  onSelectAddon?: (addonName: string, tab?: string) => void;
}

interface ContextState {
  open: boolean;
  x: number;
  y: number;
  items: ContextMenuEntry[];
}

export function ProjectCanvas({
  project,
  services,
  addons,
  envs,
  onSelectService,
  onSelectAddon,
}: Props) {
  // Latest build per service in this project. Powers the service
  // node's status — when the latest build is pending/running/failed,
  // we color the canvas card accordingly even if env.status.phase is
  // stale (which it routinely is — helm-operator doesn't write
  // phase=failed back to the env CR when a build fails). 5s refetch
  // so a "redeploy" → "pending" transition is visible quickly.
  const latestBuilds = useQuery<Record<string, BuildSummary>>({
    queryKey: ["projects", project, "builds", "latest"],
    queryFn: () => api(`/api/projects/${encodeURIComponent(project)}/builds/latest`),
    // Pre-v0.9.38 had refetchInterval=5s and staleTime=2s, which on
    // a busy multi-user dashboard meant 12 fetches/min/project of a
    // payload that rarely changes. Match staleTime to interval so a
    // refocus doesn't fire a redundant fetch when one is about to
    // run anyway. Pause polling while the tab is hidden — the user
    // isn't watching, kube isn't telling us anything new, and a
    // 30-tab browser shouldn't burn N×Q pollers in the background.
    refetchInterval: 5_000,
    refetchIntervalInBackground: false,
    staleTime: 5_000,
  });

  // All KusoCrons in the project — both service-attached and project-
  // scoped. Service-attached crons are still rendered as canvas nodes
  // (the right-click "Add cron" only creates the project-scoped
  // variants, but legacy ones still show up). Filter on the client to
  // keep the rollup endpoint as a simple GET.
  const allCrons = useQuery<{ metadata: { name: string }; spec: CronNodeData["cron"]["spec"] }[]>({
    queryKey: ["projects", project, "crons"],
    queryFn: () => api(`/api/projects/${encodeURIComponent(project)}/crons`),
    refetchInterval: 30_000,
    refetchIntervalInBackground: false,
    staleTime: 10_000,
  });

  const initialNodes: Node[] = useMemo(() => {
    const out: Node[] = [];
    services.forEach((s) => {
      const env = envs.find(
        (e) => e.spec.service === s.metadata.name && e.spec.kind === "production"
      );
      // serviceShortName strips the "<project>-" prefix the server
      // uses for the FQ name; the latest-builds endpoint returns the
      // map keyed by the same short name so direct lookup works.
      const shortName = serviceShortName(project, s.metadata.name);
      const latestBuild = latestBuilds.data?.[shortName];
      out.push({
        id: `svc:${s.metadata.name}`,
        type: "service",
        position: { x: 0, y: 0 },
        data: { project, service: s, env, latestBuild } satisfies ServiceNodeData,
      });
    });
    addons.forEach((a) => {
      out.push({
        id: `addon:${a.metadata.name}`,
        type: "addon",
        position: { x: 0, y: 0 },
        data: { project, addon: a } satisfies AddonNodeData,
      });
    });
    (allCrons.data ?? []).forEach((c) => {
      out.push({
        id: `cron:${c.metadata.name}`,
        type: "cron",
        position: { x: 0, y: 0 },
        data: { project, cron: c } satisfies CronNodeData,
      });
    });
    return out;
  }, [project, services, addons, envs, latestBuilds.data, allCrons.data]);

  const initialEdges: Edge[] = useMemo(() => {
    const out: Edge[] = [];
    // Addon → service edges come in two flavours:
    //
    //   1. "explicit" — the service actively reads the addon (either
    //      a server-resolved valueFrom.secretKeyRef pointing at the
    //      addon's conn-secret, or an unresolved `${{addon.KEY}}`
    //      ref still in spec.envVars). Drawn in amber, fully opaque.
    //
    //   2. "auto-injected" — kuso's server stamps every addon's
    //      conn-secret into every env's envFromSecrets at create
    //      time. That means the secret is mounted on the pod even
    //      when no env-var explicitly references it. Most projects
    //      use this exclusively (e.g. an app that just reads
    //      DATABASE_URL from process.env without configuring a
    //      ref). Drawn in amber, dashed + dimmer, so the user
    //      sees "this addon is available to this service" without
    //      conflating it with "this service explicitly uses it."
    //
    // The two-tier render keeps the original signal intact (an
    // explicit ref still looks like a hard dependency) while
    // surfacing the implicit wiring that previously made the canvas
    // look like the addon was disconnected from the rest of the
    // project.
    const explicitUsedAddons = new Set<string>(); // "<svc-fqn>|<addon-fqn>"
    const implicitUsedAddons = new Set<string>(); // same shape
    services.forEach((s) => {
      for (const ev of s.spec.envVars ?? []) {
        // valueFrom path: server-resolved addon refs land here.
        const skr = (ev?.valueFrom as { secretKeyRef?: { name?: string } } | undefined)?.secretKeyRef;
        if (skr?.name && skr.name.endsWith("-conn")) {
          // Conn-secret naming is "<addon-cr-name>-conn" — strip the
          // suffix and match against known addons.
          const addonFQN = skr.name.slice(0, -"-conn".length);
          if (addons.some((a) => a.metadata.name === addonFQN)) {
            explicitUsedAddons.add(`${s.metadata.name}|${addonFQN}`);
          }
        }
        // value path: literal ${{addon.KEY}} text. Server resolves
        // these on save, but during the brief window before the next
        // refetch the canvas would drop the edge — so we still draw
        // it.
        if (ev?.value) {
          const m = ev.value.match(/^\$\{\{\s*([a-zA-Z0-9_-]+)\.[A-Z_][A-Z0-9_]*\s*\}\}$/);
          if (m) {
            // Check both short + fqn names against known addons.
            const refName = m[1];
            const candidates = [refName, `${project}-${refName}`];
            for (const c of candidates) {
              if (addons.some((a) => a.metadata.name === c)) {
                explicitUsedAddons.add(`${s.metadata.name}|${c}`);
                break;
              }
            }
          }
        }
      }
    });
    // Auto-injected edges: scan the matching env CR's envFromSecrets.
    // The conn-secret naming convention "<addon-fqn>-conn" is the
    // same as the explicit path above; we just discover it via the
    // env CR's mount list rather than an env-var ref.
    envs.forEach((e) => {
      const svcFQN = e.spec.service;
      const secrets = e.spec.envFromSecrets ?? [];
      for (const secretName of secrets) {
        if (!secretName.endsWith("-conn")) continue;
        const addonFQN = secretName.slice(0, -"-conn".length);
        if (!addons.some((a) => a.metadata.name === addonFQN)) continue;
        // Don't downgrade an explicit edge — if the service already
        // has a hard ref, the amber-solid edge wins.
        const key = `${svcFQN}|${addonFQN}`;
        if (!explicitUsedAddons.has(key)) {
          implicitUsedAddons.add(key);
        }
      }
    });
    explicitUsedAddons.forEach((key) => {
      const [svcFQN, addonFQN] = key.split("|");
      out.push({
        id: `e:${addonFQN}->${svcFQN}`,
        source: `addon:${addonFQN}`,
        target: `svc:${svcFQN}`,
        animated: true,
        // The `kind` field is non-standard React Flow data; we
        // stash the edge category here so the filter chips can
        // toggle visibility without re-deriving from the id.
        // Colour: amber. Distinct from service refs (the accent
        // colour) so the two categories read as separate at a
        // glance.
        data: { kind: "addon" },
        style: { stroke: "rgb(245 158 11)", strokeWidth: 1.5, opacity: 0.85 },
      });
    });
    implicitUsedAddons.forEach((key) => {
      const [svcFQN, addonFQN] = key.split("|");
      out.push({
        id: `e:auto:${addonFQN}->${svcFQN}`,
        source: `addon:${addonFQN}`,
        target: `svc:${svcFQN}`,
        animated: false,
        // Same edge category ("addon") so the filter chip toggles
        // both kinds together. Dashed + dimmer to read as "wired
        // but not actively referenced" at a glance.
        data: { kind: "addon" },
        style: {
          stroke: "rgb(245 158 11)",
          strokeWidth: 1.25,
          strokeDasharray: "4 4",
          opacity: 0.4,
        },
      });
    });
    // Service → service edges from env-var refs. The server resolves
    // `${{ x.URL }}` to a literal in-cluster DNS string at SetEnv
    // time, so by the time the canvas reads spec.envVars the ref is
    // already gone. We reverse it: any value that points at another
    // service's cluster-local DNS counts as a dep.
    //
    // Pattern: <fqn>.<ns>.svc.cluster.local — fqn is the kube Service
    // name, which is the same as the KusoService CR name.
    // Dedupe service→service edges. Without this, a service that
    // references another via three env vars (API_URL, API_HOST,
    // API_PORT) renders as three parallel lines + label spam. One
    // edge per (source, target) pair is the right model — the
    // "direct link" is the dependency, not each individual var.
    // Build a host→FQN map so env-var values resolved to ${{ svc.PUBLIC_URL }}
    // (e.g. "https://kuso-demo-todo-api.hui.kuso.sislelabs.com") still
    // produce a service→service edge. Custom domains AND each service's
    // production env host are candidates. Sort hosts longest-first so
    // a longer custom-domain match wins over a sub-domain prefix of
    // the same string.
    const hostToFqn = new Map<string, string>();
    services.forEach((s) => {
      const fqn = s.metadata.name;
      for (const d of s.spec.domains ?? []) {
        if (d?.host) hostToFqn.set(d.host, fqn);
      }
    });
    // envNameToSvc maps a KusoEnvironment CR name → its service FQN.
    // The in-cluster Service for an env is named after the ENV CR
    // (e.g. "distill-api-production"), not the service ("distill-api"),
    // so an env-var like
    //   WEB_INTERNAL_BASE_URL = http://distill-api-production.kuso.svc...
    // resolves via this map back to the service node. Without it the
    // DNS regex captured "distill-api-production", failed the
    // service-name lookup, and no service→service edge was drawn.
    const envNameToSvc = new Map<string, string>();
    envs.forEach((e) => {
      if (e.metadata?.name && e.spec.service) {
        envNameToSvc.set(e.metadata.name, e.spec.service);
      }
      if (!e.spec.host) return;
      // Every env contributes its host → FQN mapping (not just
      // production). Pre-fix this was production-only, so a service
      // in a non-prod env-group whose env-vars referenced its
      // sibling's prod URL never produced an edge: the prod URL
      // resolved to the prod FQN, which isn't in the current
      // (env-filtered) services list, so the edge target lookup
      // failed. With the test-env's host also in the map, both
      // halves of the lookup come back with the env-scoped FQN
      // and the edge lights up.
      hostToFqn.set(e.spec.host, e.spec.service);
    });
    const sortedHosts = [...hostToFqn.keys()].sort((a, b) => b.length - a.length);

    const seenRefEdges = new Set<string>();
    services.forEach((s) => {
      const ownFqn = s.metadata.name;
      for (const ev of s.spec.envVars ?? []) {
        if (!ev?.value) continue;
        let targetFqn: string | undefined;
        // 1. In-cluster DNS form (HOST / URL / INTERNAL_URL):
        //    "<host>.<ns>.svc.cluster.local". <host> is the env CR's
        //    name (e.g. "distill-api-production"), NOT the service FQN.
        //    Try a direct service-name match first (covers any future
        //    service-named Service), then fall back to the env-name →
        //    service map.
        const dns = ev.value.match(/([a-z0-9-]+)\.[a-z0-9-]+\.svc\.cluster\.local/);
        if (dns) {
          if (services.some((t) => t.metadata.name === dns[1])) {
            targetFqn = dns[1];
          } else {
            const viaEnv = envNameToSvc.get(dns[1]);
            if (viaEnv && services.some((t) => t.metadata.name === viaEnv)) {
              targetFqn = viaEnv;
            }
          }
        }
        // 2. Public-URL form: any of the known public hosts appears
        //    in the value (typically the whole value, e.g.
        //    "https://api.proj.example.com" — but a manually-typed
        //    health probe URL like ".../healthz" should also match).
        if (!targetFqn) {
          for (const h of sortedHosts) {
            if (ev.value.includes(h)) {
              targetFqn = hostToFqn.get(h);
              break;
            }
          }
        }
        if (!targetFqn) continue;
        if (targetFqn === ownFqn) continue;
        const edgeKey = `${targetFqn}->${ownFqn}`;
        if (seenRefEdges.has(edgeKey)) continue;
        seenRefEdges.add(edgeKey);
        out.push({
          id: `eref:${edgeKey}`,
          source: `svc:${targetFqn}`,
          target: `svc:${ownFqn}`,
          animated: true,
          data: { kind: "ref" },
          // Colour: accent (purple). Service-ref edges are direct
          // dependencies — distinct from addon mounts (amber).
          style: { stroke: "var(--accent)", strokeWidth: 1.5, opacity: 0.85 },
        });
      }
    });
    return out;
  }, [project, services, addons, envs]);

  const [nodes, setNodes] = useState<Node[]>([]);
  const [edges, setEdges] = useState<Edge[]>(initialEdges);
  const [ctx, setCtx] = useState<ContextState>({ open: false, x: 0, y: 0, items: [] });
  // Add-addon + Add-cron dialogs. Live next to ctx because all
  // three are short-lived canvas-scoped overlays — no point hoisting
  // up to the page view.
  const [showAddAddon, setShowAddAddon] = useState(false);
  const [showAddCron, setShowAddCron] = useState(false);
  // Selected cron for the edit overlay. null = no overlay open.
  // Stored as the FQ name + spec snapshot so the overlay survives a
  // background refetch (we re-resolve from the live list on each
  // render so edits show through).
  const [editCronName, setEditCronName] = useState<string | null>(null);
  // Pending delete-confirm. Set when the user picks "Delete service"
  // from the right-click menu; the ConfirmDialog renders below; on
  // confirm we run the API + optimistically yank the node out of
  // state so the canvas feels instant. Replaces the old window.confirm
  // pattern which was both ugly and slow (modal-blocked event loop +
  // wait for refetch before the node disappears).
  const [confirmDelete, setConfirmDelete] = useState<{
    kind: "service" | "addon";
    short: string;
    nodeId: string;
  } | null>(null);
  const [deleting, setDeleting] = useState(false);

  // Edge category visibility. The two kinds today:
  //   - addon: the project's addon-conn Secret is mounted on every
  //            service via envFrom. Real direct dep, not "indirect."
  //   - ref:   service references another service via env-var refs
  //            like ${{api.URL}} → ends up as a literal DNS string.
  // Defaults to both ON; toggling lets you de-clutter dense projects.
  const [edgeFilters, setEdgeFilters] = useState<{ addon: boolean; ref: boolean }>({
    addon: true,
    ref: true,
  });

  const trigger = useTriggerBuild(project, "");
  const qc = useQueryClient();

  // Keyboard shortcuts on the canvas. Convention is single-letter,
  // unmodified, fired only when no input/textarea has focus and no
  // dialog/overlay is on top (we use document.body as the focus
  // sentinel — anything more interesting steals focus into itself).
  //
  //   j / arrow-down  : focus next service
  //   k / arrow-up    : focus prev service
  //   enter           : open the focused service
  //   r               : redeploy focused service
  //   l               : open focused service's logs tab
  //   v               : open variables tab
  //   s               : open settings tab
  //   ?               : flash a help toast listing all of the above
  const [focusIdx, setFocusIdx] = useState(-1);
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      // Don't hijack typing in inputs / textareas / contenteditables.
      const target = e.target as HTMLElement | null;
      if (target) {
        const tag = target.tagName;
        if (tag === "INPUT" || tag === "TEXTAREA" || tag === "SELECT") return;
        if (target.isContentEditable) return;
      }
      // Ignore when modifiers are held — those are reserved for
      // browser shortcuts (cmd-K opens the palette, cmd-R reloads).
      if (e.metaKey || e.ctrlKey || e.altKey) return;
      const svcNodes = nodes.filter((n) => n.type === "service");
      if (svcNodes.length === 0) return;
      const moveFocus = (delta: number) => {
        e.preventDefault();
        setFocusIdx((cur) => {
          const next = cur < 0 ? 0 : (cur + delta + svcNodes.length) % svcNodes.length;
          return next;
        });
      };
      const focused = focusIdx >= 0 && focusIdx < svcNodes.length ? svcNodes[focusIdx] : null;
      const focusedShort = focused
        ? serviceShortName(project, (focused.data as ServiceNodeData).service.metadata.name)
        : null;
      switch (e.key) {
        case "j":
        case "ArrowDown":
          moveFocus(1);
          break;
        case "k":
        case "ArrowUp":
          moveFocus(-1);
          break;
        case "Enter":
          if (focusedShort) {
            e.preventDefault();
            onSelectService?.(focusedShort);
          }
          break;
        case "l":
          if (focusedShort) {
            e.preventDefault();
            onSelectService?.(focusedShort, "logs");
          }
          break;
        case "v":
          if (focusedShort) {
            e.preventDefault();
            onSelectService?.(focusedShort, "variables");
          }
          break;
        case "s":
          if (focusedShort) {
            e.preventDefault();
            onSelectService?.(focusedShort, "settings");
          }
          break;
        case "r":
          if (focused && focusedShort) {
            e.preventDefault();
            const data = focused.data as ServiceNodeData;
            void callTrigger(data.project, focusedShort, trigger).then(
              () => toast.success(`Build triggered for ${focusedShort}`),
              (err) =>
                toast.error(err instanceof Error ? err.message : "Failed to trigger build"),
            );
          }
          break;
        case "?":
          e.preventDefault();
          toast.message("Canvas shortcuts", {
            description:
              "j/k or ↑/↓ — focus prev/next · enter — open · l — logs · v — vars · s — settings · r — redeploy",
            duration: 8_000,
          });
          break;
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [nodes, focusIdx, project, onSelectService, trigger]);

  useEffect(() => {
    if (!project) return;
    const stored = loadStoredLayout(project);
    const laid = autoLayout(initialNodes, initialEdges);
    setNodes(applyStoredLayout(laid, stored));
    setEdges(initialEdges);
  }, [project, initialNodes, initialEdges]);

  const onNodesChange: OnNodesChange = (changes) => {
    setNodes((prev) => {
      const next = applyNodeChanges(changes, prev);
      // Persist on any position change. We used to gate on
      // `dragging === false`, but React Flow emits position changes
      // where `dragging` is undefined (e.g. programmatic moves, drop
      // events on some browsers), and those slipped through silently
      // — the user moved a node, the state updated, but localStorage
      // never got the write. Save unconditionally on type=position;
      // the cost is one localStorage.setItem per drag tick, which is
      // negligible for a graph of this size.
      const positionChanged = changes.some((c) => c.type === "position");
      if (positionChanged) {
        const layout: Record<string, { x: number; y: number }> = {};
        next.forEach((n) => {
          layout[n.id] = n.position;
        });
        saveStoredLayout(project, layout);
      }
      return next;
    });
  };

  // Drag a wire from one node's right-handle (source) onto another's
  // left-handle (target). The connection is materialised as an env var
  // on the target service — the canvas's edge-derivation auto-redraws
  // the line on the next refetch, so we don't need to track the edge
  // in local state.
  const onConnect = async (c: Connection) => {
    if (!c.source || !c.target) return;
    const planResult = planConnection({
      project,
      sourceId: c.source,
      targetId: c.target,
      services,
      addons,
    });
    if (!planResult.ok) {
      toast.error(planResult.reason);
      return;
    }
    const { plan } = planResult;
    try {
      // Read-modify-write the target's envVars so we don't clobber
      // anything else. getServiceEnv returns secret-backed vars in
      // their valueFrom form; we re-send them untouched.
      const current = await getServiceEnv(project, plan.targetService);
      const existing = current.envVars ?? [];
      const sameName = existing.find((v) => v.name === plan.varName);
      if (sameName) {
        const sameValue = sameName.value === plan.varValue;
        if (sameValue) {
          toast.info(`Already connected: ${plan.summary}`);
          return;
        }
        toast.error(
          `${plan.varName} is already set on ${plan.targetService} with a different value — edit it manually.`
        );
        return;
      }
      const next = [
        ...existing,
        { name: plan.varName, value: plan.varValue },
      ];
      await setServiceEnv(project, plan.targetService, next);
      toast.success(`Connected ${plan.summary} (${plan.varName})`);
      // Refetch services so the new env var lands in props +
      // edge-derivation re-runs.
      qc.invalidateQueries({ queryKey: servicesQueryKey(project) });
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "Connect failed");
    }
  };

  const onNodeClick: NodeMouseHandler = (_e, node) => {
    if (node.type === "service" && onSelectService) {
      const data = node.data as ServiceNodeData;
      onSelectService(serviceShortName(data.project, data.service.metadata.name));
    } else if (node.type === "addon" && onSelectAddon) {
      const data = node.data as AddonNodeData;
      onSelectAddon(data.addon.metadata.name);
    } else if (node.type === "cron") {
      // Cron click opens the inline edit overlay. We pass the CR
      // name through state and re-resolve from allCrons on render
      // so the overlay reflects any background refetch.
      const data = node.data as unknown as CronNodeData;
      setEditCronName(data.cron.metadata.name);
    }
  };

  // Right-click on a service node — Open / View logs / Trigger build / Delete.
  const onServiceContext = (
    e: React.MouseEvent,
    data: ServiceNodeData
  ) => {
    e.preventDefault();
    const short = serviceShortName(data.project, data.service.metadata.name);
    const env = envs.find(
      (x) =>
        x.spec.service === data.service.metadata.name && x.spec.kind === "production"
    );
    const url = env?.status?.url as string | undefined;

    const runtime = data.service.spec.runtime || "dockerfile";
    const items: ContextMenuEntry[] = [
      {
        id: "header",
        kind: "header",
        label: short,
        subtitle: `service · runtime=${runtime}`,
        icon: Eye,
      },
      {
        id: "open",
        label: "Open inspector",
        icon: Eye,
        shortcut: "Enter",
        onSelect: () => onSelectService?.(short),
      },
      ...(url
        ? [
            {
              id: "open-url",
              label: "Open URL ↗",
              icon: ExternalLink,
              onSelect: () => window.open(url, "_blank", "noopener"),
            } as ContextMenuItem,
          ]
        : []),
      { id: "sep1", kind: "separator" },
      {
        id: "logs",
        label: "View logs",
        icon: ScrollText,
        onSelect: () => {
          onSelectService?.(short, "logs");
        },
      },
      {
        id: "trigger",
        label: "Trigger build",
        icon: RotateCcw,
        onSelect: async () => {
          try {
            await callTrigger(data.project, short, trigger);
            toast.success(`Build triggered for ${short}`);
          } catch (err) {
            toast.error(err instanceof Error ? err.message : "Failed to trigger build");
          }
        },
      },
      { id: "sep2", kind: "separator" },
      {
        id: "delete",
        label: "Delete service…",
        icon: Trash2,
        destructive: true,
        onSelect: () =>
          setConfirmDelete({
            kind: "service",
            short,
            nodeId: `svc:${data.service.metadata.name}`,
          }),
      },
    ];
    setCtx({ open: true, x: e.clientX, y: e.clientY, items });
  };

  // Right-click on an addon node — Open / Delete. Delete sends the
  // user into the overlay's Settings tab where the typed-name
  // confirm gate lives; we don't trust window.confirm for a
  // destructive op that may take a PVC with it.
  const onAddonContext = (e: React.MouseEvent, data: AddonNodeData) => {
    e.preventDefault();
    const addonShort = data.addon.metadata.name.replace(project + "-", "");
    const kind = data.addon.spec.kind;
    const items: ContextMenuEntry[] = [
      {
        id: "header",
        kind: "header",
        label: addonShort,
        subtitle: `addon · ${kind}`,
      },
      {
        id: "open",
        label: "Open inspector",
        icon: Eye,
        shortcut: "Enter",
        onSelect: () => onSelectAddon?.(data.addon.metadata.name),
      },
      ...(kind === "postgres"
        ? [
            {
              id: "sql",
              label: "SQL console",
              icon: Database,
              onSelect: () => {
                onSelectAddon?.(data.addon.metadata.name, "sql");
              },
            } as ContextMenuItem,
            {
              id: "backups",
              label: "Backups + restore",
              icon: HardDrive,
              onSelect: () => {
                onSelectAddon?.(data.addon.metadata.name, "backups");
              },
            } as ContextMenuItem,
          ]
        : []),
      { id: "sep1", kind: "separator" },
      {
        id: "delete",
        label: "Delete addon…",
        icon: Trash2,
        destructive: true,
        onSelect: () =>
          setConfirmDelete({
            kind: "addon",
            short: data.addon.metadata.name,
            nodeId: `addon:${data.addon.metadata.name}`,
          }),
      },
    ];
    setCtx({ open: true, x: e.clientX, y: e.clientY, items });
  };

  // Right-click on empty pane — Add service / Add addon / Reset layout.
  const onPaneContext = (e: React.MouseEvent) => {
    e.preventDefault();
    const items: ContextMenuEntry[] = [
      {
        id: "header",
        kind: "header",
        label: project,
        subtitle: "project canvas",
        icon: LayoutGrid,
      },
      {
        id: "add-service",
        label: "Add service",
        icon: Plus,
        onSelect: () => {
          window.location.href = `/projects/${encodeURIComponent(project)}/services/new`;
        },
      },
      {
        id: "add-addon",
        label: "Add addon",
        icon: Plus,
        onSelect: () => setShowAddAddon(true),
      },
      {
        id: "add-cron",
        label: "Add cron",
        icon: Plus,
        onSelect: () => setShowAddCron(true),
      },
      { id: "sep1", kind: "separator" },
      {
        id: "project-settings",
        label: "Project settings",
        icon: SettingsIcon,
        onSelect: () => {
          window.location.href = `/projects/${encodeURIComponent(project)}/settings`;
        },
      },
      {
        id: "reset-layout",
        label: "Reset layout",
        icon: LayoutGrid,
        onSelect: () => {
          saveStoredLayout(project, {});
          const laid = autoLayout(initialNodes, initialEdges);
          setNodes(laid);
        },
      },
    ];
    setCtx({ open: true, x: e.clientX, y: e.clientY, items });
  };

  return (
    <div
      className="relative flex-1 min-h-0 w-full"
      onContextMenuCapture={(e) => {
        // React Flow's nodeTypes render their own divs; if a node was
        // right-clicked the event already had its propagation stopped
        // by our node-level handler. Anything else that bubbles up
        // here is a pane context and we route it accordingly.
        const target = e.target as HTMLElement;
        if (target.closest("[data-node-context]")) return;
        onPaneContext(e);
      }}
    >
      <ReactFlow
        nodes={(() => {
          // Inject the keyboard-focused flag onto the matching service
          // node so ServiceNode can render a focus ring without the
          // shortcut handler having to know its DOM. The focus index
          // is over the *service-only* slice; map back to the full
          // nodes array via the same filter order.
          const svcOnlyOrder = nodes.filter((n) => n.type === "service");
          const focusedId =
            focusIdx >= 0 && focusIdx < svcOnlyOrder.length
              ? svcOnlyOrder[focusIdx].id
              : null;
          return nodes.map((n) => ({
            ...n,
            data: {
              ...n.data,
              __focused: focusedId === n.id,
              __onContext:
                n.type === "service"
                  ? (e: React.MouseEvent) =>
                      onServiceContext(e, n.data as ServiceNodeData)
                  : (e: React.MouseEvent) =>
                      onAddonContext(e, n.data as AddonNodeData),
            },
          }));
        })()}
        edges={edges.filter((e) => {
          // Hide categories the user toggled off. Edges without a
          // kind tag (legacy or future categories) are always shown
          // so an upgrade can't accidentally swallow them.
          const kind = (e.data as { kind?: string } | undefined)?.kind;
          if (kind === "addon" && !edgeFilters.addon) return false;
          if (kind === "ref" && !edgeFilters.ref) return false;
          return true;
        })}
        nodeTypes={nodeTypes}
        onNodesChange={onNodesChange}
        onNodeClick={onNodeClick}
        onConnect={onConnect}
        fitView
        fitViewOptions={{ padding: 0.25, maxZoom: 1, minZoom: 0.4 }}
        minZoom={0.2}
        maxZoom={1.5}
        proOptions={{ hideAttribution: true }}
        // Snap to the same 24px grid the dot Background uses so
        // dragged nodes land on dots, not between them. Visually
        // self-explanatory — the user immediately understands
        // the alignment they're being given.
        snapToGrid
        snapGrid={[24, 24]}
      >
        {/* Brighter dots than the default border-subtle so the
            grid actually reads on the dark bg. zinc-500 at 40%
            opacity-equivalent — visible without being noisy. */}
        <Background gap={24} size={1.5} color="rgb(113 113 122 / 0.55)" />
        <Controls className="!bg-[var(--bg-elevated)] !border-[var(--border-subtle)]" />
        <EdgeControlsPanel
          filters={edgeFilters}
          setFilters={setEdgeFilters}
        />
      </ReactFlow>

      <CanvasContextMenu
        open={ctx.open}
        x={ctx.x}
        y={ctx.y}
        items={ctx.items}
        onClose={() => setCtx((c) => ({ ...c, open: false }))}
      />

      <AddAddonDialog
        project={project}
        open={showAddAddon}
        onClose={() => setShowAddAddon(false)}
      />

      <AddCronDialog
        project={project}
        open={showAddCron}
        onClose={() => setShowAddCron(false)}
      />

      <EditCronDialog
        project={project}
        cron={
          editCronName
            ? (allCrons.data ?? []).find((c) => c.metadata.name === editCronName) ?? null
            : null
        }
        onClose={() => setEditCronName(null)}
      />

      <ConfirmDialog
        open={!!confirmDelete}
        title={
          confirmDelete?.kind === "service"
            ? `Delete service ${confirmDelete?.short}`
            : `Delete addon ${confirmDelete?.short}`
        }
        body={
          confirmDelete?.kind === "service" ? (
            <>
              This <strong>cascades</strong> to every environment of{" "}
              <span className="font-mono text-[var(--text-primary)]">
                {confirmDelete?.short}
              </span>{" "}
              and tears down running pods. The git repo is untouched.
            </>
          ) : (
            <>
              Drops the helm release for{" "}
              <span className="font-mono text-[var(--text-primary)]">
                {confirmDelete?.short}
              </span>
              . The PVC + data go with it unless your storage class retains it.
            </>
          )
        }
        typeToConfirm={confirmDelete?.short}
        confirmLabel={
          confirmDelete?.kind === "service" ? "Delete service" : "Delete addon"
        }
        pending={deleting}
        onCancel={() => setConfirmDelete(null)}
        onConfirm={async () => {
          if (!confirmDelete) return;
          const { short, nodeId, kind } = confirmDelete;
          setDeleting(true);

          // Optimistic: pull the node out of state immediately. The
          // refetch will confirm; if the API call fails we restore.
          // Keep a snapshot of the previous state for rollback.
          const prevNodes = nodes;
          setNodes((cur) => cur.filter((n) => n.id !== nodeId));
          // Close the modal on the same tick so the canvas renders
          // the removal without the user staring at a "Working…"
          // button.
          setConfirmDelete(null);

          try {
            if (kind === "service") {
              const { deleteService } = await import("@/features/services/api");
              await deleteService(project, short);
            } else {
              const { deleteAddon } = await import("@/features/projects/api");
              await deleteAddon(project, short);
            }
            toast.success(
              kind === "service"
                ? `Service ${short} deleted`
                : `Addon ${short} deleted`
            );
          } catch (err) {
            // Restore the node so the canvas matches reality.
            setNodes(prevNodes);
            toast.error(err instanceof Error ? err.message : "Failed to delete");
          } finally {
            setDeleting(false);
          }
        }}
      />
    </div>
  );
}

// callTrigger adapter: the trigger hook is bound to a placeholder
// service name (""), so the canvas re-binds to the actual service
// via the API directly. Keeps the canvas from spinning up N hooks
// per node.
async function callTrigger(
  project: string,
  service: string,
  _hint: ReturnType<typeof useTriggerBuild>
) {
  void _hint;
  const { triggerBuild } = await import("@/features/services/api");
  await triggerBuild(project, service, {});
}

// EdgeControlsPanel — bottom-right cluster that combines a legend
// (what each edge colour means) with the filter chips that toggle
// them. Single React Flow <Panel> so the two pieces sit on the same
// row and react-flow doesn't stack them. Without the legend the
// colours feel arbitrary; without the filter chips the user can't
// dim the noise on dependency-heavy projects.
function EdgeControlsPanel({
  filters,
  setFilters,
}: {
  filters: { addon: boolean; ref: boolean };
  setFilters: React.Dispatch<React.SetStateAction<{ addon: boolean; ref: boolean }>>;
}) {
  return (
    <Panel position="bottom-right" className="!m-3">
      <div className="flex items-center gap-2">
        <div className="flex items-center gap-3 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-elevated)] px-2.5 py-1.5 font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)] shadow-[var(--shadow-sm)]">
          <span className="inline-flex items-center gap-1.5">
            <span
              aria-hidden
              className="inline-block h-[2px] w-5"
              style={{ background: "rgb(245 158 11)" }}
            />
            addon mount
          </span>
          <span className="inline-flex items-center gap-1.5">
            <span
              aria-hidden
              className="inline-block h-[2px] w-5"
              style={{ background: "var(--accent)" }}
            />
            service ref
          </span>
        </div>
        <div className="flex items-center gap-1 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-elevated)] p-1 shadow-[var(--shadow-sm)]">
          <span className="px-2 font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]">
            edges
          </span>
          <FilterChip
            label="addon mounts"
            on={filters.addon}
            onClick={() => setFilters((f) => ({ ...f, addon: !f.addon }))}
          />
          <FilterChip
            label="service refs"
            on={filters.ref}
            onClick={() => setFilters((f) => ({ ...f, ref: !f.ref }))}
          />
        </div>
      </div>
    </Panel>
  );
}

function FilterChip({
  label,
  on,
  onClick,
}: {
  label: string;
  on: boolean;
  onClick: () => void;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      className={
        "rounded px-2 py-1 font-mono text-[10px] transition-colors " +
        (on
          ? "bg-[var(--accent-subtle)] text-[var(--accent)]"
          : "text-[var(--text-tertiary)] hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)]")
      }
    >
      {label}
    </button>
  );
}
