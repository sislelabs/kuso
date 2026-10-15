import { describe, expect, it } from "vitest";
import type { KusoService } from "@/types/projects";
import { fromSvc } from "./_primitives";
import { extraServicePatch, parseBuildArgs, parsePublicEnv } from "./servicePatchExtras";

const svc = {
  metadata: { name: "alpha-web" },
  spec: {
    project: "alpha",
    command: ["sh", "-c", "node server.js"],
    buildArgs: { NODE_ENV: "production", NPM_TOKEN: "••••••••" },
    publicEnv: ["NEXT_PUBLIC_API"],
    static: { buildCmd: "npm run build", outputDir: "dist" },
  },
} as unknown as KusoService;

describe("extraServicePatch", () => {
  const base = fromSvc(svc);

  it("sends nothing when untouched", () => {
    expect(extraServicePatch(base, base, svc.spec.command)).toEqual({ body: {} });
  });

  it("maps toggles and egress", () => {
    const next = { ...base, privateEgress: true, platformApiEgress: true, snapshotBeforeDeploy: true };
    expect(extraServicePatch(next, base, svc.spec.command)).toEqual({
      body: { privateEgress: true, platformApiEgress: true, snapshotBeforeDeploy: true },
    });
  });

  it("keeps a masked build arg as the sentinel so the server preserves it", () => {
    const next = { ...base, buildArgs: base.buildArgs + "\nAPP_VERSION=1.2=3" };
    expect(extraServicePatch(next, base, svc.spec.command)).toEqual({
      body: { buildArgs: { NODE_ENV: "production", NPM_TOKEN: "••••••••", APP_VERSION: "1.2=3" } },
    });
  });

  it("sends the static block whole, dropping blanks", () => {
    const next = { ...base, staticOutputDir: "out", staticBuilderImage: "  " };
    expect(extraServicePatch(next, base, svc.spec.command)).toEqual({
      body: { static: { buildCmd: "npm run build", outputDir: "out" } },
    });
  });

  it("clears the start command with an empty argv", () => {
    const next = { ...base, startCommand: "" };
    expect(extraServicePatch(next, base, svc.spec.command)).toEqual({ body: { command: [] } });
  });

  it("does not re-split a command that only gained whitespace", () => {
    const next = { ...base, startCommand: base.startCommand + "  " };
    expect(extraServicePatch(next, base, svc.spec.command)).toEqual({ body: {} });
  });

  it("rejects a non-identifier build arg key", () => {
    const next = { ...base, buildArgs: "1BAD=x" };
    expect(extraServicePatch(next, base, svc.spec.command)).toHaveProperty("error");
  });
});

describe("parsers", () => {
  it("parseBuildArgs refuses duplicates", () => {
    expect(parseBuildArgs("A=1\nA=2")).toHaveProperty("error");
  });
  it("parsePublicEnv splits on newlines and commas and dedupes", () => {
    expect(parsePublicEnv("A, B\nA\n")).toEqual({ names: ["A", "B"] });
  });
});
