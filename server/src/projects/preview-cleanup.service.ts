// Preview environment TTL reconciler.
//
// Webhooks (github-webhooks.service.ts) are the primary mechanism that
// tears down preview envs — pull_request "closed" events fire and we
// delete the matching KusoEnvironment. But webhooks miss for predictable
// reasons:
//
//   - the GitHub App was uninstalled / suspended
//   - the webhook delivery failed and wasn't retried by GitHub
//   - the project's previews flag flipped off after a PR was open
//   - a kuso outage spanned the close event
//
// Without a fallback, dead preview envs accumulate forever, holding pods,
// PVCs, and certs. This reconciler is the safety net: every 5 minutes it
// scans every KusoEnvironment with kind=preview and spec.ttl.expiresAt
// in the past, and deletes them. The TTL is set when the env is created
// (default 7 days from project.spec.previews.ttlDays) and refreshed on
// every push to the PR head ref by ensurePreviewEnv.
//
// Disabled by setting KUSO_PREVIEW_CLEANUP_DISABLED=true (useful in dev).

import { Injectable, Logger } from '@nestjs/common';
import { Cron, CronExpression } from '@nestjs/schedule';
import { KusoResourcesService } from './kuso-resources.service';

@Injectable()
export class PreviewCleanupService {
  private readonly logger = new Logger(PreviewCleanupService.name);

  constructor(private readonly resources: KusoResourcesService) {}

  // Every 5 minutes. Cheap (one list call across the kuso namespace),
  // and 5min is well under the smallest sane TTL (1 day).
  @Cron(CronExpression.EVERY_5_MINUTES, {
    name: 'preview-cleanup',
    disabled: false, // toggled via guard below
  })
  async sweep(): Promise<void> {
    if (process.env.KUSO_PREVIEW_CLEANUP_DISABLED === 'true') return;

    let envs;
    try {
      envs = await this.resources.listEnvironments();
    } catch (e: any) {
      // CRDs may not exist yet on a fresh cluster; the controller is
      // expected to start before Phase 7 reinstall. Log once at debug
      // level and keep going.
      this.logger.debug(`listEnvironments failed (CRDs missing?): ${e?.message}`);
      return;
    }

    const now = Date.now();
    const expired = envs.filter(
      (e) =>
        e.spec.kind === 'preview' &&
        e.spec.ttl?.expiresAt &&
        Date.parse(e.spec.ttl.expiresAt) < now,
    );

    if (expired.length === 0) return;
    this.logger.log(`preview-cleanup: deleting ${expired.length} expired env(s)`);

    for (const env of expired) {
      try {
        await this.resources.deleteEnvironment(env.metadata.name);
        this.logger.log(`preview-cleanup: deleted ${env.metadata.name}`);
      } catch (e: any) {
        this.logger.warn(
          `preview-cleanup: failed to delete ${env.metadata.name}: ${e?.message}`,
        );
      }
    }
  }
}
