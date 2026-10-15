import { gzipSync } from "node:zlib";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiError } from "@/lib/api-client";
import { RESTORE_MAX_BYTES, startControlPlaneRestore, validateRestoreFile } from "./api";

const blobOf = (b: Uint8Array) => new Blob([new Uint8Array(b)]);

describe("validateRestoreFile", () => {
  it("accepts a gzip dump inside the server's size window", async () => {
    const dump = gzipSync(Buffer.from("CREATE TABLE t (id int);\n".repeat(200)));
    await expect(validateRestoreFile(blobOf(dump))).resolves.toBeNull();
  });

  // The restore Job runs `gunzip -c | psql`; a plain .sql would only fail
  // inside the pod, after spending one of the 5 rate-limit tokens.
  it("rejects a non-gzip file", async () => {
    const sql = Buffer.from("CREATE TABLE t (id int);\n".repeat(20));
    await expect(validateRestoreFile(blobOf(sql))).resolves.toMatch(/not a gzip/i);
  });

  it("rejects files over the 900 KiB Secret cap", async () => {
    const big = new Uint8Array(RESTORE_MAX_BYTES + 1);
    big[0] = 0x1f;
    big[1] = 0x8b;
    await expect(validateRestoreFile(blobOf(big))).resolves.toMatch(/at most 900 KiB/);
  });

  it("rejects files under the server's 32-byte minimum", async () => {
    await expect(validateRestoreFile(blobOf(new Uint8Array([0x1f, 0x8b, 0])))).resolves.toMatch(
      /empty or truncated/
    );
  });
});

describe("startControlPlaneRestore", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("POSTs the raw bytes, not JSON", async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ jobName: "kuso-restore-x", statusUrl: "/api/admin/restore/kuso-restore-x" }), {
        status: 202,
      })
    );
    vi.stubGlobal("fetch", fetchMock);
    const file = blobOf(gzipSync(Buffer.from("x".repeat(100))));
    const out = await startControlPlaneRestore(file);
    expect(out.jobName).toBe("kuso-restore-x");
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe("/api/admin/restore");
    expect(init.method).toBe("POST");
    expect(init.body).toBe(file);
  });

  it("surfaces the server's error envelope (e.g. the rate limit)", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(
        new Response(JSON.stringify({ error: "restore rate limit exceeded" }), { status: 429 })
      )
    );
    const err = await startControlPlaneRestore(blobOf(new Uint8Array(64))).catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect((err as ApiError).status).toBe(429);
    expect((err as ApiError).message).toBe("restore rate limit exceeded");
  });
});
