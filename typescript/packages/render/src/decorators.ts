import { Get, UseInterceptors } from '@nestjs/common';
import { RenderInterceptor } from './render.interceptor';

const GOLEM_RENDER_ROUTES = 'GOLEM_RENDER_ROUTES';

export function RenderRoute(path: string): MethodDecorator {
  return (target, key, descriptor) => {
    const method = descriptor.value;
    if (typeof method !== 'function') {
      throw new Error('@RenderRoute can only decorate methods');
    }
    const existing = Reflect.getOwnMetadata(GOLEM_RENDER_ROUTES, method) as
      | readonly string[]
      | undefined;
    const paths = [...new Set([...(existing ?? []), path])];
    Reflect.defineMetadata(GOLEM_RENDER_ROUTES, paths, method);
    Get(paths)(target, key, descriptor);
    if (!existing) {
      UseInterceptors(RenderInterceptor)(target, key, descriptor);
    }
  };
}
