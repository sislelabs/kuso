import { describe, expect, it } from "vitest";
import { buildSettingsPayload, type BuildSettingsResponse } from "./payload";

const base: BuildSettingsResponse = {
  maxConcurrent: 1,
  memoryLimit: "2Gi",
  memoryRequest: "512Mi",
  cpuLimit: "1500m",
  cpuRequest: "200m",
  registryAuthSecret: "ext-reg",
  registryHost: "ghcr.io/acme",
};

describe("buildSettingsPayload", () => {
  it("omits an unset cap the admin never edited", () => {
    const out = buildSettingsPayload({ ...base, maxConcurrentSet: false }, false);
    expect(out).not.toHaveProperty("maxConcurrent");
    expect(out).not.toHaveProperty("maxConcurrentSet");
  });

  it("sends the cap once edited", () => {
    expect(buildSettingsPayload({ ...base, maxConcurrentSet: false }, true).maxConcurrent).toBe(1);
  });

  it("sends the cap for servers that don't report maxConcurrentSet", () => {
    expect(buildSettingsPayload(base, false).maxConcurrent).toBe(1);
  });

  it("round-trips the registry override", () => {
    const out = buildSettingsPayload(base, false);
    expect(out.registryAuthSecret).toBe("ext-reg");
    expect(out.registryHost).toBe("ghcr.io/acme");
  });
});
