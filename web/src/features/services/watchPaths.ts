// parseWatchPathsText turns the settings textarea (one glob per line,
// commas also accepted) into the spec.watchPaths list. An empty result
// clears the list, so every push builds the service again.
export function parseWatchPathsText(text: string): string[] {
  return text
    .split(/[\n,]/)
    .map((p) => p.trim())
    .filter(Boolean);
}
