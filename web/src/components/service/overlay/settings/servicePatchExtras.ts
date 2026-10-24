import type { PatchServiceBody } from "@/features/services";
import type { FormState } from "./_primitives";

// Mirrors the server's envNameValid: build-arg keys and public-env names
// become env vars, so they must be POSIX identifiers.
const ENV_NAME_RE = /^[A-Za-z_][A-Za-z0-9_]*$/;

export function parseBuildArgs(text: string): { args: Record<string, string> } | { error: string } {
  const args: Record<string, string> = {};
  for (const raw of text.split("\n")) {
    const line = raw.trim();
    if (!line) continue;
    const eq = line.indexOf("=");
    const key = (eq === -1 ? line : line.slice(0, eq)).trim();
    if (!ENV_NAME_RE.test(key)) {
      return { error: `Build arg "${key}" must be a POSIX env-var name (letters, digits, _; not starting with a digit)` };
    }
    if (key in args) return { error: `Build arg "${key}" is listed twice` };
    args[key] = eq === -1 ? "" : line.slice(eq + 1);
  }
  return { args };
}

export function parsePublicEnv(text: string): { names: string[] } | { error: string } {
  const names: string[] = [];
  for (const raw of text.split(/[\n,]/)) {
    const name = raw.trim();
    if (!name) continue;
    if (!ENV_NAME_RE.test(name)) return { error: `Public env "${name}" must be a POSIX env-var name` };
    if (!names.includes(name)) names.push(name);
  }
  return { names };
}

// extraServicePatch builds the PATCH fields for the settings added in the
// CLI-parity pass (egress, snapshot, start command, build-time config).
// Only changed fields are included; blocks the server replaces wholesale
// (static, buildpacks, buildArgs, publicEnv) are sent whole.
export function extraServicePatch(
  state: FormState,
  base: FormState,
  originalCommand: string[] | undefined,
): { body: PatchServiceBody } | { error: string } {
  const body: PatchServiceBody = {};
  if (state.privateEgress !== base.privateEgress) body.privateEgress = state.privateEgress;
  if (state.platformApiEgress !== base.platformApiEgress) body.platformApiEgress = state.platformApiEgress;
  if (state.snapshotBeforeDeploy !== base.snapshotBeforeDeploy) {
    body.snapshotBeforeDeploy = state.snapshotBeforeDeploy;
  }
  if (state.startCommand !== base.startCommand) {
    // The text is the stored argv joined by spaces; whitespace-only edits
    // re-send the original argv so quoted args survive.
    const argv = state.startCommand.trim().split(/\s+/).filter(Boolean);
    const same = originalCommand && argv.join(" ") === originalCommand.join(" ");
    if (!same) body.command = argv;
  }
  if (state.buildArgs !== base.buildArgs) {
    const parsed = parseBuildArgs(state.buildArgs);
    if ("error" in parsed) return parsed;
    body.buildArgs = parsed.args;
  }
  if (state.publicEnv !== base.publicEnv) {
    const parsed = parsePublicEnv(state.publicEnv);
    if ("error" in parsed) return parsed;
    body.publicEnv = parsed.names;
  }
  if (
    state.staticBuilderImage !== base.staticBuilderImage ||
    state.staticRuntimeImage !== base.staticRuntimeImage ||
    state.staticBuildCmd !== base.staticBuildCmd ||
    state.staticOutputDir !== base.staticOutputDir
  ) {
    body.static = dropEmpty({
      builderImage: state.staticBuilderImage,
      runtimeImage: state.staticRuntimeImage,
      buildCmd: state.staticBuildCmd,
      outputDir: state.staticOutputDir,
    });
  }
  if (
    state.buildpacksBuilderImage !== base.buildpacksBuilderImage ||
    state.buildpacksLifecycleImage !== base.buildpacksLifecycleImage
  ) {
    body.buildpacks = dropEmpty({
      builderImage: state.buildpacksBuilderImage,
      lifecycleImage: state.buildpacksLifecycleImage,
    });
  }
  return { body };
}

function dropEmpty<T extends Record<string, string>>(o: T): Partial<T> {
  const out: Partial<T> = {};
  for (const k of Object.keys(o) as (keyof T)[]) {
    const v = o[k].trim();
    if (v) out[k] = v as T[keyof T];
  }
  return out;
}
