export interface CronImage {
  repository?: string;
  tag?: string;
  pullPolicy?: string;
  pullSecret?: string;
}

// Run-policy knobs shared by every cron kind. Strings so a field can be
// blank: blank deadline = 0 = none (startingDeadline defaults to 300 at
// create). concurrencyPolicy "" = server default (Forbid).
export interface CronPolicy {
  concurrencyPolicy: string;
  activeDeadlineSeconds: string;
  startingDeadlineSeconds: string;
}

// Failure webhook (project-scoped crons only). secretName/secretKey name
// the HMAC signing key; the server only accepts this project's own
// secrets (<project>-shared, addon conn secrets, kuso-instance-shared).
export interface CronOnFailureForm {
  webhookURL: string;
  secretName: string;
  secretKey: string;
}

export const CONCURRENCY_POLICIES = ["Forbid", "Allow", "Replace"] as const;

export function policyForm(spec?: {
  concurrencyPolicy?: string;
  activeDeadlineSeconds?: number;
  startingDeadlineSeconds?: number;
}): CronPolicy {
  return {
    concurrencyPolicy: spec?.concurrencyPolicy ?? "",
    activeDeadlineSeconds: spec?.activeDeadlineSeconds ? String(spec.activeDeadlineSeconds) : "",
    startingDeadlineSeconds: spec?.startingDeadlineSeconds ? String(spec.startingDeadlineSeconds) : "",
  };
}

export function onFailureForm(of?: { webhookURL?: string; secretRef?: { name?: string; key?: string } }): CronOnFailureForm {
  return {
    webhookURL: of?.webhookURL ?? "",
    secretName: of?.secretRef?.name ?? "",
    secretKey: of?.secretRef?.key ?? "",
  };
}

function seconds(label: string, raw: string): number {
  const v = raw.trim();
  if (!v) return 0;
  const n = Number(v);
  if (!Number.isInteger(n) || n < 0) throw new Error(`${label} must be a whole number of seconds`);
  return n;
}

function concurrency(raw: string): string {
  if (raw && !(CONCURRENCY_POLICIES as readonly string[]).includes(raw)) {
    throw new Error("Concurrency must be Forbid, Allow or Replace");
  }
  return raw;
}

// policyCreateFields: blank fields are omitted so server defaults apply.
export function policyCreateFields(p: CronPolicy): Record<string, unknown> {
  const out: Record<string, unknown> = {};
  const c = concurrency(p.concurrencyPolicy);
  if (c) out.concurrencyPolicy = c;
  const a = seconds("Timeout", p.activeDeadlineSeconds);
  if (a) out.activeDeadlineSeconds = a;
  const st = seconds("Starting deadline", p.startingDeadlineSeconds);
  if (st) out.startingDeadlineSeconds = st;
  return out;
}

// policyPatchFields sends only the knobs that changed.
export function policyPatchFields(next: CronPolicy, base: CronPolicy): Record<string, unknown> {
  const out: Record<string, unknown> = {};
  if (next.concurrencyPolicy !== base.concurrencyPolicy && next.concurrencyPolicy) {
    out.concurrencyPolicy = concurrency(next.concurrencyPolicy);
  }
  if (next.activeDeadlineSeconds.trim() !== base.activeDeadlineSeconds.trim()) {
    out.activeDeadlineSeconds = seconds("Timeout", next.activeDeadlineSeconds);
  }
  if (next.startingDeadlineSeconds.trim() !== base.startingDeadlineSeconds.trim()) {
    out.startingDeadlineSeconds = seconds("Starting deadline", next.startingDeadlineSeconds);
  }
  return out;
}

