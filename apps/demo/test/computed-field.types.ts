import { ComputedField, GolemHooks, GolemRequest, GolemResult } from '@eleven-am/golem';
import '../src/generated/golem';

class ValidComputedFields {
  @ComputedField('User', { type: 'String!', requires: ['name', 'email'] })
  displayName(): string {
    return '';
  }
}

class InvalidComputedFields {
  // @ts-expect-error the registered schema rejects unknown model names
  @ComputedField('Ghost', { type: 'String!' })
  unknownModel(): string {
    return '';
  }

  // @ts-expect-error the registered schema rejects fields that do not belong to the model
  @ComputedField('User', { type: 'String!', requires: ['title'] })
  unknownRequirement(): string {
    return '';
  }
}

@GolemHooks('User')
class ValidHooks {
  normalize(request: GolemRequest<'User', 'create'>): GolemRequest<'User', 'create'> {
    return request;
  }

  observe(result: GolemResult<'User', 'create'>): void {
    void result;
  }
}

// @ts-expect-error the registered schema rejects unknown model names on hooks
@GolemHooks('Ghost')
class InvalidHooks {}

class InvalidHookPayload {
  // @ts-expect-error the registered schema rejects unknown model names on hook payloads
  normalize(request: GolemRequest<'Ghost', 'create'>): void {
    void request;
  }
}

void ValidComputedFields;
void InvalidComputedFields;
void ValidHooks;
void InvalidHooks;
void InvalidHookPayload;
