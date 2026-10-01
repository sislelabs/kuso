import { describe, expect, it } from "vitest";
import { errorsQueryKey } from "./hooks";

describe("errorsQueryKey", () => {
  // Without env in the key, switching staging → production served the
  // cached staging groups until the next refetch.
  it("keys each environment separately", () => {
    expect(errorsQueryKey("p", "api", "24h", "staging")).not.toEqual(errorsQueryKey("p", "api", "24h", "production"));
    expect(errorsQueryKey("p", "api", "24h")).toEqual(errorsQueryKey("p", "api", "24h", ""));
  });
});
