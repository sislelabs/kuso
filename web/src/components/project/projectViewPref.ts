export type ProjectViewMode = "canvas" | "list";

const key = (project: string) => `kuso.project.view.${project}`;

export function loadProjectView(project: string): ProjectViewMode | null {
  if (typeof window === "undefined") return null;
  try {
    const v = localStorage.getItem(key(project));
    return v === "canvas" || v === "list" ? v : null;
  } catch {
    return null;
  }
}

export function saveProjectView(project: string, mode: ProjectViewMode) {
  if (typeof window === "undefined") return;
  try {
    localStorage.setItem(key(project), mode);
  } catch {
    // storage disabled: the choice just won't survive a reload
  }
}
