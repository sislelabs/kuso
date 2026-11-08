// Plain-language summaries of the built-in instance roles, mirroring
// server-go auth.Compute: only admins carry instance permissions;
// editors can additionally create projects; project access otherwise
// comes from per-project grants.
export const INSTANCE_ROLE_LABEL: Record<string, string> = {
  "": "Inherit from groups",
  admin: "Admin",
  editor: "Editor",
  viewer: "Viewer",
};

export const INSTANCE_ROLE_GRANTS: Record<string, string> = {
  "": "Their access comes only from the groups they belong to.",
  admin:
    "Full control: every project, all instance settings, users, roles, updates and the audit log, plus env values, shells and the SQL console.",
  editor:
    "They can create new projects (and administer the ones they create). Other projects follow their per-project grants; no instance settings.",
  viewer: "No instance-wide rights. They see only the projects they've been granted, at the role granted there.",
};

export function roleLabel(role: string | undefined): string {
  return INSTANCE_ROLE_LABEL[role ?? ""] ?? role ?? "";
}
