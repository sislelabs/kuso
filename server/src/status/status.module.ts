// Status module — exposes /status (Prometheus metrics).
//
// v0.2 cleanup: dropped the StatusService cron that emitted
// kuso_pipelines_total / kuso_apps_total — those concepts no longer
// exist. Project / service / environment / build counters can be added
// on top of ProjectsService when there's a use for them.

import { Module } from '@nestjs/common';
import { PrometheusModule } from '@willsoto/nestjs-prometheus';
import { StatusController } from './status.controller';

@Module({
  imports: [
    PrometheusModule.register({
      controller: StatusController,
    }),
  ],
})
export class StatusModule {}
