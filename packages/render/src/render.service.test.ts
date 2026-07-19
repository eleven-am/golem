import { resolve } from 'node:path';
import { GolemRenderService } from './render.service';

describe('GolemRenderService startup validation', () => {
  const adapterHost = {
    httpAdapter: {
      getType: () => 'express',
      getInstance: () => ({ use: jest.fn() }),
    },
  } as never;

  it('fails with the resolved absolute index path when the bundle is missing', () => {
    const client = './packages/render/test/does-not-exist';
    expect(() => new GolemRenderService({ client }, adapterHost)).toThrow(
      `Frontend bundle is missing index file at ${resolve(client, 'index.html')}`,
    );
  });

  it('rejects an index path that escapes the configured client directory', () => {
    const client = resolve(__dirname, '../test/fixture');
    expect(() => new GolemRenderService({ client, index: '../index.html' }, adapterHost))
      .toThrow(`Frontend index must be a file inside ${client}`);
  });

  it('fails loudly on unsupported Nest HTTP adapters', () => {
    const client = resolve(__dirname, '../test/fixture');
    const service = new GolemRenderService({ client }, {
      httpAdapter: { getType: () => 'fastify' },
    } as never);

    expect(() => service.onModuleInit()).toThrow(
      '@eleven-am/golem-render currently requires the Nest Express adapter',
    );
  });

  it('supports a configured index filename', () => {
    const client = resolve(__dirname, '../test/fixture');
    const service = new GolemRenderService({ client, index: 'shell.html' }, adapterHost);

    expect(service.indexPath).toBe(resolve(client, 'shell.html'));
    expect(service.render(undefined, '/')).toContain('<title>Alternate shell</title>');
  });

  it('rejects an invalid base URL during startup', () => {
    const client = resolve(__dirname, '../test/fixture');
    expect(() => new GolemRenderService({ client, baseUrl: 'not a URL' }, adapterHost))
      .toThrow('Invalid Golem render baseUrl: not a URL');
  });
});
