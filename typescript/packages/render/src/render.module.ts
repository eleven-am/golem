import { DynamicModule, Global, Module } from '@nestjs/common';
import { RenderInterceptor } from './render.interceptor';
import { GolemRenderService } from './render.service';
import {
  GOLEM_RENDER_OPTIONS,
  type GolemRenderOptions,
} from './types';

@Global()
@Module({})
export class GolemRenderModule {
  static forRoot(options: GolemRenderOptions): DynamicModule {
    return {
      module: GolemRenderModule,
      global: true,
      providers: [
        { provide: GOLEM_RENDER_OPTIONS, useValue: options },
        GolemRenderService,
        RenderInterceptor,
      ],
      exports: [GolemRenderService, RenderInterceptor],
    };
  }
}
