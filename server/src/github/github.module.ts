import { Module, forwardRef } from '@nestjs/common';
import { GithubController } from './github.controller';
import { GithubService } from './github.service';
import { GithubWebhooksService } from './github-webhooks.service';
import { ProjectsModule } from '../projects/projects.module';

@Module({
  imports: [forwardRef(() => ProjectsModule)],
  providers: [GithubService, GithubWebhooksService],
  controllers: [GithubController],
  exports: [GithubService],
})
export class GithubModule {}
