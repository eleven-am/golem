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

class ReturnTypes {
  @ComputedField('User', { type: 'String!', requires: ['email'] })
  ok(): string {
    return '';
  }

  @ComputedField('User', { type: 'String!', requires: ['email'] })
  okAsync(): Promise<string> {
    return Promise.resolve('');
  }

  @ComputedField('User', { type: 'Int!' })
  okInt(): number {
    return 1;
  }

  @ComputedField('User', { type: '[String!]!' })
  okList(): string[] {
    return [];
  }

  @ComputedField('User', { type: 'Post!' })
  okObject(): { id: string } {
    return { id: '' };
  }

  // @ts-expect-error String! cannot be satisfied by a number
  @ComputedField('User', { type: 'String!' })
  wrongScalar(): number {
    return 1;
  }

  // @ts-expect-error a non-null field cannot return null
  @ComputedField('User', { type: 'String!' })
  wrongNullability(): string | null {
    return null;
  }
}

void ReturnTypes;
