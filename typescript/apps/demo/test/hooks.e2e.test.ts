import { INestApplication } from '@nestjs/common';
import request from 'supertest';
import { GolemPrismaService } from '../src/generated/golem/client';
import { bootDemoApp, shutdownDemoApp } from './harness';

describe('golem hooks and model config (e2e)', () => {
  let app: INestApplication;
  let prisma: GolemPrismaService;

  beforeAll(async () => {
    const context = await bootDemoApp(__filename);
    app = context.app;
    prisma = context.prisma;
  });

  afterAll(async () => {
    await shutdownDemoApp(app, __filename);
  });

  function gql(query: string) {
    return request(app.getHttpServer()).post('/graphql').set('authorization', 'token-roy@example.com').send({ query });
  }

  it('before hooks transform mutation data over the wire', async () => {
    const response = await gql(
      'mutation { createUser(data: { email: "MiXeD@Example.COM" }) { email } }',
    );
    expect(response.body.errors).toBeUndefined();
    expect(response.body.data.createUser.email).toBe('mixed@example.com');
    const stored = await prisma.user.findUnique({ where: { email: 'mixed@example.com' } });
    expect(stored?.apiKey).toBe('key_mixed@example.com');
  });

  it('before hooks abort operations with their error code', async () => {
    const response = await gql(
      'mutation { deleteUser(where: { email: "roy@example.com" }) { id } }',
    );
    expect(response.body.errors[0].message).toBe('The seed user cannot be deleted');
    expect(response.body.errors[0].extensions.code).toBe('BAD_USER_INPUT');
    const stillThere = await prisma.user.findUnique({ where: { email: 'roy@example.com' } });
    expect(stillThere).not.toBeNull();
  });

  it('hidden fields are unqueryable and unwritable', async () => {
    const query = await gql('{ users { apiKey } }');
    expect(query.body.errors[0].message).toContain('Cannot query field "apiKey"');

    const write = await gql(
      'mutation { createUser(data: { email: "x@y.z", apiKey: "forged" }) { id } }',
    );
    const messages = write.body.errors.map((e: { message: string }) => e.message).join('\n');
    expect(messages).toContain('"apiKey" is not defined by type "UserCreateInput"');
  });

  it('immutable fields are rejected in update inputs', async () => {
    const response = await gql(
      'mutation { updateUser(where: { email: "roy@example.com" }, data: { email: "new@example.com" }) { id } }',
    );
    const messages = response.body.errors.map((e: { message: string }) => e.message).join('\n');
    expect(messages).toContain('"email" is not defined by type "UserUpdateInput"');
  });

  it('disabled operations do not exist in the schema', async () => {
    const response = await gql(
      'mutation { deleteProfile(where: { id: "x" }) { id } }',
    );
    const messages = response.body.errors.map((e: { message: string }) => e.message).join('\n');
    expect(messages).toContain("Cannot query field \"deleteProfile\"");
  });

  it('rejects take values above maxTake with BAD_USER_INPUT', async () => {
    const response = await gql('{ users(take: 500) { id } }');
    expect(response.body.errors[0].extensions.code).toBe('BAD_USER_INPUT');
    expect(response.body.errors[0].message).toContain('exceeds the maximum of 100');
  });
});
