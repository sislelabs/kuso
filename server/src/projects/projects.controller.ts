import {
  Body,
  Controller,
  Delete,
  Get,
  Param,
  Post,
  Query,
  UseGuards,
} from '@nestjs/common';
import { ApiBearerAuth, ApiOperation, ApiTags } from '@nestjs/swagger';
import { JwtAuthGuard } from '../auth/strategies/jwt.guard';
import { PermissionsGuard } from '../auth/permissions.guard';
import { Permissions } from '../auth/permissions.decorator';
import { ProjectsService } from './projects.service';
import { BuildsService } from './builds.service';
import { LogsService } from './logs.service';
import {
  CreateAddonDTO,
  CreateProjectDTO,
  CreateServiceDTO,
} from './projects.types';
import { CreateBuildRequest } from './builds.types';

@ApiTags('projects')
@Controller({ path: 'api/projects' })
@UseGuards(JwtAuthGuard, PermissionsGuard)
@ApiBearerAuth('bearerAuth')
export class ProjectsController {
  constructor(
    private readonly projects: ProjectsService,
    private readonly builds: BuildsService,
    private readonly logs: LogsService,
  ) {}

  // ---------------- projects ----------------

  @Get('/')
  @Permissions('app:read', 'app:write')
  @ApiOperation({ summary: 'List all projects' })
  list() {
    return this.projects.list();
  }

  @Post('/')
  @Permissions('app:write')
  @ApiOperation({ summary: 'Create a new project' })
  create(@Body() dto: CreateProjectDTO) {
    return this.projects.create(dto);
  }

  @Get(':project')
  @Permissions('app:read', 'app:write')
  @ApiOperation({
    summary: 'Get a project with services, environments, addons rolled up',
  })
  describe(@Param('project') project: string) {
    return this.projects.describe(project);
  }

  @Delete(':project')
  @Permissions('app:write')
  @ApiOperation({ summary: 'Cascade-delete a project' })
  remove(@Param('project') project: string) {
    return this.projects.delete(project);
  }

  // ---------------- services ----------------

  @Get(':project/services')
  @Permissions('app:read', 'app:write')
  @ApiOperation({ summary: 'List services in a project' })
  listServices(@Param('project') project: string) {
    return this.projects.listServices(project);
  }

  @Post(':project/services')
  @Permissions('app:write')
  @ApiOperation({
    summary: 'Add a service to a project (auto-creates production env)',
  })
  addService(@Param('project') project: string, @Body() dto: CreateServiceDTO) {
    return this.projects.addService(project, dto);
  }

  @Get(':project/services/:service')
  @Permissions('app:read', 'app:write')
  @ApiOperation({ summary: 'Get one service' })
  getService(
    @Param('project') project: string,
    @Param('service') service: string,
  ) {
    return this.projects.getService(project, service);
  }

  @Delete(':project/services/:service')
  @Permissions('app:write')
  @ApiOperation({ summary: 'Delete a service (cascade to its environments)' })
  removeService(
    @Param('project') project: string,
    @Param('service') service: string,
  ) {
    return this.projects.deleteService(project, service);
  }

  // ---------------- builds ----------------

  @Get(':project/services/:service/builds')
  @Permissions('app:read', 'app:write')
  @ApiOperation({ summary: 'List builds for a service (newest first)' })
  listBuilds(
    @Param('project') project: string,
    @Param('service') service: string,
  ) {
    return this.builds.list(project, service);
  }

  @Post(':project/services/:service/builds')
  @Permissions('app:write')
  @ApiOperation({
    summary: 'Trigger a build for a service. Body: {branch?, ref?}',
  })
  createBuild(
    @Param('project') project: string,
    @Param('service') service: string,
    @Body() body: CreateBuildRequest,
  ) {
    return this.builds.createBuild(project, service, body || {});
  }

  // ---------------- logs ----------------

  @Get(':project/services/:service/logs')
  @Permissions('app:read', 'app:write')
  @ApiOperation({
    summary:
      'Tail recent log lines for a service. ?env=production|preview-pr-N (default production), ?lines=N (default 200, max 2000)',
  })
  tailLogs(
    @Param('project') project: string,
    @Param('service') service: string,
    @Query('env') env?: string,
    @Query('lines') lines?: string,
  ) {
    const n = Math.min(2000, Math.max(1, Number(lines) || 200));
    return this.logs.tail(project, service, env || 'production', n);
  }

