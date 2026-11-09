// Which onboarding step a visitor without ?step= should land on.

export type WelcomeStep = 1 | 2 | 3;

export interface ProjectProgress {
  name: string;
  createdAt?: string;
  // null when the server didn't say (summary unavailable).
  serviceCount: number | null;
}

export interface WelcomeProgress {
  done: Record<WelcomeStep, boolean>;
  step: WelcomeStep;
  // The project step 3 is about, when one exists.
  project: string;
  // Every known project already runs a service: step 3 has nothing
  // left to prompt for.
  allDeployed: boolean;
}

function newestFirst(a: ProjectProgress, b: ProjectProgress): number {
  return (b.createdAt ?? "").localeCompare(a.createdAt ?? "");
}

// welcomeProgress lands on the step after the furthest completed one,
// so a user who deployed from an image without the GitHub App isn't
// sent back to step 1. Step 3 targets the newest project that has no
// service yet, else the newest project.
export function welcomeProgress(hasGitHub: boolean, projects: readonly ProjectProgress[]): WelcomeProgress {
  const sorted = [...projects].sort(newestFirst);
  const empty = sorted.find((p) => p.serviceCount === 0);
  const deployed = sorted.some((p) => (p.serviceCount ?? 0) > 0);
  const done: Record<WelcomeStep, boolean> = { 1: hasGitHub, 2: sorted.length > 0, 3: deployed };
  const project = (empty ?? sorted[0])?.name ?? "";
  let step: WelcomeStep = 1;
  if (done[2]) step = 3;
  else if (done[1]) step = 2;
  return { done, step, project, allDeployed: deployed && !empty && sorted.every((p) => p.serviceCount !== null) };
}

export interface NextStepFacts {
  hasGitHub: boolean;
  // Every env CR the summary returned, across projects.
  environments: readonly { envFromSecrets?: string[]; additionalHosts?: string[] }[];
  // null = the viewer can't see users or rules, so the step is left out.
  userCount: number | null;
  alertRuleCount: number | null;
}

export type NextStepId = "github" | "database" | "domain" | "teammate" | "alerts";

// nextSteps is the post-setup checklist: what an already-deployed team
// usually does next, each marked done from data the dashboard already has.
// An addon mounts into envs through its "<addon>-conn" Secret, so a conn
// Secret on any env means a database exists.
export function nextSteps(f: NextStepFacts): { id: NextStepId; done: boolean }[] {
  const out: { id: NextStepId; done: boolean }[] = [
    { id: "github", done: f.hasGitHub },
    { id: "database", done: f.environments.some((e) => (e.envFromSecrets ?? []).some((s) => s.endsWith("-conn"))) },
    { id: "domain", done: f.environments.some((e) => (e.additionalHosts ?? []).length > 0) },
  ];
  if (f.userCount !== null) out.push({ id: "teammate", done: f.userCount > 1 });
  if (f.alertRuleCount !== null) out.push({ id: "alerts", done: f.alertRuleCount > 0 });
  // Open steps first, in the order above.
  return [...out.filter((s) => !s.done), ...out.filter((s) => s.done)];
}
