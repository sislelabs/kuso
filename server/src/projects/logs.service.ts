// Read recent log lines from the pods backing a service's environment(s).
// Read-only; no streaming yet. Streaming will land later as a websocket
// route — for now `kuso logs` does a one-shot tail.
//
// Pod selector: deployments rendered by the kusoenvironment helm chart
// label their pods with `app.kubernetes.io/instance=<env-name>`. We
// list pods matching that label in the kuso namespace.

import { Injectable, Logger, NotFoundException } from '@nestjs/common';
import { CoreV1Api, V1Pod } from '@kubernetes/client-node';
import { KubernetesService } from '../kubernetes/kubernetes.service';
import { KusoResourcesService } from './kuso-resources.service';

export interface ServiceLogLine {
  pod: string;
  line: string;
}

export interface ServiceLogs {
  project: string;
  service: string;
  env: string;
  lines: ServiceLogLine[];
}

@Injectable()
export class LogsService {
  private readonly logger = new Logger(LogsService.name);

  constructor(
    private readonly kubectl: KubernetesService,
    private readonly resources: KusoResourcesService,
  ) {}

  /**
   * Return the last N log lines (combined across pods) for a service in
   * a given environment. env defaults to "production".
   */
  async tail(
    project: string,
    service: string,
    env: string = 'production',
    lines: number = 200,
  ): Promise<ServiceLogs> {
    const fqn = service.startsWith(`${project}-`)
      ? service
      : `${project}-${service}`;
    const envName = env.includes('-')
      ? env
      : `${fqn}-${env}`; // accept short ("production") or fqn form

    if (!(await this.resources.getEnvironment(envName))) {
      throw new NotFoundException(`environment ${envName} not found`);
    }

    const coreApi = (this.kubectl as any).coreV1Api as CoreV1Api;
    const ns = process.env.KUSO_NAMESPACE || 'kuso';
    const sel = `app.kubernetes.io/instance=${envName}`;
    const podList = await coreApi.listNamespacedPod(
      ns,
      undefined,
      undefined,
      undefined,
      undefined,
      sel,
    );
    const pods: V1Pod[] = podList.body.items || [];

    const out: ServiceLogLine[] = [];
    const perPod = Math.max(1, Math.floor(lines / Math.max(1, pods.length)));
    for (const pod of pods) {
      const podName = pod.metadata?.name;
      if (!podName) continue;
      try {
        const res = await coreApi.readNamespacedPodLog(
          podName,
          ns,
          undefined, // container — let it pick the only one
          false, // follow
          undefined, // insecureSkipTLSVerifyBackend
          undefined, // limitBytes
          undefined, // pretty
          undefined, // previous
          undefined, // sinceSeconds
          perPod, // tailLines
          false, // timestamps
        );
        const body = String(res.body || '');
        for (const l of body.split('\n')) {
          if (l !== '') out.push({ pod: podName, line: l });
        }
      } catch (e: any) {
        this.logger.warn(
          `tail logs for ${podName} failed: ${e?.message || e}`,
        );
      }
    }
    return {
      project,
      service: fqn,
      env: envName,
      lines: out.slice(-lines),
    };
  }
}
