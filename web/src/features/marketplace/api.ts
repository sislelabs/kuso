import { api } from "@/lib/api-client";

export interface MarketplacePrompt {
  key: string;
  title: string;
  kind: "string" | "password" | "domain";
  help?: string;
  default?: string;
  placeholder?: string;
  required?: boolean;
}

export interface MarketplaceApp {
  name: string;
  title: string;
  description: string;
  category: string;
  website?: string;
  appVersion?: string;
  prompts: MarketplacePrompt[];
}

export interface RenderResult {
  project: string;
  yaml: string;
  notes: { kind: string; detail: string }[];
}

export async function listMarketplace(): Promise<MarketplaceApp[]> {
  const res = await api<{ apps: MarketplaceApp[] }>("/api/marketplace");
  return res.apps ?? [];
}

export async function renderApp(
  app: string,
  project: string,
  answers: Record<string, string>,
): Promise<RenderResult> {
  return api<RenderResult>(`/api/marketplace/${encodeURIComponent(app)}/render`, {
    method: "POST",
    body: { project, answers },
  });
}
