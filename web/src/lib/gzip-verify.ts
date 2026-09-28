// verifyCompleteGzip resolves only if blob is a gzip stream that
// decompresses to its end with a non-empty payload.
//
// The addon backup download streams pg_dump output as gzip. If the dump
// dies mid-stream the server can't change the status code any more; it
// leaves the gzip without its footer and reports "failed" in an HTTP
// trailer. Browsers never expose trailers to fetch(), so a truncated
// stream is the only signal the web client can see.
export async function verifyCompleteGzip(blob: Blob): Promise<void> {
  if (blob.size === 0) {
    throw new Error("Backup download is empty — the dump produced no data.");
  }
  let decompressed = 0;
  try {
    const reader = blob.stream().pipeThrough(new DecompressionStream("gzip")).getReader();
    for (;;) {
      const { done, value } = await reader.read();
      if (done) break;
      decompressed += value.byteLength;
    }
  } catch {
    throw new Error(
      "Backup download is incomplete — the dump failed mid-stream on the server. Nothing was saved; try again."
    );
  }
  if (decompressed === 0) {
    throw new Error("Backup download is empty — the dump produced no data.");
  }
}
