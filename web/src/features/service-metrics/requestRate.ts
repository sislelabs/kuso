// How the Requests chart counts. Below one request a second a per-second
// rate reads as "<0.01/s" or "0/s" on a service that did get traffic, so
// the chart switches to requests per sample bucket ("3 per 30s").

export interface RequestScale {
  perBucket: boolean;
  // Seconds per bucket; multiply a per-second rate by this.
  bucketSec: number;
  // "per 30s", "per 5m"; "/s" in rate mode.
  unit: string;
}

export function requestScale(peakPerSec: number | null, stepMs: number): RequestScale {
  const bucketSec = Math.round(stepMs / 1000);
  if (peakPerSec === null || peakPerSec >= 1 || bucketSec < 2) return { perBucket: false, bucketSec: 1, unit: "/s" };
  return { perBucket: true, bucketSec, unit: `per ${fmtBucket(bucketSec)}` };
}

function fmtBucket(sec: number): string {
  if (sec % 3600 === 0) return `${sec / 3600}h`;
  if (sec % 60 === 0) return `${sec / 60}m`;
  return `${sec}s`;
}

// fmtRequestCount: an estimated count (rate × bucket), so a fraction
// below 10 keeps one decimal instead of rounding a trickle to 0.
export function fmtRequestCount(n: number): string {
  if (n === 0) return "0";
  if (n < 0.1) return "<0.1";
  if (n < 10) return n.toFixed(1).replace(/\.0$/, "");
  if (n < 1000) return Math.round(n).toLocaleString("en-US");
  if (n < 1_000_000) return `${(n / 1000).toFixed(1).replace(/\.0$/, "")}K`;
  return `${(n / 1_000_000).toFixed(1).replace(/\.0$/, "")}M`;
}
