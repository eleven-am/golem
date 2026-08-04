import 'reflect-metadata';
import { Parent } from '@nestjs/graphql';
import { ComputedField, CustomQuery } from './decorators';
import { extractExtensionSpecs } from './extensions';

describe('extension metadata extraction', () => {
  it('discovers inherited computed fields without constructing the provider', () => {
    class BaseExtension {
      @ComputedField('User', { type: 'String!', requires: ['email'] })
      domain(@Parent() parent: { email: string }): string {
        return parent.email;
      }
    }

    class UserExtension extends BaseExtension {
      constructor(_requestScopedDependency: never) {
        super();
        throw new Error('must not be constructed during schema discovery');
      }

      @CustomQuery({ type: 'String!' })
      search(): string {
        return 'result';
      }
    }

    const specs = extractExtensionSpecs([UserExtension]);

    expect(specs.computedFields).toMatchObject([{
      model: 'User',
      name: 'domain',
      type: 'String!',
      requires: ['email'],
    }]);
    expect(specs.customOperations).toMatchObject([{
      kind: 'query',
      name: 'search',
      type: 'String!',
    }]);
  });

  it('does not resurrect an overridden base computed field', () => {
    class BaseExtension {
      @ComputedField('User', { type: 'String!', requires: ['email'] })
      label(): string {
        return 'base';
      }
    }

    class UserExtension extends BaseExtension {
      override label(): string {
        return 'override';
      }
    }

    expect(extractExtensionSpecs([UserExtension]).computedFields).toEqual([]);
  });

  it('fails loudly when one extension class targets multiple models', () => {
    class InvalidExtension {
      @ComputedField('User', { type: 'String!' })
      userLabel(): string {
        return 'user';
      }

      @ComputedField('Post', { type: 'String!' })
      postLabel(): string {
        return 'post';
      }
    }

    expect(() => extractExtensionSpecs([InvalidExtension])).toThrow(
      'Extension InvalidExtension declares computed fields for multiple models: Post, User',
    );
  });
});
