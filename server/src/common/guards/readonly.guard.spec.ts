import { ReadonlyGuard } from './readonly.guard';
import { ExecutionContext, HttpException } from '@nestjs/common';

describe('ReadonlyGuard', () => {
  let guard: ReadonlyGuard;
  let mockContext: ExecutionContext;

  beforeEach(() => {
    guard = new ReadonlyGuard();
    mockContext = {} as ExecutionContext;
  });

  afterEach(() => {
    delete process.env.KUSO_READONLY;
  });

  it('should allow access when KUSO_READONLY is not "true"', () => {
    process.env.KUSO_READONLY = 'false';
    expect(guard.canActivate(mockContext)).toBe(true);
  });

  it('should throw HttpException when KUSO_READONLY is "true"', () => {
    process.env.KUSO_READONLY = 'true';
    expect(() => guard.canActivate(mockContext)).toThrow(HttpException);
    try {
      guard.canActivate(mockContext);
    } catch (e) {
      expect(e.message).toBe('Kuso is in read-only mode');
      expect(e.getStatus()).toBe(202);
    }
  });
});
