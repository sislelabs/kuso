import { describe, expect, it } from "vitest";
import { isSensitiveConnectionKey, redactConnectionURL } from "./connection";

describe("connection masking", () => {
  it("shows coordinates, masks credentials", () => {
    expect(isSensitiveConnectionKey("POSTGRES_HOST", "db")).toBe(false);
    expect(isSensitiveConnectionKey("POSTGRES_PASSWORD", "x")).toBe(true);
    expect(isSensitiveConnectionKey("MONGODB_URI", "mongodb://h")).toBe(true);
    expect(isSensitiveConnectionKey("REDIS_HOST", "redis://:pw@h")).toBe(true);
  });

  it("redacts only the password of a DSN", () => {
    expect(redactConnectionURL("postgres://kuso:s3cret@e2e-db:5432/e2e?sslmode=disable")).toBe(
      "postgres://kuso:••••••••@e2e-db:5432/e2e?sslmode=disable",
    );
    expect(redactConnectionURL("redis://e2e-cache:6379")).toBe("");
    expect(redactConnectionURL("not a url")).toBe("");
  });
});
