// The trigger endpoint coalesces into an in-flight build for the same
// service+branch and answers existing=true (200 instead of 201). Toasts
// must not claim a fresh start in that case.
export function buildTriggerMessage(
  result: { existing?: boolean },
  fresh: string,
  service?: string,
): string {
  if (!result.existing) return fresh;
  return service
    ? `A build for ${service} is already in progress`
    : "A build is already in progress";
}
