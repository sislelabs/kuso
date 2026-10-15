import { beforeEach, describe, expect, it, vi } from "vitest";

const apiMock = vi.hoisted(() => vi.fn());
vi.mock("@/lib/api-client", () => ({ api: apiMock }));

import {
  catalogLabel,
  getInstanceInfo,
  isValidQuantity,
  podSizeToInput,
  updatePodSize,
  validatePodSize,
  type PodSizeInput,
} from "./api";

const input: PodSizeInput = {
  Name: " small ",
  CPURequest: "250m",
  CPULimit: "1",
  MemoryRequest: "256Mi",
  MemoryLimit: "1Gi",
  Description: "",
};

describe("pod size presets", () => {
  beforeEach(() => apiMock.mockReset());

  it("accepts Kubernetes quantities and rejects junk", () => {
    for (const q of ["", "250m", "1", "0.5", "512Mi", "2Gi", "1e3"]) expect(isValidQuantity(q)).toBe(true);
    for (const q of ["1 Gi", "2GB", "abc", "1.5cores"]) expect(isValidQuantity(q)).toBe(false);
  });

  it("validates name and quantities", () => {
    expect(validatePodSize(input)).toBeNull();
    expect(validatePodSize({ ...input, Name: " " })).toMatch(/name/i);
    expect(validatePodSize({ ...input, Name: "none" })).toMatch(/reserved/);
    expect(validatePodSize({ ...input, MemoryLimit: "1GB" })).toMatch(/Memory limit/);
    expect(validatePodSize(input, ["small"])).toMatch(/already exists/);
  });

  // The server decodes into db.PodSize, where Description is a
  // sql.NullString: a bare string fails the whole decode with a 400.
  it("sends Description as a NullString and trims fields", async () => {
    apiMock.mockResolvedValue({});
    await updatePodSize("p/1", input);
    expect(apiMock).toHaveBeenCalledWith("/api/config/podsizes/p%2F1", {
      method: "PUT",
      body: {
        Name: "small",
        CPURequest: "250m",
        CPULimit: "1",
        MemoryRequest: "256Mi",
        MemoryLimit: "1Gi",
        Description: { String: "", Valid: false },
      },
    });
  });

  it("round-trips a row into the form", () => {
    expect(
      podSizeToInput({
        ID: "1",
        Name: "s",
        CPURequest: "",
        CPULimit: "",
        MemoryRequest: "",
        MemoryLimit: "",
        Description: { String: "tiny", Valid: true },
      }).Description
    ).toBe("tiny");
  });
});

describe("instance info", () => {
  beforeEach(() => apiMock.mockReset());

  // An admin's /api/config/registry is the raw CR map and can carry
  // credentials; only enabled + host may reach the UI.
  it("drops everything from the registry map except enabled and host", async () => {
    apiMock.mockImplementation((path: string) => {
      switch (path) {
        case "/api/config/banner":
          return Promise.resolve({ show: true, text: "maintenance", bgcolor: "red", fontcolor: "white" });
        case "/api/config/registry":
          return Promise.resolve({ enabled: true, host: "registry.example.com", password: "s3cret" });
        case "/api/config/clusterissuer":
          return Promise.resolve({ clusterissuer: "letsencrypt-prod" });
        default:
          return Promise.resolve({ enabled: false, catalogs: null });
      }
    });
    const info = await getInstanceInfo();
    expect(info.registry).toEqual({ enabled: true, host: "registry.example.com" });
    expect(info.banner.text).toBe("maintenance");
    expect(info.clusterIssuer).toBe("letsencrypt-prod");
    expect(info.templates).toEqual({ enabled: false, catalogs: [] });
  });

  it("labels catalogs of any shape", () => {
    expect(catalogLabel({ name: "kuso", index: "https://x/index.yaml" })).toBe("kuso · https://x/index.yaml");
    expect(catalogLabel("plain")).toBe("plain");
    expect(catalogLabel({ other: 1 })).toBe('{"other":1}');
  });
});
