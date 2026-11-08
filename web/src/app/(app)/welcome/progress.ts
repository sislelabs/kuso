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
