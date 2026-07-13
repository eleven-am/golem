import { Injectable, OnModuleInit } from '@nestjs/common';
import { DiscoveryService, MetadataScanner, Reflector } from '@nestjs/core';
import { HookRegistry } from '@eleven-am/golem-core';
import { GOLEM_HOOK, GOLEM_HOOKS_MODEL, GolemHookMetadata } from './decorators';

@Injectable()
export class GolemHooksExplorer implements OnModuleInit {
  constructor(
    private readonly discovery: DiscoveryService,
    private readonly scanner: MetadataScanner,
    private readonly reflector: Reflector,
    private readonly registry: HookRegistry,
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
