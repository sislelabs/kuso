// Server response carries the external registry override, which the builds
// page doesn't edit but must PUT back unchanged (the server writes every key).
// maxConcurrentSet is only present on servers that report whether the cap was
// explicitly chosen; when it's false the server sizes the cap adaptively.
export interface BuildSettingsResponse {
  maxConcurrent: number;
  memoryLimit: string;
  memoryRequest: string;
  cpuLimit: string;
  cpuRequest: string;
  registryAuthSecret?: string;
  registryHost?: string;
  maxConcurrentSet?: boolean;
}

// buildSettingsPayload decides what to PUT. maxConcurrent is left out only
// when the server told us it's unset AND the admin never touched it, so a
// save of the memory fields doesn't pin the cap to the default 1.
export function buildSettingsPayload(
  s: BuildSettingsResponse,
  maxConcurrentEdited: boolean,
): Partial<BuildSettingsResponse> {
  const { maxConcurrentSet, maxConcurrent, ...rest } = s;
  if (maxConcurrentSet === false && !maxConcurrentEdited) return rest;
  return { ...rest, maxConcurrent };
}
