// HTTP surface for the kuso GitHub App.
//
// Three groups of endpoints:
//
//   /api/webhooks/github       webhook receiver (no auth; HMAC verified)
//   /api/github/installations  list cached installations and their repos
//   /api/github/install-url    return the public install URL for the App
//   /api/github/repos/:owner/:repo/tree   list repo tree at a branch+path
//   /api/github/detect-runtime POST helper used by the project create flow

import {
  BadRequestException,
  Body,
  Controller,
  Get,
  Headers,
  HttpCode,
  Param,
  Post,
  Query,
  Req,
  UseGuards,
} from '@nestjs/common';
import { ApiBearerAuth, ApiOperation, ApiTags } from '@nestjs/swagger';
import { createHmac, timingSafeEqual } from 'crypto';
import type { Request } from 'express';
import { JwtAuthGuard } from '../auth/strategies/jwt.guard';
import { GithubService } from './github.service';
import { GithubWebhooksService } from './github-webhooks.service';

@ApiTags('github')
@Controller('api')
export class GithubController {
  constructor(
    private readonly github: GithubService,
    private readonly webhooks: GithubWebhooksService,
  ) {}

  // ---------------- webhook ----------------

  @Post('/webhooks/github')
  @HttpCode(204)
  @ApiOperation({ summary: 'GitHub App webhook receiver (HMAC verified)' })
  async webhook(
    @Headers('x-github-event') event: string,
    @Headers('x-hub-signature-256') signature: string,
    @Req() req: Request,
  ) {
    const secret = process.env.GITHUB_APP_WEBHOOK_SECRET;
    const raw = (req as any).rawBody as Buffer | undefined;
    if (!secret || !raw) {
      // No secret configured or body wasn't captured raw → reject. Refuse
      // to silently no-op because the alternative is accepting unsigned
      // events.
      throw new BadRequestException('webhook not configured');
    }
    if (!verifySignature(secret, raw, signature)) {
      throw new BadRequestException('invalid webhook signature');
    }
    const payload = JSON.parse(raw.toString('utf8'));
    await this.webhooks.dispatch(event, payload);
  }

  // ---------------- installations + auth ----------------

  @Get('/github/install-url')
  @UseGuards(JwtAuthGuard)
  @ApiBearerAuth('bearerAuth')
  @ApiOperation({ summary: 'Public URL to install the kuso GitHub App' })
  installUrl() {
    return {
      configured: this.github.isConfigured(),
      url: this.github.getInstallUrl(),
    };
  }

  @Get('/github/installations')
  @UseGuards(JwtAuthGuard)
  @ApiBearerAuth('bearerAuth')
  @ApiOperation({ summary: 'List GitHub App installations cached locally' })
  async installations() {
    return this.github.listInstallations();
  }

  @Get('/github/installations/:id/repos')
  @UseGuards(JwtAuthGuard)
  @ApiBearerAuth('bearerAuth')
  @ApiOperation({ summary: 'List repos accessible via this installation' })
  async installationRepos(@Param('id') id: string) {
    return this.github.listInstallationRepos(Number(id));
  }

  @Post('/github/installations/refresh')
  @UseGuards(JwtAuthGuard)
  @ApiBearerAuth('bearerAuth')
  @ApiOperation({ summary: 'Force a refresh of the installation cache from GitHub' })
  async refreshInstallations() {
    await this.github.refreshInstallations();
    return { ok: true };
  }

  // ---------------- repo introspection ----------------

  @Get('/github/installations/:id/repos/:owner/:repo/tree')
  @UseGuards(JwtAuthGuard)
  @ApiBearerAuth('bearerAuth')
  @ApiOperation({ summary: 'List a repo tree at a branch + path' })
  async repoTree(
    @Param('id') id: string,
    @Param('owner') owner: string,
    @Param('repo') repo: string,
    @Query('branch') branch: string,
    @Query('path') path?: string,
  ) {
    if (!branch) throw new BadRequestException('branch query param is required');
    return this.github.listRepoTree(Number(id), owner, repo, branch, path || '');
  }

  @Post('/github/detect-runtime')
  @UseGuards(JwtAuthGuard)
  @ApiBearerAuth('bearerAuth')
  @ApiOperation({ summary: 'Auto-detect runtime + port for a service' })
  async detectRuntime(
    @Body()
    body: {
      installationId: number;
      owner: string;
      repo: string;
      branch: string;
      path?: string;
    },
  ) {
    if (!body?.installationId || !body?.owner || !body?.repo || !body?.branch) {
      throw new BadRequestException(
        'installationId, owner, repo, and branch are required',
      );
    }
    return this.github.detectRuntime(
      body.installationId,
      body.owner,
      body.repo,
      body.branch,
      body.path || '',
    );
  }
}

function verifySignature(secret: string, raw: Buffer, signature: string): boolean {
  if (!signature || !signature.startsWith('sha256=')) return false;
  const expected = 'sha256=' + createHmac('sha256', secret).update(raw).digest('hex');
  const a = Buffer.from(expected);
  const b = Buffer.from(signature);
  if (a.length !== b.length) return false;
  return timingSafeEqual(a, b);
}
