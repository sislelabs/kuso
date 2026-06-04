import { api } from "@/lib/api-client";

// One user's preference for one project: starred (pin to the top of the
// grid) and/or filed under a free-text folder label. The server only
// stores rows for projects the user has expressed a preference about;
// the default (unstarred, unfiled) is the absence of a row.
export interface ProjectPref {
  project: string;
  starred: boolean;
  folder?: string;
  updatedAt: string;
}

export async function listProjectPrefs(): Promise<ProjectPref[]> {
  const resp = await api<{ prefs: ProjectPref[] }>("/api/me/project-prefs");
  return resp.prefs ?? [];
}

export async function setProjectPref(
  project: string,
  body: { starred: boolean; folder: string }
): Promise<void> {
  await api(`/api/me/project-prefs/${encodeURIComponent(project)}`, {
    method: "PUT",
    body,
  });
}

export async function clearProjectPref(project: string): Promise<void> {
  await api(`/api/me/project-prefs/${encodeURIComponent(project)}`, {
    method: "DELETE",
  });
}

export async function renameFolder(from: string, to: string): Promise<void> {
  await api("/api/me/folders/rename", { method: "POST", body: { from, to } });
}
