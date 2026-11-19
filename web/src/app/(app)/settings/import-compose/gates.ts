// Mirrors the CLI's `kuso import compose --apply` gates: each risk has to
// be acknowledged before Apply is allowed.
export interface ComposeApplyRisks {
  missingEnvFiles: number;
  unresolvedVars: number;
  emptyAddons: number;
}

export interface ComposeApplyAcks {
  allowMissingEnvFiles: boolean;
  allowUnresolvedVars: boolean;
  allowEmptyAddons: boolean;
}

export function composeApplyBlocked(risks: ComposeApplyRisks, acks: ComposeApplyAcks): boolean {
  return (
    (risks.missingEnvFiles > 0 && !acks.allowMissingEnvFiles) ||
    (risks.unresolvedVars > 0 && !acks.allowUnresolvedVars) ||
    (risks.emptyAddons > 0 && !acks.allowEmptyAddons)
  );
}
