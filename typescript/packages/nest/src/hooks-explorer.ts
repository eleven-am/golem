import { Inject, Injectable, OnModuleInit } from '@nestjs/common';
import { DiscoveryService, MetadataScanner, Reflector } from '@nestjs/core';
import { HookRegistry } from '@eleven-am/golem-core';
import { GOLEM_HOOK, GOLEM_HOOKS_MODEL, GolemHookMetadata } from './decorators';

export const GOLEM_MODEL_NAMES = 'GOLEM_MODEL_NAMES';

@Injectable()
export class GolemHooksExplorer implements OnModuleInit {
  constructor(
    private readonly discovery: DiscoveryService,
    private readonly scanner: MetadataScanner,
    private readonly reflector: Reflector,
    private readonly registry: HookRegistry,
    @Inject(GOLEM_MODEL_NAMES) private readonly modelNames: ReadonlySet<string>,
  ) {}

  onModuleInit(): void {
    for (const wrapper of this.discovery.getProviders()) {
      const { instance, metatype } = wrapper;
      if (!instance || !metatype) {
        continue;
      }
      const model = this.reflector.get<string | undefined>(GOLEM_HOOKS_MODEL, metatype);
      if (!model) {
        continue;
      }
      if (!this.modelNames.has(model)) {
        throw new Error(
          `${metatype.name} declares hooks for unknown model ${model}; known models are ${[...this.modelNames].sort().join(', ')}`,
        );
      }
      const prototype = Object.getPrototypeOf(instance);
      for (const methodName of this.scanner.getAllMethodNames(prototype)) {
        const metadata = this.reflector.get<GolemHookMetadata | undefined>(
          GOLEM_HOOK,
          instance[methodName],
        );
        if (!metadata) {
          continue;
        }
        const bound = instance[methodName].bind(instance);
        if (metadata.phase === 'before') {
          this.registry.registerBefore(model, metadata.operation, bound);
        } else {
          this.registry.registerAfter(model, metadata.operation, bound);
        }
      }
    }
  }
}
