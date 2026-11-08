import { api } from "@/lib/api-client";

export interface DirectoryUser {
  id: string;
  username: string;
  email?: string;
  firstName?: string;
  lastName?: string;
}

// GET /api/users needs settings:admin or user:write; callers gate on
// that before enabling the query.
export async function listUserDirectory(): Promise<DirectoryUser[]> {
  const res = await api<{ data?: DirectoryUser[] } | DirectoryUser[]>("/api/users");
  return Array.isArray(res) ? res : (res.data ?? []);
}
