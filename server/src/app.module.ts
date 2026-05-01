// Root NestJS module. v0.2 — see docs/REDESIGN.md.
//
// Legacy modules removed in the v0.2.x cleanup pass:
//   apps, pipelines, deployments, repo, addons (v1), security
// Their behaviour is owned by ProjectsModule + GithubModule, or was
// pipeline-shaped only and is gone for good.

import { join } from 'path';
import { Module } from '@nestjs/common';
import { AppController } from './app.controller';
import { AppService } from './app.service';
import { EventsModule } from './events/events.module';
import { ServeStaticModule } from '@nestjs/serve-static';
import { AuthModule } from './auth/auth.module';
import { ConfigModule } from './config/config.module';
import { MetricsModule } from './metrics/metrics.module';
// LogsModule was v0.1-pipeline shaped; v0.2 log streaming lands later.
import { KubernetesModule } from './kubernetes/kubernetes.module';
import { AuditModule } from './audit/audit.module';
import { NotificationsModule } from './notifications/notifications.module';
import { TemplatesController } from './templates/templates.controller';
import { TemplatesService } from './templates/templates.service';
import { StatusModule } from './status/status.module';
import { DatabaseModule } from './database/database.module';
import { GroupModule } from './groups/groups.module';
import { RolesModule } from './roles/roles.module';
import { TokenModule } from './token/token.module';
import { CliModule } from './cli/cli.module';
import { ProjectsModule } from './projects/projects.module';
import { GithubModule } from './github/github.module';
import { ScheduleModule } from '@nestjs/schedule';

@Module({
  imports: [
    ServeStaticModule.forRoot({
      rootPath: join(__dirname, 'public'),
    }),
    EventsModule,
    AuthModule,
    ConfigModule,
    MetricsModule,
    KubernetesModule,
    AuditModule,
    NotificationsModule,
    StatusModule,
    DatabaseModule,
    GroupModule,
    RolesModule,
    TokenModule,
    CliModule,
    ProjectsModule,
    GithubModule,
    ScheduleModule.forRoot(),
  ],
  controllers: [AppController, TemplatesController],
  providers: [AppService, TemplatesService],
})
export class AppModule {}
