// fingerprint labels a secret value in the review without showing it, so
// two staged values can be told apart ("•••• a1f3c9"). FNV-1a: not a
// security primitive, and only 24 bits are shown.
export function fingerprint(value: string): string {
  let h = 0x811c9dc5;
  for (let i = 0; i < value.length; i++) {
    h ^= value.charCodeAt(i);
    h = Math.imul(h, 0x01000193);
  }
  return `•••• ${((h >>> 0) & 0xffffff).toString(16).padStart(6, "0")}`;
}
