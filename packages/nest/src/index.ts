import {
  DynamicModule,
  Logger,
  Module,
  ModuleMetadata,
  OnModuleDestroy,
  OnModuleInit,
  Type,
} from '@nestjs/common';
import { DiscoveryModule } from '@nestjs/core';
import {
  AuthorizationProvider,
  GolemDefaults,
  GolemEngine,
  GolemEngineRef,
  GolemQueryInterceptor,
  DatamodelDocument,
  HookRegistry,
  ModelsConfig,
  buildGolemSchema,
  createGolemEngine,
  createEventPublisher,
  subscribableModels,
} from '@eleven-am/golem-core';
import { PubSub, PubSubEngine } from 'graphql-subscriptions';
import { PubSubEventBus } from './event-bus';
import { extractExtensionSpecs } from './extensions';
import { GolemHooksExplorer } from './hooks-explorer';

export { PubSubEventBus } from './event-bus';
export * from './decorators';
export * from '@eleven-am/golem-core';

export const GOLEM_SCHEMA = 'GOLEM_SCHEMA';
export const GOLEM_EVENT_BUS = 'GOLEM_EVENT_BUS';
export const GOLEM_ENGINE = 'GOLEM_ENGINE';
const GOLEM_CLIENT_LIFECYCLE = 'GOLEM_CLIENT_LIFECYCLE';

type GeneratedGolemClient = new (
  options: unknown,
  interceptor: GolemQueryInterceptor,
  engineRef: GolemEngineRef,
) => object;

export interface GolemModuleOptions<TModels> {
  imports?: ModuleMetadata['imports'];
  client: Type<object>;
  prismaOptions?: unknown;
  datamodel: DatamodelDocument<TModels>;
  models?: ModelsConfig<TModels>;
  defaults?: GolemDefaults;
  pubSub?: Type<PubSubEngine> | string | symbol;
  extensions?: Type<object>[];
  importedExtensions?: Array<Type<object> | string | symbol>;
  authorization?: Type<AuthorizationProvider> | string | symbol;
}

interface ConnectableClient {
  $connect(): Promise<void>;
  $disconnect(): Promise<void>;
}

class GolemClientLifecycle implements OnModuleInit, OnModuleDestroy {
  constructor(private readonly client: ConnectableClient) {}

  onModuleInit(): Promise<void> {
    return this.client.$connect();
  }

  onModuleDestroy(): Promise<void> {
    return this.client.$disconnect();
  }
}

function subscriptionsEnabled<TModels>(options: GolemModuleOptions<TModels>): boolean {
  if (options.defaults?.subscriptions === true) {
    return true;
  }
  const configs = Object.values(options.models ?? {}) as Array<false | { subscriptions?: boolean } | undefined>;
  return configs.some(
    (config) => typeof config === 'object' && config !== null && config.subscriptions === true,
  );
}

@Module({})
export class GolemModule {
  static forRoot<TModels>(options: GolemModuleOptions<TModels>): DynamicModule {
    const eventBusProvider = options.pubSub
      ? {
          provide: GOLEM_EVENT_BUS,
          useFactory: (engine: PubSubEngine) => new PubSubEventBus(engine),
          inject: [options.pubSub],
        }
      : {
          provide: GOLEM_EVENT_BUS,
          useFactory: () => {
            if (subscriptionsEnabled(options)) {
              new Logger(GolemModule.name).warn(
                'Subscriptions are using the in-memory PubSub, which only works on a single instance. Provide pubSub for production use.',
              );
            }
            return new PubSubEventBus(new PubSub());
          },
        };

    const extensions = options.extensions ?? [];
    const importedExtensions = options.importedExtensions ?? [];
    const extensionInject = [...extensions, ...importedExtensions];
    const authorizationProviders =
      typeof options.authorization === 'function' ? [options.authorization] : [];
    const authorizationInject = options.authorization ? [options.authorization] : [];
    const engineRef: GolemEngineRef = {};

    return {
      module: GolemModule,
      global: true,
      imports: [DiscoveryModule, ...(options.imports ?? [])],
      providers: [
        eventBusProvider,
        HookRegistry,
        GolemHooksExplorer,
        ...extensions,
        ...authorizationProviders,
        {
          provide: options.client,
          useFactory: (eventBus: PubSubEventBus) => {
            const publisher = createEventPublisher({
              datamodel: options.datamodel,
              eventBus,
              models: subscribableModels(options),
            });
            return new (options.client as unknown as GeneratedGolemClient)(
              options.prismaOptions,
              publisher,
              engineRef,
            );
          },
          inject: [GOLEM_EVENT_BUS],
        },
        {
          provide: GOLEM_CLIENT_LIFECYCLE,
          useFactory: (client: ConnectableClient) => new GolemClientLifecycle(client),
          inject: [options.client],
        },
        {
          provide: GOLEM_ENGINE,
          useFactory: (
            client: Record<string, any>,
            eventBus: PubSubEventBus,
            hooks: HookRegistry,
            ...rest: unknown[]
          ) => {
            const engine = createGolemEngine({
              datamodel: options.datamodel,
              client,
              models: options.models,
              defaults: options.defaults,
              eventBus,
              hooks,
              authorization: options.authorization
                ? (rest[0] as AuthorizationProvider)
                : undefined,
            });
            engineRef.current = engine;
            return engine;
          },
          inject: [options.client, GOLEM_EVENT_BUS, HookRegistry, ...authorizationInject],
        },
        {
          provide: GOLEM_SCHEMA,
          useFactory: (
            client: Record<string, any>,
            eventBus: PubSubEventBus,
            hooks: HookRegistry,
            engine: GolemEngine,
            ...rest: unknown[]
          ) => {
            const authorization = options.authorization
              ? (rest[0] as AuthorizationProvider)
              : undefined;
            const extensionInstances = rest.slice(authorizationInject.length) as object[];
            const specs = extractExtensionSpecs(extensionInstances);
            return buildGolemSchema({
              datamodel: options.datamodel,
              client,
              models: options.models,
              defaults: options.defaults,
              eventBus,
              hooks,
              engine,
              computedFields: specs.computedFields,
              customOperations: specs.customOperations,
              authorization,
            });
          },
          inject: [
            options.client,
            GOLEM_EVENT_BUS,
            HookRegistry,
            GOLEM_ENGINE,
            ...authorizationInject,
            ...extensionInject,
          ],
        },
      ],
      exports: [GOLEM_SCHEMA, GOLEM_EVENT_BUS, GOLEM_ENGINE, HookRegistry, options.client],
    };
  }
}
