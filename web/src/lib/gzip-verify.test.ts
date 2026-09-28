import { gzipSync } from "node:zlib";
import { describe, expect, it } from "vitest";
import { verifyCompleteGzip } from "./gzip-verify";

const blobOf = (b: Uint8Array) => new Blob([new Uint8Array(b)]);

describe("verifyCompleteGzip", () => {
  const dump = gzipSync(Buffer.from("CREATE TABLE t (id int);\n".repeat(2000)));

  it("accepts a complete gzip", async () => {
    await expect(verifyCompleteGzip(blobOf(dump))).resolves.toBeUndefined();
  });

  // What the server sends when pg_dump dies mid-stream: the gzip is left
  // without its footer (the X-Kuso-Backup-Status trailer, which the
  // browser can't read, says "failed").
  it("rejects a gzip truncated mid-stream", async () => {
    await expect(verifyCompleteGzip(blobOf(dump.subarray(0, dump.length - 8)))).rejects.toThrow(
      /incomplete/i
    );
    await expect(verifyCompleteGzip(blobOf(dump.subarray(0, dump.length / 2)))).rejects.toThrow(
      /incomplete/i
    );
  });

  it("rejects an empty body and a gzip of nothing", async () => {
    await expect(verifyCompleteGzip(new Blob([]))).rejects.toThrow(/empty/i);
    await expect(verifyCompleteGzip(blobOf(gzipSync(Buffer.alloc(0))))).rejects.toThrow(/empty/i);
  });

  it("rejects a body that isn't gzip", async () => {
    await expect(verifyCompleteGzip(new Blob(["backup failed: boom"]))).rejects.toThrow(
      /incomplete/i
    );
  });
});
