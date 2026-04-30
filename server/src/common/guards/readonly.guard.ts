import {
  CanActivate,
  ExecutionContext,
  Injectable,
  HttpException,
  Logger,
} from '@nestjs/common';

/* DEPRECATED: This guard is deprecated and will be removed in future versions in favour of Kuso roles*/
@Injectable()
export class ReadonlyGuard implements CanActivate {
  private logger = new Logger(ReadonlyGuard.name);
  canActivate(context: ExecutionContext): boolean {
    if (process.env.KUSO_READONLY === 'true') {
      this.logger.warn(
        'Kuso is in read-only mode, write operations are blocked',
      );
      this.logger.warn(
        'KUSO_READONLY is deprecated! Use Kuso\'s RBAC feature instead.',
      );
      throw new HttpException('Kuso is in read-only mode', 202);
    }
    return true;
  }
}
