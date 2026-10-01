export interface CronImage {
  repository?: string;
  tag?: string;
  pullPolicy?: string;
  pullSecret?: string;
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
}

function argv(cmd: string): string[] {
  return cmd.trim().split(/\s+/).filter(Boolean);
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
    body.command = argv(form.cmd);
  }
  return body;
}

// Body for PATCH /api/projects/{p}/services/{svc}/crons/{name}.
export function serviceCronPatch(form: CronForm): Record<string, unknown> {
  return {
    displayName: form.displayName.trim(),
    schedule: form.schedule.trim(),
    suspend: form.suspend,
    pinImage: form.pinImage,
    command: argv(form.cmd),
  };
}
