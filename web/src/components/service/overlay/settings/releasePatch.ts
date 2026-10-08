export type ReleasePatch = { command: string[]; timeoutSeconds: number } | { clear: true };

// Body for the `release` field of a service PATCH, or undefined when the
// hook is unchanged. The text field is the stored argv joined by spaces,
// so an untouched command re-sends `originalArgv` verbatim: re-splitting
// it would break quoted args like ["sh", "-c", "migrate && seed"].
export function releasePatch(
  command: string,
  timeout: string,
  baseCommand: string,
  baseTimeout: string,
  originalArgv: string[] | undefined,
): ReleasePatch | undefined {
  if (command === baseCommand && timeout === baseTimeout) return undefined;
  const argv =
    command === baseCommand && originalArgv && originalArgv.length > 0
      ? originalArgv
      : command.trim().split(/\s+/).filter(Boolean);
  if (argv.length > 0) return { command: argv, timeoutSeconds: Number(timeout) || 0 };
  if (baseCommand.trim().length > 0) return { clear: true };
  return undefined;
}
