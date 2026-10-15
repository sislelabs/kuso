import { describe, expect, it } from "vitest";
import { runResources } from "./runResources";

const blank = { cpuRequest: "", cpuLimit: "", memRequest: "", memLimit: "" };

describe("runResources", () => {
  it("omits resources when every field is blank", () => {
    expect(runResources(blank)).toEqual({});
  });
  it("splits requests and limits", () => {
    expect(runResources({ ...blank, cpuRequest: "250m", memLimit: "1Gi" })).toEqual({
      resources: { requests: { cpu: "250m" }, limits: { memory: "1Gi" } },
    });
  });
  it("rejects a malformed quantity", () => {
    expect(runResources({ ...blank, memLimit: "1 GB" })).toHaveProperty("error");
  });
});
