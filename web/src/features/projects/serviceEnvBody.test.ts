import { describe, expect, it } from "vitest";
import { buildServiceEnvBody, type ServiceEnvForm } from "./serviceEnvBody";

const f: ServiceEnvForm = { name: "staging", branch: "develop", host: "", shareAddons: false, seedFrom: "", addons: [] };

describe("buildServiceEnvBody", () => {
  it("sends only name + branch by default (own copies of every stateful addon)", () => {
    expect(buildServiceEnvBody(f)).toEqual({ name: "staging", branch: "develop" });
  });
  it("drops seed and addon kinds when sharing production's addons", () => {
    expect(buildServiceEnvBody({ ...f, shareAddons: true, seedFrom: "production", addons: ["postgres"] })).toEqual({
      name: "staging",
      branch: "develop",
      shareAddons: true,
    });
  });
  it("passes seedFrom, addons and a lowercased host", () => {
    expect(buildServiceEnvBody({ ...f, seedFrom: "production", addons: ["postgres"], host: "QA.Example.com" })).toEqual({
      name: "staging",
      branch: "develop",
      host: "qa.example.com",
      seedFrom: "production",
      addons: ["postgres"],
    });
  });
  it("rejects reserved names and a missing branch", () => {
    expect(buildServiceEnvBody({ ...f, name: "pr-4" })).toHaveProperty("error");
    expect(buildServiceEnvBody({ ...f, branch: " " })).toHaveProperty("error");
  });
});
