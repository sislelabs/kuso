import { Module } from '@nestjs/common';
import { ProjectsController } from './projects.controller';
import { ProjectsService } from './projects.service';
import { KusoResourcesService } from './kuso-resources.service';
import { PreviewCleanupService } from './preview-cleanup.service';

// KubernetesModule is @Global() so we don't need to import it here.
@Module({
  providers: [ProjectsService, KusoResourcesService, PreviewCleanupService],
  controllers: [ProjectsController],
  exports: [ProjectsService, KusoResourcesService],
})
export class ProjectsModule {}
