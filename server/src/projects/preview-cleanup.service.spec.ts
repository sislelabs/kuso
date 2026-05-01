import { PreviewCleanupService } from './preview-cleanup.service';
import { KusoResourcesService } from './kuso-resources.service';
import { KusoEnvironment } from './projects.types';

// Pure-logic test: stub KusoResourcesService.listEnvironments + delete
// and check that sweep() deletes only expired preview envs.

describe('PreviewCleanupService.sweep', () => {
  function makeEnv(name: string, kind: 'production' | 'preview', expiresAt?: string): KusoEnvironment {
    return {
      metadata: { name },
      spec: {
        project: 'p',
        service: 'p-s',
        kind,
        branch: 'main',
        ttl: expiresAt ? { expiresAt } : undefined,
      },
    } as KusoEnvironment;
  }

  function makeService(envs: KusoEnvironment[]) {
    const deleted: string[] = [];
    const stub = {
      listEnvironments: jest.fn().mockResolvedValue(envs),
      deleteEnvironment: jest.fn(async (name: string) => { deleted.push(name); }),
    } as unknown as KusoResourcesService;
    return { stub, deleted };
  }

  const past = new Date(Date.now() - 60_000).toISOString();
  const future = new Date(Date.now() + 24 * 3600 * 1000).toISOString();

  it('deletes expired preview envs', async () => {
    const { stub, deleted } = makeService([
      makeEnv('p-s-pr-1', 'preview', past),
      makeEnv('p-s-pr-2', 'preview', future),
      makeEnv('p-s-production', 'production'),
    ]);
    const svc = new PreviewCleanupService(stub);
    await svc.sweep();
    expect(deleted).toEqual(['p-s-pr-1']);
  });

  it('skips production envs even if they had a stale ttl', async () => {
    const { stub, deleted } = makeService([
      makeEnv('p-s-production', 'production', past),
    ]);
    const svc = new PreviewCleanupService(stub);
    await svc.sweep();
    expect(deleted).toEqual([]);
  });

  it('handles empty list cleanly', async () => {
    const { stub, deleted } = makeService([]);
    const svc = new PreviewCleanupService(stub);
    await svc.sweep();
    expect(deleted).toEqual([]);
  });

  it('honours KUSO_PREVIEW_CLEANUP_DISABLED', async () => {
    const { stub, deleted } = makeService([
      makeEnv('p-s-pr-1', 'preview', past),
    ]);
    const svc = new PreviewCleanupService(stub);
    process.env.KUSO_PREVIEW_CLEANUP_DISABLED = 'true';
    try {
      await svc.sweep();
    } finally {
      delete process.env.KUSO_PREVIEW_CLEANUP_DISABLED;
    }
    expect(deleted).toEqual([]);
    expect((stub.listEnvironments as jest.Mock)).not.toHaveBeenCalled();
  });
});
