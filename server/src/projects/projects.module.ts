import { Module, forwardRef } from '@nestjs/common';
import { ProjectsController } from './projects.controller';
import { ProjectsService } from './projects.service';
import { KusoResourcesService } from './kuso-resources.service';
import { PreviewCleanupService } from './preview-cleanup.service';
import { BuildsService } from './builds.service';
import { GithubModule } from '../github/github.module';

// KubernetesModule is @Global() so we don't need to import it here.
// GithubModule is wrapped in forwardRef because it imports ProjectsModule
// (for the webhook handler's access to projects/services).
@Module({
  imports: [forwardRef(() => GithubModule)],
  providers: [
    ProjectsService,
    KusoResourcesService,
    PreviewCleanupService,
    BuildsService,
  ],
  controllers: [ProjectsController],
  exports: [ProjectsService, KusoResourcesService, BuildsService],
})
export class ProjectsModule {}
