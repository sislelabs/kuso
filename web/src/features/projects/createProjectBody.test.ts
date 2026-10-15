import { describe, expect, it } from "vitest";
import { buildCreateProjectBody, type CreateProjectForm } from "./createProjectBody";

const blank: CreateProjectForm = {
  name: " shop ",
  description: "",
  baseDomain: "",
  namespace: "",
  repoURL: "",
  repoBranch: "",
  installationId: 0,
  previewsEnabled: true,
  previewsTTLDays: "",
};

describe("buildCreateProjectBody", () => {
  it("omits blank optional fields and defaults the TTL to 7", () => {
    expect(JSON.parse(JSON.stringify(buildCreateProjectBody(blank)))).toEqual({
      name: "shop",
      previews: { enabled: true, ttlDays: 7 },
    });
  });
  it("maps namespace, repo, installation and TTL", () => {
    expect(
      buildCreateProjectBody({
        ...blank,
        namespace: "shop-prod",
        repoURL: "https://github.com/acme/shop",
        repoBranch: "develop",
        installationId: 42,
        previewsTTLDays: "3",
      }),
    ).toMatchObject({
      namespace: "shop-prod",
      defaultRepo: { url: "https://github.com/acme/shop", defaultBranch: "develop" },
      github: { installationId: 42 },
      previews: { enabled: true, ttlDays: 3 },
    });
  });
  it("rejects a bad namespace and an out-of-range TTL", () => {
    expect(buildCreateProjectBody({ ...blank, namespace: "Shop_NS" })).toHaveProperty("error");
    expect(buildCreateProjectBody({ ...blank, previewsTTLDays: "0" })).toHaveProperty("error");
  });
});
