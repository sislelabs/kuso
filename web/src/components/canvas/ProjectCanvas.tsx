"use client";

import { useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import {
  ReactFlow,
  Background,
  BackgroundVariant,
  Controls,
  Panel,
  type Node,
  type Edge,
  type NodeMouseHandler,
  type OnNodesChange,
  type Connection,
  type ReactFlowInstance,
  type BuiltInEdge,
  ConnectionMode,
  applyNodeChanges,
} from "@xyflow/react";
import "@xyflow/react/dist/style.css";
import { useSkipTarget } from "@/lib/skip-targets";

import type { KusoAddon, KusoEnvironment, KusoService } from "@/types/projects";
import {
  getServiceEnv,
  setServiceEnv,
} from "@/features/services/api";
import { ServiceNode, type ServiceNodeData } from "./ServiceNode";
import { AddonNode, type AddonNodeData } from "./AddonNode";
import { CronNode, type CronNodeData } from "./CronNode";
import {
  applyStoredLayout,
  autoLayout,
  clearStoredLayout,
  loadStoredLayout,
  saveStoredLayout,
} from "./layout";
import {
  CanvasContextMenu,
  type ContextMenuItem,
  type ContextMenuEntry,
} from "./CanvasContextMenu";
import { planConnection } from "./connect";
import { facingSides, nextInDirection, readingOrder, type Box, type Direction } from "./geometry";
import { useLatestBuilds, useProjectCrons } from "@/features/services";
import { useServiceBadges } from "./useServiceBadges";
import { useCanvasDensity } from "./density";
import { CanvasShortcutsButton, useCanvasShortcuts } from "./CanvasShortcuts";
import { isTypingTarget } from "@/lib/shortcuts";
import { servicesQueryKey } from "@/features/projects";
import { invalidateProjectDescribe } from "@/features/projects/hooks";
import { anySurfaceOpen } from "@/lib/escape-layer";
import { AddAddonDialog } from "@/components/addon/AddAddonDialog";
import { AddCronDialog } from "@/components/cron/AddCronDialog";
import { EditCronDialog } from "@/components/cron/EditCronDialog";
import { ConfirmDialog } from "@/components/shared/ConfirmDialog";
import { cn, serviceShortName } from "@/lib/utils";
import { isProductionGroup } from "@/lib/env-group";
import { cronsForEnv } from "@/features/projects/facts";
import { buildTriggerMessage } from "@/features/services";
import { useCanOnProject, Perms } from "@/features/auth";
import {
  ExternalLink,
  ScrollText,
  RotateCcw,
  Trash2,
  Eye,
  Plus,
  LayoutGrid,
  Rows3,
  Database,
  HardDrive,
  Settings as SettingsIcon,
  Clock,
  Terminal,
  Square,
  Play,
} from "lucide-react";
import { toast } from "sonner";
import { toastError } from "@/lib/errors";
import { useRouter, useSearchParams } from "next/navigation";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";

const nodeTypes = {
  service: ServiceNode,
  addon: AddonNode,
  cron: CronNode,
};

interface Props {
  project: string;
  // Env-group scope the parent view is showing (production / staging /
  // preview-pr-N / custom). Drives the per-tile "latest build" query
  // so a preview env tile shows the PR-branch build, not main's
  // newest build (B1.2 from v0.17.0 audit, completed in v0.17.2 by
  // wiring the web side too).
  selectedEnv: string;
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
  selectedEnv,
  services,
  addons,
  envs,
  onSelectService,
  onSelectAddon,
}: Props) {
  // Latest build per service in this project, filtered to the
  // selected env-group's branch. The server (v0.17.1+) groups builds
  // by branch — production = the project's default branch, custom
  // env names match the branch name, preview-pr-N matches builds for
  // the PR's head ref. Without the env filter, a staging build that
  // landed seconds after main's build would render under production's
  // service tile and flicker between the two on each 5s poll tick.
  const latestBuilds = useLatestBuilds(project, selectedEnv);
  const allCrons = useProjectCrons(project);

  // Env-scoped addon visibility. Per-PR preview clones are named
  // Addon visibility per env is decided UPSTREAM in the project view
  // (addonsForEnv): it hides the shared base addons that the selected env
  // replaces with its own env-scoped clone (per-env addon provisioning), and
  // scopes env-labeled clones to their own tab. The canvas trusts that filtered
  // set so node rendering and edge wiring agree (a hidden addon never leaves a
  // dangling edge) and there's a single source of truth.
  const visibleAddons: KusoAddon[] = addons;

  // Error + vuln badge counts: one project-wide request each, shared
  // with the mobile cards so both surfaces show the same numbers.
  const { vulnSummaries, errorSummaries, errorHot } = useServiceBadges(project, selectedEnv, services);
  const [density, setDensity] = useCanvasDensity(project);
  useSkipTarget("canvas-root", "Skip to canvas");

  const initialNodes: Node[] = useMemo(() => {
    const out: Node[] = [];
    services.forEach((s) => {
      // Match by service short-name only. The `envs` slice has
      // already been filtered upstream to the selected env-group
      // (production / staging / preview-pr-N), so the first match
      // Prefer the production env when one exists — the canvas node
      // is the visual "this service" tile, not the per-env tile.
      // Without a preference, .find() returned whichever env was
      // indexed first (staging-then-production was the observed
      // order on tickero), so the chip URL showed staging.tickero.bg
      // for a service whose canonical URL is frontend.tickero.bg.
      //
      // Fallback to ANY env when there's no production one — covers
      // preview-only / staging-only setups.
      const allForService = envs.filter((e) => e.spec.service === s.metadata.name);
      // Select the production-GROUP env via the kuso.sislelabs.com/env
      // label — spec.kind==="production" also matches staging/custom
      // clones, so keying on it duplicated the tile with a clone's URL.
      const env =
        allForService.find((e) => isProductionGroup(e)) ??
        allForService[0];
      // serviceShortName strips the "<project>-" prefix the server
      // uses for the FQ name; the latest-builds endpoint returns the
      // map keyed by the same short name so direct lookup works.
      const shortName = serviceShortName(project, s.metadata.name);
      const latestBuild = latestBuilds.data?.[shortName];
      out.push({
        id: `svc:${s.metadata.name}`,
        type: "service",
        position: { x: 0, y: 0 },
        data: {
          project,
          service: s,
          env,
          latestBuild,
          vulns: vulnSummaries.services[shortName],
          errors: errorSummaries[shortName],
          errorsHot: errorHot[shortName],
          compact: density === "compact",
        } satisfies ServiceNodeData,
      });
    });
    visibleAddons.forEach((a) => {
      out.push({
        id: `addon:${a.metadata.name}`,
        type: "addon",
        position: { x: 0, y: 0 },
        data: {
          project,
          addon: a,
          vulns: vulnSummaries.addons[serviceShortName(project, a.metadata.name)],
        } satisfies AddonNodeData,
      });
    });
    cronsForEnv(allCrons.data ?? [], services, selectedEnv, project).forEach((c) => {
      out.push({
        id: `cron:${c.metadata.name}`,
        type: "cron",
        position: { x: 0, y: 0 },
        data: { project, cron: c } satisfies CronNodeData,
      });
    });
    return out;
  }, [project, services, visibleAddons, envs, latestBuilds.data, allCrons.data, vulnSummaries, errorSummaries, errorHot, selectedEnv, density]);

  const initialEdges: Edge[] = useMemo(() => {
    const out: Edge[] = [];
    // A service that has never had an image can't use the connection
    // yet: its edges draw dashed ("pending") until the first deploy.
    const notDeployed = (svcFQN: string) => {
      const env = envs.find((e) => e.spec.service === svcFQN);
      return !env || env.status?.state === "no_image";
    };
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
    services.forEach((s) => {
      for (const ev of s.spec.envVars ?? []) {
        // valueFrom path: server-resolved addon refs land here.
        const skr = (ev?.valueFrom as { secretKeyRef?: { name?: string } } | undefined)?.secretKeyRef;
        if (skr?.name && skr.name.endsWith("-conn")) {
          // Conn-secret naming is "<addon-cr-name>-conn" — strip the
          // suffix and match against known addons.
          const addonFQN = skr.name.slice(0, -"-conn".length);
          if (visibleAddons.some((a) => a.metadata.name === addonFQN)) {
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
              if (visibleAddons.some((a) => a.metadata.name === c)) {
                explicitUsedAddons.add(`${s.metadata.name}|${c}`);
                break;
              }
            }
          }
        }
      }
    });
    // Addon-conn auto-mount edges. Kuso auto-mounts every addon's
    // <addon>-conn secret onto every env via envFromSecrets. That
    // means apps that read e.g. DATABASE_URL from process.env have
    // an implicit-but-real dependency on the addon, with no
    // ${{ }} ref to detect. We surface those edges as dashed lines
    // so the user sees "pg is wired to api" without the explicit-
    // ref's solid line confusing them about whether it's referenced
    // by name.
    //
    // Deduped per (service, addon) — we look at any one env's
    // envFromSecrets, not all of them, so a 5-service / 5-addon
    // project draws at most one edge per addon→service pair
    // instead of N² across envs.
    const implicitUsedAddons = new Set<string>();
    const seenSvcAddon = new Set<string>();
    envs.forEach((e) => {
      const svcFQN = e.spec.service;
      const secrets = e.spec.envFromSecrets ?? [];
      for (const secretName of secrets) {
        if (!secretName.endsWith("-conn")) continue;
        const addonFQN = secretName.slice(0, -"-conn".length);
        if (!visibleAddons.some((a) => a.metadata.name === addonFQN)) continue;
        const dedupeKey = `${svcFQN}|${addonFQN}`;
        if (seenSvcAddon.has(dedupeKey)) continue;
        seenSvcAddon.add(dedupeKey);
        if (!explicitUsedAddons.has(dedupeKey)) {
          implicitUsedAddons.add(dedupeKey);
        }
      }
    });
    explicitUsedAddons.forEach((key) => {
      const [svcFQN, addonFQN] = key.split("|");
      out.push({
        id: `e:${addonFQN}->${svcFQN}`,
        source: `addon:${addonFQN}`,
        target: `svc:${svcFQN}`,
        // `data` is ours, not React Flow's: the filter chips and
        // styleEdge read the category from it. Styling happens at
        // render time so hover/selection can light edges up.
        data: { kind: "addon", pending: notDeployed(svcFQN) } satisfies EdgeData,
      });
    });
    implicitUsedAddons.forEach((key) => {
      const [svcFQN, addonFQN] = key.split("|");
      out.push({
        id: `e:auto:${addonFQN}->${svcFQN}`,
        source: `addon:${addonFQN}`,
        target: `svc:${svcFQN}`,
        data: { kind: "addon", implicit: true, pending: notDeployed(svcFQN) } satisfies EdgeData,
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

    // Helper: try to match an env var entry (by name OR string value)
    // to a sibling service. Returns the target service FQN, or
    // undefined. Used both for service-spec envVars AND the env-CR's
    // expanded envVars (which include valueFrom.secretKeyRef entries
    // for subscribed shared-secret keys we can't peek into directly).
    const inferTargetFromEnvVar = (name: string, value: string | undefined): string | undefined => {
      // 1. In-cluster DNS form (HOST / URL / INTERNAL_URL):
      //    "<host>.<ns>.svc.cluster.local". <host> is the env CR's
      //    name (e.g. "distill-api-production"), NOT the service FQN.
      //    Try a direct service-name match first (covers any future
      //    service-named Service), then fall back to the env-name →
      //    service map.
      if (value) {
        const dns = value.match(/([a-z0-9-]+)\.[a-z0-9-]+\.svc\.cluster\.local/);
        if (dns) {
          if (services.some((t) => t.metadata.name === dns[1])) {
            return dns[1];
          }
          const viaEnv = envNameToSvc.get(dns[1]);
          if (viaEnv && services.some((t) => t.metadata.name === viaEnv)) {
            return viaEnv;
          }
        }
        // 2. Public-URL form: any of the known public hosts appears
        //    in the value (typically the whole value, e.g.
        //    "https://api.proj.example.com" — but a manually-typed
        //    health probe URL like ".../healthz" should also match).
        for (const h of sortedHosts) {
          if (value.includes(h)) {
            return hostToFqn.get(h);
          }
        }
      }
      // 3. Key-name heuristic. When the value lives in a secret we
      //    can't read (shared-secret subscriptions, per-env override
      //    secrets), inferring from the value is impossible — but the
      //    KEY name is observable and most apps follow the convention
      //    `<SERVICE>_URL`. Match the leading SERVICE token (case-
      //    insensitive) against sibling service short names.
      //
      // Strict matching: require token === short OR token ends with
      // "_<short>". Bare substring suffix (e.g. "backend".endsWith
      // ("end") for service named "end") was the v0.17.0 false-
      // positive that drew spurious edges (B4.3 from the audit).
      const urlMatch = name.match(/^([A-Z][A-Z0-9]*?)_(URL|HOST|BASE_URL|API_URL|INTERNAL_URL|PUBLIC_URL)$/);
      if (urlMatch) {
        const token = urlMatch[1].toLowerCase();
        const candidates = services
          .map((svc) => ({
            fqn: svc.metadata.name,
            short: serviceShortName(project, svc.metadata.name),
          }))
          .filter((c) => token === c.short || token.endsWith("_" + c.short))
          .sort((a, b) => b.short.length - a.short.length);
        if (candidates.length > 0) return candidates[0].fqn;
      }
      return undefined;
    };

    const seenRefEdges = new Set<string>();
    // Build a quick lookup from service FQN → that service's env CR
    // (for the active env-group, since envs is already filtered).
    // We scan both the service spec envVars AND the env CR's envVars
    // — the latter carries valueFrom.secretKeyRef entries that the
    // service spec never sees (subscribed shared-secret keys), and
    // those drive the API_URL → api edge we'd otherwise miss.
    const envBySvcFqn = new Map<string, KusoEnvironment>();
    envs.forEach((e) => {
      if (e.spec.service) envBySvcFqn.set(e.spec.service, e);
    });
    // Recognize shared-secret names so we can SKIP env-name-heuristic
    // matching on valueFrom entries pointing at them. Without this
    // skip, a subscribed key like API_URL whose valueFrom points at
    // <project>-shared draws a false-positive edge to a sibling
    // service named "api" — but the value is a generic URL the user
    // configured in the shared secret, not necessarily a
    // service-to-service call (B4.4 from v0.17.0 audit).
    const sharedSecretNames = new Set([
      `${project}-shared`,
      "kuso-instance-shared",
    ]);
    services.forEach((s) => {
      const ownFqn = s.metadata.name;
      const allEnvVars: { name: string; value?: string }[] = [];
      for (const ev of s.spec.envVars ?? []) {
        if (!ev?.name) continue;
        // Skip shared-secret-backed entries when applying the
        // key-name heuristic — see comment above.
        const ref = (ev.valueFrom as { secretKeyRef?: { name?: string } } | undefined)?.secretKeyRef;
        if (ref?.name && sharedSecretNames.has(ref.name)) continue;
        allEnvVars.push({ name: ev.name, value: ev.value });
      }
      const envCR = envBySvcFqn.get(ownFqn);
      for (const ev of envCR?.spec.envVars ?? []) {
        if (!ev?.name) continue;
        const ref = (ev.valueFrom as { secretKeyRef?: { name?: string } } | undefined)?.secretKeyRef;
        if (ref?.name && sharedSecretNames.has(ref.name)) continue;
        allEnvVars.push({ name: ev.name, value: ev.value });
      }
      for (const ev of allEnvVars) {
        const targetFqn = inferTargetFromEnvVar(ev.name, ev.value);
        if (!targetFqn) continue;
        if (targetFqn === ownFqn) continue;
        const edgeKey = `${targetFqn}->${ownFqn}`;
        if (seenRefEdges.has(edgeKey)) continue;
        seenRefEdges.add(edgeKey);
        out.push({
          id: `eref:${edgeKey}`,
          source: `svc:${targetFqn}`,
          target: `svc:${ownFqn}`,
          data: { kind: "ref", pending: notDeployed(ownFqn) } satisfies EdgeData,
        });
      }
    });
    return out;
  }, [project, services, visibleAddons, envs]);

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
  // Pending stop-confirm. Set when the user picks "Stop service" from
  // the right-click menu; the ConfirmDialog below warns that stopping
  // takes the service offline (503) before the API call fires.
  const [confirmStop, setConfirmStop] = useState<{
    short: string;
    nodeId: string;
  } | null>(null);
  const [stopping, setStopping] = useState(false);
  // Pending redeploy-confirm for the `r` key and the context menu, so
  // those entry points ask first like the Deployments tab does.
  const [confirmRedeploy, setConfirmRedeploy] = useState<string | null>(null);
  const [redeploying, setRedeploying] = useState(false);
  // Branch of the redeploy target's env in the selected group. Without it
  // the server builds the project's default branch, so a staging-tab
  // redeploy of a shared service would rebuild production.
  const redeployBranch = confirmRedeploy
    ? envs.find((e) => serviceShortName(project, e.spec.service) === confirmRedeploy)?.spec.branch
    : undefined;

  // ?add=addon|cron opens the matching dialog (the ⌘K palette links
  // here). Strip the param afterwards so a reload doesn't reopen it.
  const router = useRouter();
  const search = useSearchParams();
  const addParam = search?.get("add");
  useEffect(() => {
    if (addParam !== "addon" && addParam !== "cron") return;
    if (addParam === "addon") setShowAddAddon(true);
    else setShowAddCron(true);
    const next = new URLSearchParams(search?.toString() ?? "");
    next.delete("add");
    const qs = next.toString();
    router.replace(`/projects/${encodeURIComponent(project)}${qs ? `?${qs}` : ""}`);
  }, [addParam, search, router, project]);

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

  const qc = useQueryClient();

  // The canvas is one tab stop: the active node holds tabIndex 0 and
  // arrow keys rove between nodes by geometry. Clicking or tabbing onto
  // a node makes it active; j/k/l/v/s/r act on it.
  const [activeId, setActiveId] = useState<string | null>(null);
  // DOM focus is tracked separately so a focused node lights its edges
  // like a hovered one does.
  const [focusedId, setFocusedId] = useState<string | null>(null);

  // Edges sit quietly until you point at (or select) a node; then that
  // node's connections light up in the accent and the rest step back.
  const [hoveredNode, setHoveredNode] = useState<string | null>(null);
  const hotNodes = useMemo(() => {
    const ids = new Set(nodes.filter((n) => n.selected).map((n) => n.id));
    if (hoveredNode) ids.add(hoveredNode);
    if (focusedId) ids.add(focusedId);
    return ids;
  }, [nodes, hoveredNode, focusedId]);

  // Project-scoped write capabilities. The server enforces these
  // (403), but surfacing them here keeps viewers from clicking
  // trigger/stop/delete/create controls that can only end in an
  // error toast. Mirrors permsForProjectRole in features/auth —
  // viewers get read-only, editors+ get the write set.
  const canServicesWrite = useCanOnProject(project, Perms.ServicesWrite);
  const canAddonsWrite = useCanOnProject(project, Perms.AddonsWrite);
  // Service crons/runs mount the service's secrets, so creating one is
  // admin-only (secrets:read) server-side.
  const canCreateJobs = useCanOnProject(project, Perms.SecretsRead);

  // editorGate: context-menu affordance for gated items. Disabled
  // items stay visible (discoverability) with a right-aligned hint
  // in the shortcut slot — the menu rows have no tooltip surface.
  const editorGate = (can: boolean) =>
    can ? {} : { disabled: true, shortcut: "needs editor" };

  // Keyboard on the canvas. "?" is global; the Canvas group is
  // registered so the help dialog lists these keys (CANVAS_SHORTCUTS).
  useCanvasShortcuts();
  const rf = useRef<ReactFlowInstance<Node, BuiltInEdge> | null>(null);
  const wrapperRef = useRef<HTMLDivElement>(null);
  // Set once the user pans or zooms; from then on nothing refits.
  const userMovedRef = useRef(false);
  const boxes = useMemo(() => nodes.map(nodeBox), [nodes]);
  const boxById = useMemo(() => new Map(boxes.map((b) => [b.id, b])), [boxes]);
  const order = useMemo(() => readingOrder(boxes).map((b) => b.id), [boxes]);
  const tabStopId = activeId && nodes.some((n) => n.id === activeId) ? activeId : (order[0] ?? null);
  const openNodeRef = useRef<(id: string) => void>(() => {});

  const focusNode = (id: string) => {
    setActiveId(id);
    const el = wrapperRef.current?.querySelector<HTMLElement>(`[data-canvas-node="${CSS.escape(id)}"]`);
    el?.focus({ preventScroll: true });
    // Keep the node on screen without changing the zoom the user chose.
    const box = boxes.find((b) => b.id === id);
    const inst = rf.current;
    const wrap = wrapperRef.current;
    if (!box || !inst || !wrap) return;
    const { x, y, zoom } = inst.getViewport();
    const left = box.x * zoom + x;
    const top = box.y * zoom + y;
    const margin = 24;
    const offscreen =
      left < margin ||
      top < margin ||
      left + box.w * zoom > wrap.clientWidth - margin ||
      top + box.h * zoom > wrap.clientHeight - margin;
    if (offscreen) {
      userMovedRef.current = true;
      void inst.setCenter(box.x + box.w / 2, box.y + box.h / 2, { zoom, duration: reducedMotion() ? 0 : 200 });
    }
  };

  // Keys pressed while a node (or something inside one) has focus.
  const onCanvasKeyDown = (e: React.KeyboardEvent) => {
    if (e.metaKey || e.ctrlKey || e.altKey || e.defaultPrevented) return;
    const target = e.target instanceof HTMLElement ? e.target : null;
    const nodeEl = target?.closest<HTMLElement>("[data-canvas-node]");
    const id = nodeEl?.dataset.canvasNode;
    if (!nodeEl || !id) return;
    const onNode = target === nodeEl;
    if (e.key === "Escape" && !onNode) {
      // Esc from a control inside a node hands focus back to the node.
      e.preventDefault();
      nodeEl.focus();
      return;
    }
    if (!onNode) return;
    const dir = ARROW_DIR[e.key];
    if (dir) {
      e.preventDefault();
      const next = nextInDirection(boxes, id, dir);
      if (next) focusNode(next);
      return;
    }
    if (e.key === "Enter" || e.key === " ") {
      e.preventDefault();
      openNodeRef.current(id);
    } else if (e.key === "ContextMenu" || (e.shiftKey && e.key === "F10")) {
      // The node's actions menu, opened at the node like a right-click.
      e.preventDefault();
      const r = nodeEl.getBoundingClientRect();
      nodeEl.dispatchEvent(
        new MouseEvent("contextmenu", { bubbles: true, cancelable: true, clientX: r.left + 16, clientY: r.top + 16 }),
      );
    } else if (e.key === "Home" || e.key === "End") {
      e.preventDefault();
      const to = e.key === "Home" ? order[0] : order[order.length - 1];
      if (to) focusNode(to);
    }
  };

  // Single-letter shortcuts, fired only when no input has focus and no
  // dialog/overlay is on top. j/k work from anywhere on the page (they
  // pull focus into the canvas); l/v/s/r act on the active service.
  // Time of the last bare "g": the next key belongs to a global g-chord
  // (g s = settings), not to the canvas.
  const lastG = useRef(0);
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (isTypingTarget(e.target)) return;
      // An overlay, dialog or menu is open on top of the canvas: its
      // keys belong to it (r/l/v/s would otherwise switch services
      // under an overlay with unsaved edits).
      if (e.defaultPrevented || anySurfaceOpen()) return;
      if (e.metaKey || e.ctrlKey || e.altKey) return;
      if (e.key === "g") {
        lastG.current = e.timeStamp;
        return;
      }
      if (lastG.current && e.timeStamp - lastG.current < 1200) {
        lastG.current = 0;
        return;
      }
      lastG.current = 0;
      const svcOrder = order.filter((id) => id.startsWith("svc:"));
      if (e.key === "j" || e.key === "k") {
        if (svcOrder.length === 0) return;
        e.preventDefault();
        const cur = activeId ? svcOrder.indexOf(activeId) : -1;
        const delta = e.key === "j" ? 1 : -1;
        const next = cur < 0 ? (delta > 0 ? 0 : svcOrder.length - 1) : (cur + delta + svcOrder.length) % svcOrder.length;
        focusNode(svcOrder[next]);
        return;
      }
      const active = activeId?.startsWith("svc:") ? nodes.find((n) => n.id === activeId) : undefined;
      if (!active) return;
      const short = serviceShortName(project, (active.data as ServiceNodeData).service.metadata.name);
      const tab = ({ l: "logs", v: "variables", s: "settings" } as Record<string, string>)[e.key];
      if (tab) {
        e.preventDefault();
        onSelectService?.(short, tab);
      } else if (e.key === "r") {
        e.preventDefault();
        if (!canServicesWrite) {
          toast.error("Redeploying requires editor access on this project");
          return;
        }
        setConfirmRedeploy(short);
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
    // focusNode reads refs + boxes; boxes changes with nodes.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [nodes, order, activeId, project, onSelectService, canServicesWrite]);

  // Refit when the canvas box changes size (sidebar toggle, window
  // resize, the env banner appearing) — unless the user has panned or
  // zoomed, in which case their viewport wins.
  useEffect(() => {
    const el = wrapperRef.current;
    if (!el || typeof ResizeObserver === "undefined") return;
    let timer: ReturnType<typeof setTimeout> | undefined;
    let last = { w: el.clientWidth, h: el.clientHeight };
    const ro = new ResizeObserver(() => {
      const w = el.clientWidth;
      const h = el.clientHeight;
      if (Math.abs(w - last.w) < 2 && Math.abs(h - last.h) < 2) return;
      last = { w, h };
      clearTimeout(timer);
      timer = setTimeout(() => {
        if (!userMovedRef.current) void rf.current?.fitView(FIT_VIEW_OPTIONS);
      }, 150);
    });
    ro.observe(el);
    return () => {
      clearTimeout(timer);
      ro.disconnect();
    };
  }, []);

  // Layout effect, not effect: nodes are positioned before the first
  // paint, so fitView never sees (or flashes) an empty or unlaid graph.
  // A data refetch keeps whatever the user had selected.
  useLayoutEffect(() => {
    if (!project) return;
    const stored = loadStoredLayout(project);
    const laid = applyStoredLayout(autoLayout(initialNodes, initialEdges), stored);
    setNodes((prev) => {
      const selected = new Set(prev.filter((n) => n.selected).map((n) => n.id));
      return selected.size ? laid.map((n) => (selected.has(n.id) ? { ...n, selected: true } : n)) : laid;
    });
    setEdges(initialEdges);
  }, [project, initialNodes, initialEdges]);

  // The fitView prop only fits the first render; nodes that arrive with
  // the data (or a changed node count) refit until the user takes over.
  const nodeCount = initialNodes.length;
  useEffect(() => {
    if (nodeCount === 0 || userMovedRef.current) return;
    const id = requestAnimationFrame(() => void rf.current?.fitView(FIT_VIEW_OPTIONS));
    return () => cancelAnimationFrame(id);
  }, [project, nodeCount]);

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
    // Wiring a connection writes an env var on the target service —
    // an editor-level mutation. Fail fast with a clear message
    // instead of letting the setServiceEnv call 403.
    if (!canServicesWrite) {
      toast.error("Connecting nodes requires editor access on this project");
      return;
    }
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
      // Role-system v2: masked values (non-admin) would be re-sent as
      // the sentinel and clobber the target's real env. Refuse. (Server
      // rejects the sentinel too.)
      if (current.masked) {
        toast.error("Env values are admin-only for your role — can't wire this connection without seeing the target's env.");
        return;
      }
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
      // Edges derive from the describe payload, not the services list.
      qc.invalidateQueries({ queryKey: servicesQueryKey(project) });
      qc.invalidateQueries({ queryKey: ["projects", project, "envs"] });
      invalidateProjectDescribe(qc, project);
    } catch (err) {
      toastError(err, "Connect failed");
    }
  };

  const openNode = (node: Node) => {
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
  const onNodeClick: NodeMouseHandler = (_e, node) => openNode(node);
  openNodeRef.current = (id: string) => {
    const node = nodes.find((n) => n.id === id);
    if (node) openNode(node);
  };

  // Right-click on a service node — Open / View logs / Redeploy / Delete.
  const onServiceContext = (
    e: React.MouseEvent,
    data: ServiceNodeData,
    liveUrl: string | undefined,
  ) => {
    e.preventDefault();
    const short = serviceShortName(data.project, data.service.metadata.name);
    // The card's own URL (custom domain, or the auto host while it isn't serving).
    const url = liveUrl;

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
        label: "Redeploy…",
        icon: RotateCcw,
        shortcut: canServicesWrite ? "r" : undefined,
        ...editorGate(canServicesWrite),
        onSelect: () => setConfirmRedeploy(short),
      },
      // Crons + Runs deep-links. The service-overlay panels for both
      // tabs render their create-composer at the top of the panel
      // (CronsPanel has an "Add cron" button; RunsPanel always shows
      // NewRunComposer for admins), so landing
      // on the tab is enough — no separate dialog needed. The overlay
      // surfaces these tabs even when empty if the user explicitly
      // routed to them (see MAIN_TABS in ServiceOverlay) so the
      // strip stays in sync.
      {
        id: "add-cron",
        label: "Add cron…",
        icon: Clock,
        ...(canCreateJobs ? {} : { disabled: true, shortcut: "needs admin" }),
        onSelect: () => {
          onSelectService?.(short, "crons");
        },
      },
      {
        id: "run-command",
        label: "Run command…",
        icon: Terminal,
        ...(canCreateJobs ? {} : { disabled: true, shortcut: "needs admin" }),
        onSelect: () => {
          onSelectService?.(short, "runs");
        },
      },
      { id: "sep2", kind: "separator" },
      // Hard-stop toggle. Reads spec.stopped off the node's service.
      // Stopped → "Start service"; running → "Stop service" (confirmed
      // via the shared ConfirmDialog since it takes the service offline).
      data.service.spec?.stopped
        ? {
            id: "start",
            label: "Start service",
            icon: Play,
            ...editorGate(canServicesWrite),
            onSelect: async () => {
              try {
                await callStopStart(data.project, short, "start", qc);
                toast.success(`Starting ${short}`);
              } catch (err) {
                toastError(err, "Failed to start service");
              }
            },
          }
        : {
            id: "stop",
            label: "Stop service…",
            icon: Square,
            ...editorGate(canServicesWrite),
            onSelect: () =>
              setConfirmStop({ short, nodeId: `svc:${data.service.metadata.name}` }),
          },
      { id: "sep3", kind: "separator" },
      {
        id: "delete",
        label: "Delete service…",
        icon: Trash2,
        destructive: true,
        ...editorGate(canServicesWrite),
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
        ...editorGate(canAddonsWrite),
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

  const resetLayout = () => {
    clearStoredLayout(project);
    setNodes(autoLayout(initialNodes, initialEdges));
    userMovedRef.current = false;
    requestAnimationFrame(() => void rf.current?.fitView({ ...FIT_VIEW_OPTIONS, duration: reducedMotion() ? 0 : 200 }));
  };

  // Right-click on a cron node: the edit dialog is the cron's only
  // detail surface. (Crons used to fall through to the addon menu, which
  // read data.addon and threw.)
  const onCronContext = (e: React.MouseEvent, data: CronNodeData) => {
    e.preventDefault();
    const items: ContextMenuEntry[] = [
      {
        id: "header",
        kind: "header",
        label: data.cron.spec.displayName?.trim() || serviceShortName(project, data.cron.metadata.name),
        subtitle: `cron · ${data.cron.spec.schedule ?? ""}`,
        icon: Clock,
      },
      {
        id: "edit",
        label: "Edit cron…",
        icon: SettingsIcon,
        shortcut: "Enter",
        onSelect: () => setEditCronName(data.cron.metadata.name),
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
        ...editorGate(canServicesWrite),
        onSelect: () => {
          window.location.href = `/projects/${encodeURIComponent(project)}/services/new`;
        },
      },
      {
        id: "add-addon",
        label: "Add addon",
        icon: Plus,
        ...editorGate(canAddonsWrite),
        onSelect: () => setShowAddAddon(true),
      },
      {
        id: "add-cron",
        label: "Add cron",
        icon: Plus,
        // Cron create/update/delete requires editor on the server
        // (crons.go requireProjectAccess Editor) — services:write is
        // the matching client-side capability.
        ...editorGate(canServicesWrite),
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
        onSelect: resetLayout,
      },
    ];
    setCtx({ open: true, x: e.clientX, y: e.clientY, items });
  };

  return (
    <div
      ref={wrapperRef}
      // Skip-link target; focusing the region forwards to the active node.
      id="canvas-root"
      tabIndex={-1}
      role="region"
      aria-label={`${project} canvas. Arrow keys move between services and addons, Enter opens one.`}
      className="relative flex-1 min-h-0 w-full bg-[var(--canvas-bg)]"
      onKeyDown={onCanvasKeyDown}
      onFocus={(e) => {
        if (e.target === e.currentTarget) {
          e.currentTarget.querySelector<HTMLElement>('[data-canvas-node][tabindex="0"]')?.focus();
          return;
        }
        const id = e.target instanceof HTMLElement ? e.target.dataset.canvasNode : undefined;
        if (id) {
          setActiveId(id);
          setFocusedId(id);
        }
      }}
      onBlur={(e) => {
        if (e.target instanceof HTMLElement && e.target.dataset.canvasNode) setFocusedId(null);
      }}
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
        nodes={nodes.map((n): Node => ({
          ...n,
          data: {
            ...n.data,
            __tabIndex: n.id === tabStopId ? (0 as const) : (-1 as const),
            __onContext:
              n.type === "service"
                ? (e: React.MouseEvent, liveUrl?: string) => onServiceContext(e, n.data as ServiceNodeData, liveUrl)
                : n.type === "addon"
                  ? (e: React.MouseEvent) => onAddonContext(e, n.data as AddonNodeData)
                  : (e: React.MouseEvent) => onCronContext(e, n.data as unknown as CronNodeData),
            __onOpenTab:
              n.type === "service"
                ? (tab: string) =>
                    onSelectService?.(
                      serviceShortName(project, (n.data as ServiceNodeData).service.metadata.name),
                      tab,
                    )
                : n.type === "addon"
                  ? (tab: string) => onSelectAddon?.((n.data as AddonNodeData).addon.metadata.name, tab)
                  : undefined,
          },
        }))}
        edges={edges
          .filter((e) => {
            // Hide categories the user toggled off. Edges without a
            // kind tag (legacy or future categories) are always shown
            // so an upgrade can't accidentally swallow them.
            const kind = (e.data as { kind?: string } | undefined)?.kind;
            if (kind === "addon" && !edgeFilters.addon) return false;
            if (kind === "ref" && !edgeFilters.ref) return false;
            return true;
          })
          .map((e) => styleEdge(e, hotNodes, boxById))}
        nodeTypes={nodeTypes}
        onNodesChange={onNodesChange}
        onNodeClick={onNodeClick}
        onNodeMouseEnter={(_e, n) => setHoveredNode(n.id)}
        onNodeMouseLeave={() => setHoveredNode(null)}
        onConnect={onConnect}
        connectionMode={ConnectionMode.Loose}
        onInit={(inst) => {
          rf.current = inst;
        }}
        // A non-null event means the user dragged or scrolled the
        // viewport (programmatic fits pass null).
        onMoveStart={(e) => {
          if (e) userMovedRef.current = true;
        }}
        // Roving focus lives on our node cards: React Flow's own node
        // wrappers and edges would add a tab stop per element, and its
        // arrow keys move nodes instead of focus.
        nodesFocusable={false}
        edgesFocusable={false}
        disableKeyboardA11y
        fitView
        fitViewOptions={FIT_VIEW_OPTIONS}
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
        {/* Drafting paper: a dot on every 24px snap point and a cross
            every fifth, so nodes visibly land on the grid. */}
        <Background id="minor" gap={24} size={1.2} color="var(--grid-minor)" />
        <Background id="major" variant={BackgroundVariant.Cross} gap={120} size={9} lineWidth={1} color="var(--grid-major)" />
        <Controls />
        <EdgeLegend filters={edgeFilters} setFilters={setEdgeFilters} />
        <Panel position="top-right" className="!m-3 flex items-center gap-2">
          <button
            type="button"
            onClick={resetLayout}
            title="Forget dragged positions and re-run the automatic layout"
            className="canvas-float inline-flex h-8 items-center gap-1.5 rounded-lg px-2.5 text-xs font-medium text-[var(--text-secondary)] transition-colors hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)]"
          >
            <LayoutGrid className="h-3.5 w-3.5" aria-hidden />
            Reset layout
          </button>
          <button
            type="button"
            aria-pressed={density === "compact"}
            onClick={() => setDensity(density === "compact" ? "comfortable" : "compact")}
            title={density === "compact" ? "Show full cards (URL row)" : "Smaller cards, so more of the project fits"}
            className={cn(
              "canvas-float inline-flex h-8 items-center gap-1.5 rounded-lg px-2.5 text-xs font-medium transition-colors hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)]",
              density === "compact" ? "text-[var(--text-primary)]" : "text-[var(--text-secondary)]",
            )}
          >
            <Rows3 className="h-3.5 w-3.5" aria-hidden />
            Compact
          </button>
          <CanvasShortcutsButton />
        </Panel>
        <AddMenuPanel
          project={project}
          canServicesWrite={canServicesWrite}
          canAddonsWrite={canAddonsWrite}
          onAddAddon={() => setShowAddAddon(true)}
          onAddCron={() => setShowAddCron(true)}
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
              . Its data volume (PVC) and connection secret are kept: re-adding
              an addon with the same name reattaches the old data. To free
              the disk, delete the PVC by hand afterwards.
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
            // Prefix match: describe, services, addons, envs. Without it
            // the node came back from cache on the next canvas rebuild.
            void qc.invalidateQueries({ queryKey: ["projects", project] });
            toast.success(
              kind === "service"
                ? `Service ${short} deleted`
                : `Addon ${short} deleted`
            );
          } catch (err) {
            // Restore the node so the canvas matches reality.
            setNodes(prevNodes);
            toastError(err, "Failed to delete");
          } finally {
            setDeleting(false);
          }
        }}
      />

      <ConfirmDialog
        open={!!confirmRedeploy}
        title={`Redeploy ${confirmRedeploy ?? ""} (${selectedEnv})?`}
        destructive={false}
        confirmLabel="Redeploy"
        body={
          <>
            This starts a new {selectedEnv} build of{" "}
            <span className="font-mono text-[var(--text-primary)]">{confirmRedeploy}</span>
            {redeployBranch ? (
              <>
                {" "}from <span className="font-mono text-[var(--text-primary)]">{redeployBranch}</span>
              </>
            ) : null}{" "}
            and rolls it out when the build succeeds.
          </>
        }
        pending={redeploying}
        onCancel={() => setConfirmRedeploy(null)}
        onConfirm={async () => {
          if (!confirmRedeploy) return;
          const short = confirmRedeploy;
          setRedeploying(true);
          try {
            const res = await callTrigger(project, short, redeployBranch ? { branch: redeployBranch } : {});
            toast.success(buildTriggerMessage(res, `Redeploy started for ${short}`, short));
            setConfirmRedeploy(null);
          } catch (err) {
            toastError(err, "Failed to redeploy");
          } finally {
            setRedeploying(false);
          }
        }}
      />

      <ConfirmDialog
        open={!!confirmStop}
        title={`Stop ${confirmStop?.short ?? ""}`}
        body="Scales the service to zero until you start it again. Public services return a 503 meanwhile; workers stop processing."
        confirmLabel="Stop service"
        pending={stopping}
        onCancel={() => setConfirmStop(null)}
        onConfirm={async () => {
          if (!confirmStop) return;
          const { short } = confirmStop;
          setStopping(true);
          try {
            await callStopStart(project, short, "stop", qc);
            toast.success(`Stopped ${short}`);
            setConfirmStop(null);
          } catch (err) {
            toastError(err, "Failed to stop service");
          } finally {
            setStopping(false);
          }
        }}
      />
    </div>
  );
}

// callTrigger hits the API directly rather than spinning up a
// per-node mutation hook.
async function callTrigger(
  project: string,
  service: string,
  body: { branch?: string }
) {
  const { triggerBuild } = await import("@/features/services/api");
  return triggerBuild(project, service, body);
}

// callStopStart adapter: like callTrigger, the canvas hits the stop/
// start API directly rather than spinning up a per-node mutation hook.
// On success it invalidates the project subtree so the node repaints
// its stopped/running state (spec.stopped + replicas both change).
async function callStopStart(
  project: string,
  service: string,
  action: "stop" | "start",
  qc: ReturnType<typeof useQueryClient>
) {
  const { stopService, startService } = await import("@/features/services/api");
  if (action === "stop") await stopService(project, service);
  else await startService(project, service);
  qc.invalidateQueries({ queryKey: ["projects", project] });
}

// AddMenuPanel is the visible "+ Add" entry point in the canvas's top-left
// corner: the same add actions as the pane right-click menu, reachable by
// keyboard and touch.
function AddMenuPanel({
  project,
  canServicesWrite,
  canAddonsWrite,
  onAddAddon,
  onAddCron,
}: {
  project: string;
  canServicesWrite: boolean;
  canAddonsWrite: boolean;
  onAddAddon: () => void;
  onAddCron: () => void;
}) {
  const [open, setOpen] = useState(false);
  const itemCls =
    "flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-left text-xs text-[var(--text-secondary)] hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)] focus-visible:bg-[var(--bg-tertiary)] focus-visible:outline-none disabled:cursor-not-allowed disabled:opacity-50 disabled:hover:bg-transparent";
  const needsEditor = "You need editor access on this project";
  return (
    <Panel position="top-left" className="!m-3">
      <Popover open={open} onOpenChange={setOpen}>
        <PopoverTrigger className="canvas-float inline-flex h-8 items-center gap-1.5 rounded-lg px-3 text-xs font-medium text-[var(--text-primary)] transition-colors hover:bg-[var(--bg-tertiary)] data-[popup-open]:bg-[var(--bg-tertiary)]">
          <Plus className="h-3.5 w-3.5" />
          Add
        </PopoverTrigger>
        <PopoverContent align="start" className="w-48 gap-0.5 p-1">
          {(!canServicesWrite || !canAddonsWrite) && (
            <p className="border-b border-[var(--border-subtle)] px-2 pt-1 pb-2 text-xs text-[var(--text-tertiary)]">
              {needsEditor} to add here.
            </p>
          )}
          {canServicesWrite ? (
            <a href={`/projects/${encodeURIComponent(project)}/services/new`} className={itemCls}>
              <Plus className="h-3.5 w-3.5" />
              Service
            </a>
          ) : (
            <button type="button" disabled title={needsEditor} className={itemCls}>
              <Plus className="h-3.5 w-3.5" />
              Service
            </button>
          )}
          <button
            type="button"
            disabled={!canAddonsWrite}
            title={canAddonsWrite ? undefined : needsEditor}
            className={itemCls}
            onClick={() => {
              setOpen(false);
              onAddAddon();
            }}
          >
            <Database className="h-3.5 w-3.5" />
            Addon (database, cache, …)
          </button>
          <button
            type="button"
            disabled={!canServicesWrite}
            title={canServicesWrite ? undefined : needsEditor}
            className={itemCls}
            onClick={() => {
              setOpen(false);
              onAddCron();
            }}
          >
            <Clock className="h-3.5 w-3.5" />
            Cron job
          </button>
        </PopoverContent>
      </Popover>
    </Panel>
  );
}

// EdgeLegend explains the two edge colours and doubles as their
// visibility toggle: each entry is a pressed/unpressed button.
function EdgeLegend({
  filters,
  setFilters,
}: {
  filters: { addon: boolean; ref: boolean };
  setFilters: React.Dispatch<React.SetStateAction<{ addon: boolean; ref: boolean }>>;
}) {
  return (
    <Panel position="bottom-right" className="!m-3">
      <div
        role="group"
        aria-label="Edges"
        className="canvas-float flex items-center gap-0.5 rounded-[10px] p-1"
      >
        <LegendToggle
          label="Addon connections"
          hint="A service reads the addon's connection secret. Fainter: mounted but not referenced by name. Dashed: the service hasn't deployed yet."
          swatch="var(--edge-addon)"
          on={filters.addon}
          onClick={() => setFilters((f) => ({ ...f, addon: !f.addon }))}
        />
        <LegendToggle
          label="Service references"
          hint="One service's env vars point at another service's URL or host. Hover or select a node to highlight its connections."
          swatch="var(--edge-ref)"
          on={filters.ref}
          onClick={() => setFilters((f) => ({ ...f, ref: !f.ref }))}
        />
      </div>
    </Panel>
  );
}

function LegendToggle({
  label,
  hint,
  swatch,
  on,
  onClick,
}: {
  label: string;
  hint: string;
  swatch: string;
  on: boolean;
  onClick: () => void;
}) {
  return (
    <button
      type="button"
      aria-pressed={on}
      onClick={onClick}
      title={`${on ? "Hide" : "Show"} ${label.toLowerCase()}. ${hint}`}
      className={
        "inline-flex h-7 items-center gap-2 rounded-md border px-2 text-xs transition-colors " +
        (on
          ? "border-[var(--border-strong)] bg-[var(--bg-tertiary)] text-[var(--text-primary)] hover:bg-[var(--bg-secondary)]"
          : "border-transparent text-[var(--text-tertiary)] hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-secondary)]")
      }
    >
      <span
        aria-hidden
        className="inline-block h-[3px] w-5 rounded-full transition-opacity"
        style={{ background: swatch, opacity: on ? 1 : 0.3 }}
      />
      {label}
      <span className={on ? "text-[var(--text-secondary)]" : "text-[var(--text-tertiary)]"}>{on ? "On" : "Off"}</span>
    </button>
  );
}

interface EdgeData extends Record<string, unknown> {
  kind: "addon" | "ref";
  // Conn secret mounted but not referenced by name: drawn fainter.
  implicit?: boolean;
  // Target never deployed: the only dashed edges.
  pending?: boolean;
}

const EDGE_COLOR: Record<EdgeData["kind"], string> = {
  addon: "var(--edge-addon)",
  ref: "var(--edge-ref)",
};

// styleEdge: solid and low-contrast by default, accent + thicker when
// either end is hovered, focused or selected, and nearly gone when
// another node is. Edges are rounded orthogonal paths that leave and
// enter on the sides of the two nodes facing each other, so a busy graph
// reads as tidy lanes instead of crossing curves; several addon edges
// into one service share its facing side and bundle on the way in.
// Never animated — a moving dash read as "in progress" on every edge.
function styleEdge(e: Edge, hot: Set<string>, boxById: Map<string, Box>): BuiltInEdge {
  const d = (e.data ?? { kind: "ref" }) as EdgeData;
  const lit = hot.has(e.source) || hot.has(e.target);
  const dimmed = hot.size > 0 && !lit;
  const src = boxById.get(e.source);
  const dst = boxById.get(e.target);
  const sides = src && dst ? facingSides(src, dst) : { source: "right" as const, target: "left" as const };
  return {
    ...e,
    type: "smoothstep",
    pathOptions: { borderRadius: 18, offset: 24 },
    sourceHandle: sides.source,
    targetHandle: sides.target,
    animated: false,
    zIndex: lit ? 1 : 0,
    style: {
      stroke: lit ? "var(--edge-lit)" : EDGE_COLOR[d.kind],
      strokeWidth: lit ? 2.25 : 1.5,
      strokeLinecap: "round",
      opacity: lit ? 1 : dimmed ? 0.08 : d.implicit ? 0.45 : 0.85,
      strokeDasharray: d.pending ? "5 5" : undefined,
    },
  };
}

const NODE_FALLBACK_SIZE: Record<string, { w: number; h: number }> = {
  service: { w: 280, h: 120 }, // min; long names widen it (measured wins)
  addon: { w: 220, h: 72 },
  cron: { w: 220, h: 96 },
};

function nodeBox(n: Node): Box {
  const fb = NODE_FALLBACK_SIZE[n.type ?? "service"] ?? NODE_FALLBACK_SIZE.service;
  return { id: n.id, x: n.position.x, y: n.position.y, w: n.measured?.width ?? fb.w, h: n.measured?.height ?? fb.h };
}

const ARROW_DIR: Record<string, Direction> = {
  ArrowUp: "up",
  ArrowDown: "down",
  ArrowLeft: "left",
  ArrowRight: "right",
};

// Never fit below 0.85: card text stays ≥12px on screen and a big graph
// pans instead of shrinking into unreadable tiles.
// A small project zooms in to fill the canvas instead of floating in a
// corner; a big one zooms out far enough to fit.
const FIT_VIEW_OPTIONS = { padding: 0.15, maxZoom: 1.25, minZoom: 0.5 };

function reducedMotion(): boolean {
  return typeof window !== "undefined" && window.matchMedia?.("(prefers-reduced-motion: reduce)").matches === true;
}
