import { Injectable, Logger } from '@nestjs/common';
import {
  AfterCreate,
  BeforeCreate,
  BeforeDelete,
  GolemHooks,
  GolemRequest,
  GolemResult,
  GolemValidationError,
} from '@eleven-am/golem';

@GolemHooks('User')
@Injectable()
export class UserHooks {
  private readonly logger = new Logger(UserHooks.name);

  @BeforeCreate()
  normalizeEmail(request: GolemRequest<'User', 'create'>): GolemRequest<'User', 'create'> {
    return {
      ...request,
      data: {
        ...request.data,
        email: request.data.email.toLowerCase(),
        apiKey: `key_${request.data.email.toLowerCase()}`,
      },
    };
  }

  @AfterCreate()
  logCreation(result: GolemResult<'User', 'create'>) {
    this.logger.log(`User created: ${result.id}`);
  }

  @BeforeDelete()
  protectSeedUser(request: GolemRequest<'User', 'delete'>) {
    if (request.where.email === 'roy@example.com') {
      throw new GolemValidationError('The seed user cannot be deleted');
    }
  }
}
