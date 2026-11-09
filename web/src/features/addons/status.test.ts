import { describe, expect, it } from "vitest";
import type { KusoAddon } from "@/types/projects";
import { addonHealthFrom, runningImageOf } from "./status";

describe("runningImageOf", () => {
  it("reads the version newer servers report", () => {
    expect(runningImageOf({ pods: [], runningImage: "postgres:16-alpine", runningVersion: "16-alpine" })).toEqual({
      runningImage: "postgres:16-alpine",
      runningVersion: "16-alpine",
    });
  });
  it("is empty for older servers and digest-only images", () => {
    expect(runningImageOf({ namespace: "kuso", pods: [] })).toEqual({});
    expect(runningImageOf({ runningImage: "postgres@sha256:abc", runningVersion: "" })).toEqual({
      runningImage: "postgres@sha256:abc",
    });
    expect(runningImageOf(undefined)).toEqual({});
  });
});

const addon = (status: Record<string, unknown>): KusoAddon =>
  ({ metadata: { name: "demo-db" }, spec: { kind: "postgres" }, status }) as unknown as KusoAddon;

describe("addonHealthFrom", () => {
  it("takes the server's stuck state over a Deployed release, with its reason", () => {
    const h = addonHealthFrom(
      addon({
        state: "stuck",
        stateDetail: "No node has enough memory",
        pods: { ready: 0, total: 1 },
        conditions: [{ type: "Deployed", status: "True" }],
      }),
      { pending: false, error: false, pods: [{ name: "db-0", ready: false }] },
    );
    expect(h.status.label).toBe("Stuck");
    expect(h.detail).toBe("No node has enough memory");
  });
  it("shows pod counts for a live addon", () => {
    const h = addonHealthFrom(addon({ state: "live", pods: { ready: 1, total: 1 } }), {
      pending: true,
      error: false,
      pods: undefined,
    });
    expect(h).toMatchObject({ pending: false, detail: "1/1 pods ready" });
    expect(h.status.label).toBe("Live");
  });
  it("falls back to the pod list on servers without a rollup", () => {
    const h = addonHealthFrom(addon({ conditions: [{ type: "Deployed", status: "True" }] }), {
      pending: false,
      error: false,
      pods: [],
    });
    expect(h.status.label).toBe("Failing");
    expect(h.detail).toBe("No pods running");
  });
  it("doesn't call a database on the shared instance postgres failing for having no pods", () => {
    const shared = {
      metadata: { name: "bukvite-db" },
      spec: { kind: "postgres", useInstanceAddon: "pg" },
      status: { conditions: [{ type: "Deployed", status: "True" }] },
    } as unknown as KusoAddon;
    const h = addonHealthFrom(shared, { pending: false, error: false, pods: [] });
    expect(h.status.label).toBe("Live");
    expect(h.detail).toBe("Runs on the shared instance database pg");
  });
});
