"use client";

import { useState } from "react";
import { useMarketplace, type MarketplaceApp } from "@/features/marketplace";
import { DeployDialog } from "@/components/marketplace/DeployDialog";
import { env } from "@/lib/env";
import { Input } from "@/components/ui/input";

export default function MarketplacePage() {
  const { data: apps = [], isLoading } = useMarketplace();
  const [q, setQ] = useState("");
  const [cat, setCat] = useState<string>("all");
  const [selected, setSelected] = useState<MarketplaceApp | null>(null);

  const categories = ["all", ...Array.from(new Set(apps.map((a) => a.category))).sort()];
  const filtered = apps.filter(
    (a) =>
      (cat === "all" || a.category === cat) &&
      (q === "" || `${a.title} ${a.description}`.toLowerCase().includes(q.toLowerCase())),
  );

  return (
    <div className="mx-auto max-w-5xl p-6">
      <h1 className="text-xl font-semibold">Marketplace</h1>
      <p className="mt-1 text-sm text-[var(--text-secondary)]">
        Deploy a curated app in one click. Each app is a tested kuso.yaml.
      </p>

      <div className="mt-4 flex flex-wrap items-center gap-2">
        <Input placeholder="Search apps…" value={q} onChange={(e) => setQ(e.target.value)} className="max-w-xs" />
        {categories.map((c) => (
          <button
            key={c}
            onClick={() => setCat(c)}
            className={`rounded-full px-3 py-1 text-xs transition ${
              cat === c
                ? "bg-[var(--accent)] text-[var(--accent-foreground)]"
                : "bg-[var(--bg-tertiary)] text-[var(--text-secondary)] hover:text-[var(--text-primary)]"
            }`}
          >
            {c}
          </button>
        ))}
      </div>

      {isLoading ? (
        <p className="mt-6 text-sm text-[var(--text-tertiary)]">Loading…</p>
      ) : (
        <div className="mt-6 grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-3">
          {filtered.map((a) => (
            <button
              key={a.name}
              onClick={() => setSelected(a)}
              className="flex flex-col items-start rounded-lg border border-[var(--border-subtle)] bg-[var(--card)] p-4 text-left transition hover:border-[var(--accent)] hover:bg-[var(--bg-elevated)]"
            >
              <img
                src={`${env.apiBase}/api/marketplace/${encodeURIComponent(a.name)}/icon?v=3`}
                alt=""
                className="h-9 w-9 rounded-md"
                onError={(e) => ((e.target as HTMLImageElement).style.visibility = "hidden")}
              />
              <span className="mt-3 font-medium text-[var(--text-primary)]">{a.title}</span>
              <span className="mt-1 text-xs text-[var(--text-secondary)] line-clamp-2">{a.description}</span>
              <span className="mt-2 rounded bg-[var(--bg-tertiary)] px-1.5 py-0.5 text-[10px] text-[var(--text-tertiary)]">
                {a.category}
              </span>
            </button>
          ))}
        </div>
      )}

      {selected && <DeployDialog app={selected} onClose={() => setSelected(null)} />}
    </div>
  );
}
