// Staged changes: edits to a service's variables and settings that are
// held in the browser until the user deploys them together, so each
// service rolls once instead of once per card.

export interface StagedEnvSet {
  name: string;
  value: string;
  // The deployed value when staged; null for a new variable. Undefined
  // when the editor couldn't see it (masked/secret-backed).
  before: string | null | undefined;
  // Secret-looking values are kept in memory only and shown by
  // fingerprint, never in clear, in the review.
  sensitive: boolean;
}

export interface StagedEnvUnset {
  name: string;
  before: string | undefined;
  sensitive: boolean;
}

export interface StagedEnv {
  set: StagedEnvSet[];
  unset: StagedEnvUnset[];
}

// Settings are stored as raw form-field overrides. `base` keeps each
// field's deployed value at staging time so the review can say when
// someone else changed it since.
export interface StagedSettings {
  fields: Record<string, unknown>;
  base: Record<string, unknown>;
}

export interface StagedService {
  env?: StagedEnv;
  settings?: StagedSettings;
  // Last apply error per item key ("env:NAME" or "settings"), kept so a
  // partial failure shows on the item and the user can retry it.
  errors?: Record<string, string>;
}

export type StagedProject = Record<string, StagedService>;
export type StagedState = Record<string, StagedProject>;