  // ---------------- env vars ----------------

  @Get(':project/services/:service/env')
  @Permissions('app:read', 'app:write')
  @ApiOperation({
    summary:
      'List a service env: plain key=value pairs, plus the keys of any secret-typed entries (values redacted)',
  })
  getServiceEnv(
    @Param('project') project: string,
    @Param('service') service: string,
  ) {
    return this.projects.getEnv(project, service);
  }

  @Post(':project/services/:service/env')
  @Permissions('app:write')
  @ApiOperation({
    summary:
      'Replace the service env list. Body: { envVars: [{name, value} | {name, valueFrom: {secretKeyRef: {name, key}}}] }',
  })
  setEnv(
    @Param('project') project: string,
    @Param('service') service: string,
    @Body() body: { envVars?: any[] },
  ) {
    return this.projects.setEnv(project, service, body?.envVars || []);
  }

  // ---------------- secrets ----------------

  @Get(':project/services/:service/secrets')
  @Permissions('app:read', 'app:write')
  @ApiOperation({
    summary: 'List secret keys for a service (values are never returned)',
  })
  listSecrets(
    @Param('project') project: string,
    @Param('service') service: string,
  ) {
    return this.projects
      .listSecretKeys(project, service)
      .then((keys) => ({ keys }));
  }

  @Post(':project/services/:service/secrets')
  @Permissions('app:write')
  @ApiOperation({
    summary: 'Set or replace a secret key. Body: { key, value }',
  })
  setSecret(
    @Param('project') project: string,
    @Param('service') service: string,
    @Body() body: { key?: string; value?: string },
  ) {
    return this.projects
      .setSecret(
        project,
        service,
        String(body?.key || ''),
        String(body?.value ?? ''),
      )
      .then(() => ({ ok: true }));
  }

  @Delete(':project/services/:service/secrets/:key')
  @Permissions('app:write')
  @ApiOperation({ summary: 'Unset a secret key' })
  unsetSecret(
    @Param('project') project: string,
    @Param('service') service: string,
    @Param('key') key: string,
  ) {
    return this.projects
      .unsetSecret(project, service, key)
      .then(() => ({ ok: true }));
  }

  // ---------------- environments ----------------

  @Get(':project/envs')
  @Permissions('app:read', 'app:write')
  @ApiOperation({ summary: 'List environments in a project' })
  listEnvs(@Param('project') project: string) {
    return this.projects.listEnvironments(project);
  }

  @Get(':project/envs/:env')
  @Permissions('app:read', 'app:write')
  @ApiOperation({ summary: 'Get one environment' })
  getEnv(@Param('project') project: string, @Param('env') env: string) {
    return this.projects.getEnvironment(project, env);
  }

  @Delete(':project/envs/:env')
  @Permissions('app:write')
  @ApiOperation({
    summary: 'Delete a preview environment (production cannot be deleted)',
  })
  removeEnv(@Param('project') project: string, @Param('env') env: string) {
    return this.projects.deleteEnvironment(project, env);
  }

  // ---------------- addons ----------------

  @Get(':project/addons')
  @Permissions('app:read', 'app:write')
  @ApiOperation({ summary: 'List addons in a project' })
  listAddons(@Param('project') project: string) {
    return this.projects.listAddons(project);
  }

  @Post(':project/addons')
  @Permissions('app:write')
  @ApiOperation({
    summary:
      'Add an addon to a project (refreshes envFromSecrets on every env)',
  })
  addAddon(@Param('project') project: string, @Body() dto: CreateAddonDTO) {
    return this.projects.addAddon(project, dto);
  }

  @Delete(':project/addons/:addon')
  @Permissions('app:write')
  @ApiOperation({ summary: 'Delete an addon' })
  removeAddon(
    @Param('project') project: string,
    @Param('addon') addon: string,
  ) {
    return this.projects.deleteAddon(project, addon);
  }
}
