import 'reflect-metadata';
import { Injectable } from '@nestjs/common';
import { Test } from '@nestjs/testing';
import type { DatamodelDocument } from '@eleven-am/golem-core';
import { BeforeCreate, ComputedField, GolemHooks } from './decorators';
import { GolemModule } from './index';

const datamodel: DatamodelDocument<{ User: 'id'; Post: 'id' }> = {
  models: [
    {
      name: 'User',
      fields: [{
        name: 'id',
        kind: 'scalar',
        type: 'String',
        isList: false,
        isRequired: true,
        isUnique: true,
        isId: true,
        hasDefaultValue: true,
        isReadOnly: false,
        isUpdatedAt: false,
      }],
    },
    {
      name: 'Post',
      fields: [{
        name: 'id',
        kind: 'scalar',
        type: 'String',
        isList: false,
        isRequired: true,
        isUnique: true,
        isId: true,
        hasDefaultValue: true,
        isReadOnly: false,
        isUpdatedAt: false,
      }],
    },
  ],
  enums: [],
};

class FakeClient {
  readonly user = {};
  readonly post = {};

  async $connect(): Promise<void> {}
  async $disconnect(): Promise<void> {}
}

describe('GolemModule extension validation', () => {
  it('fails during module compilation when one extension targets multiple models', async () => {
    @Injectable()
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

    await expect(Test.createTestingModule({
      imports: [GolemModule.forRoot({
        client: FakeClient,
        datamodel,
        extensions: [InvalidExtension],
      })],
    }).compile()).rejects.toThrow(
      'Extension InvalidExtension declares computed fields for multiple models: Post, User',
    );
  });

  it('refuses to boot when hooks target a model that does not exist', async () => {
    @GolemHooks('Ghost' as 'User')
    @Injectable()
    class GhostHooks {
      @BeforeCreate()
      normalize(request: unknown): unknown {
        return request;
      }
    }

    const moduleRef = await Test.createTestingModule({
      imports: [GolemModule.forRoot({
        client: FakeClient,
        datamodel,
        extensions: [GhostHooks],
      })],
    }).compile();

    await expect(moduleRef.init()).rejects.toThrow(
      'GhostHooks declares hooks for unknown model Ghost; known models are Post, User',
    );
  });

  it('registers hooks for a model that exists', async () => {
    @GolemHooks('User')
    @Injectable()
    class UserHooks {
      @BeforeCreate()
      normalize(request: unknown): unknown {
        return request;
      }
    }

    const moduleRef = await Test.createTestingModule({
      imports: [GolemModule.forRoot({
        client: FakeClient,
        datamodel,
        extensions: [UserHooks],
      })],
    }).compile();

    await expect(moduleRef.init()).resolves.toBeDefined();
    await moduleRef.close();
  });
});