function onFailureValue(f: CronOnFailureForm): { webhookURL: string; secretRef?: { name: string; key: string } } {
  const webhookURL = f.webhookURL.trim();
  if (!/^https?:\/\//i.test(webhookURL)) throw new Error("Failure webhook must be an http(s) URL");
  const name = f.secretName.trim();
  const key = f.secretKey.trim();
  if (!name && key) throw new Error("Set the signing secret's name, or clear its key");
  return name ? { webhookURL, secretRef: { name, key } } : { webhookURL };
}

export function onFailureCreateField(f: CronOnFailureForm): Record<string, unknown> {
  return f.webhookURL.trim() ? { onFailure: onFailureValue(f) } : {};
}

// onFailurePatchField: emptied URL clears the webhook; an edit replaces it.
export function onFailurePatchField(next: CronOnFailureForm, base: CronOnFailureForm): Record<string, unknown> {
  if (JSON.stringify(next) === JSON.stringify(base)) return {};
  if (!next.webhookURL.trim()) return base.webhookURL.trim() ? { onFailure: { clear: true } } : {};
  return { onFailure: onFailureValue(next) };
}

export interface CronForm {
  kind: string;
  displayName: string;
  schedule: string;
  suspend: boolean;
  pinImage: boolean;
  url: string;
  imageRepo: string;
  imageTag: string;
  cmd: string;
  // The argv the form was seeded with. `cmd` is a whitespace-joined
  // rendering of it, so re-splitting an untouched field would mangle
  // quoted args like ["sh", "-c", "a b"].
  initialCommand?: string[];
  // Policy knobs + their seeded values (only changed ones are sent).
  policy?: CronPolicy;
  initialPolicy?: CronPolicy;
  // Project-scoped crons only.
  onFailure?: CronOnFailureForm;
  initialOnFailure?: CronOnFailureForm;
}

function extras(form: CronForm, withOnFailure: boolean): Record<string, unknown> {
  const out: Record<string, unknown> = {};
  if (form.policy) Object.assign(out, policyPatchFields(form.policy, form.initialPolicy ?? policyForm()));
  if (withOnFailure && form.onFailure) {
    Object.assign(out, onFailurePatchField(form.onFailure, form.initialOnFailure ?? onFailureForm()));
  }
  return out;
}

function argv(cmd: string): string[] {
  return cmd.trim().split(/\s+/).filter(Boolean);
}

// Omitted from the PATCH (server keeps spec.command) unless edited.
function editedCommand(form: CronForm): string[] | undefined {
  if (form.initialCommand && form.cmd === form.initialCommand.join(" ")) return undefined;
  return argv(form.cmd);
}

// Body for PATCH /api/projects/{p}/crons/{name} (kind=http|command).
// The server replaces spec.image wholesale, so fields the form doesn't
// edit (pullSecret, pullPolicy) are carried over from the current spec.
export function projectCronPatch(form: CronForm, current?: CronImage): Record<string, unknown> {
  const body: Record<string, unknown> = {
    displayName: form.displayName.trim(),
    schedule: form.schedule.trim(),
    suspend: form.suspend,
    pinImage: form.pinImage,
  };
  if (form.kind === "http") {
    body.url = form.url.trim();
  } else if (form.kind === "command") {
    if (form.imageRepo.trim()) {
      const image: CronImage = { repository: form.imageRepo.trim() };
      if (form.imageTag.trim()) image.tag = form.imageTag.trim();
      if (current?.pullPolicy) image.pullPolicy = current.pullPolicy;
      if (current?.pullSecret) image.pullSecret = current.pullSecret;
      body.image = image;
    }
    const command = editedCommand(form);
    if (command) body.command = command;
  }
  return { ...body, ...extras(form, true) };
}

// Body for PATCH /api/projects/{p}/services/{svc}/crons/{name}.
export function serviceCronPatch(form: CronForm): Record<string, unknown> {
  const body: Record<string, unknown> = {
    displayName: form.displayName.trim(),
    schedule: form.schedule.trim(),
    suspend: form.suspend,
    pinImage: form.pinImage,
  };
  const command = editedCommand(form);
  if (command) body.command = command;
  return { ...body, ...extras(form, false) };
}
