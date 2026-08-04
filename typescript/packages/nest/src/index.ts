import {
  DynamicModule,
  FactoryProvider,
  Logger,
  MiddlewareConsumer,
  Module,
  ModuleMetadata,
  NestModule,
  OnModuleDestroy,
  OnModuleInit,
  Type,
} from '@nestjs/common';
import { DiscoveryModule, DiscoveryService } from '@nestjs/core';
import {
  AuthorizationProvider,
  GolemDefaults,
  GolemBatchEventOptions,
  GolemEngine,
  GolemEngineRef,
  GolemQueryInterceptor,
  GolemSubscriptionOptions,
  DatamodelDocument,
  HookRegistry,
  ModelsConfig,
  buildGolemSchema,
  createGolemEngine,
  createEventPublisher,
  subscribableModels,
  validateUpsertGuardInfrastructure,
} from '@eleven-am/golem-core';
import { PubSub, PubSubEngine } from 'graphql-subscriptions';
import type { GraphQLSchema } from 'graphql';
import { PubSubEventBus } from './event-bus';
import { extractExtensionSpecs } from './extensions';
import type { ExtractedExtensions } from './extensions';
import { createGolemGraphQLArtifacts } from './graphql-artifacts';
import type { GolemGraphQLArtifacts } from './graphql-artifacts';
import { GOLEM_MODEL_NAMES, GolemHooksExplorer } from './hooks-explorer';
import { golemRequestBoundary } from './request-boundary';

export { PubSubEventBus } from './event-bus';
export { golemSharedContext } from './request-boundary';
export type { GolemGraphQLArtifacts } from './graphql-artifacts';
export * from './decorators';
export * from './register';
export * from '@eleven-am/golem-core';

export const GOLEM_GRAPHQL = 'GOLEM_GRAPHQL';
export const GOLEM_EVENT_BUS = 'GOLEM_EVENT_BUS';
export const GOLEM_ENGINE = 'GOLEM_ENGINE';
const GOLEM_CLIENT_LIFECYCLE = 'GOLEM_CLIENT_LIFECYCLE';
const GOLEM_CLIENT_OPTIONS = 'GOLEM_CLIENT_OPTIONS';
const GOLEM_EXTENSION_SPECS = 'GOLEM_EXTENSION_SPECS';
const GOLEM_INTERNAL_SCHEMA = 'GOLEM_INTERNAL_SCHEMA';

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
  subscription?: GolemSubscriptionOptions;
  batchEvents?: GolemBatchEventOptions;
}

export interface GolemModuleAsyncOptions<TModels>
  extends Omit<GolemModuleOptions<TModels>, 'prismaOptions'> {
  inject?: FactoryProvider['inject'];
  useFactory: (...dependencies: any[]) => unknown | Promise<unknown>;
}

interface ConnectableClient {
  $connect(): Promise<void>;
  $disconnect(): Promise<void>;
}

class GolemClientLifecycle implements OnModuleInit, OnModuleDestroy {
  constructor(
    private readonly client: ConnectableClient,
    private readonly validateUpsertGuard: boolean,
  ) {}

  async onModuleInit(): Promise<void> {
    await this.client.$connect();
    if (this.validateUpsertGuard) {
      await validateUpsertGuardInfrastructure(
        this.client as unknown as Record<string, unknown>,
      );
    }
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
export class GolemModule implements NestModule {
  configure(consumer: MiddlewareConsumer): void {
    consumer.apply(golemRequestBoundary).forRoutes('*');
  }

  static forRoot<TModels>(options: GolemModuleOptions<TModels>): DynamicModule {
    return this.createDynamicModule(options, {
      provide: GOLEM_CLIENT_OPTIONS,
      useValue: options.prismaOptions,
    });
  }

  static forRootAsync<TModels>(options: GolemModuleAsyncOptions<TModels>): DynamicModule {
    return this.createDynamicModule(options, {
      provide: GOLEM_CLIENT_OPTIONS,
      inject: options.inject ?? [],
      useFactory: options.useFactory,
    });
  }

  private static createDynamicModule<TModels>(
    options: Omit<GolemModuleOptions<TModels>, 'prismaOptions'>,
    clientOptionsProvider: FactoryProvider | { provide: string; useValue: unknown },
  ): DynamicModule {
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
    const authorizationProviders =
      typeof options.authorization === 'function' ? [options.authorization] : [];
    const authorizationInject = options.authorization ? [options.authorization] : [];
    const engineRef: GolemEngineRef = {};

    return {
      module: GolemModule,
      global: true,
      imports: [DiscoveryModule, ...(options.imports ?? [])],
      providers: [
        clientOptionsProvider,
        eventBusProvider,
        HookRegistry,
        {
          provide: GOLEM_MODEL_NAMES,
          useValue: new Set(options.datamodel.models.map((model) => model.name)),
        },
        GolemHooksExplorer,
        ...extensions,
        ...authorizationProviders,
        {
          provide: options.client,
          useFactory: (eventBus: PubSubEventBus, clientOptions: unknown) => {
            const publisher = createEventPublisher({
              datamodel: options.datamodel,
              eventBus,
              models: subscribableModels(options),
              batch: options.batchEvents,
            });
            return new (options.client as unknown as GeneratedGolemClient)(
              clientOptions,
              publisher,
              engineRef,
            );
          },
          inject: [GOLEM_EVENT_BUS, GOLEM_CLIENT_OPTIONS],
        },
        {
          provide: GOLEM_CLIENT_LIFECYCLE,
          useFactory: (client: ConnectableClient) =>
            new GolemClientLifecycle(client, options.authorization !== undefined),
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
          provide: GOLEM_EXTENSION_SPECS,
          useFactory: (discovery: DiscoveryService) => {
            const importedTypes = importedExtensions.map((token) => {
              if (typeof token === 'function') {
                return token;
              }
              const wrapper = discovery.getProviders().find((provider) => provider.token === token);
              const instanceType = wrapper?.instance?.constructor;
              const type = typeof instanceType === 'function' && instanceType !== Object
                ? instanceType
                : wrapper?.metatype;
              if (typeof type !== 'function') {
                throw new Error(`Imported Golem extension ${String(token)} has no discoverable class`);
              }
              return type;
            });
            return extractExtensionSpecs([...extensions, ...importedTypes]);
          },
          inject: [DiscoveryService],
        },
        {
          provide: GOLEM_INTERNAL_SCHEMA,
          useFactory: (
            client: Record<string, any>,
            eventBus: PubSubEventBus,
            hooks: HookRegistry,
            engine: GolemEngine,
            specs: ExtractedExtensions,
            ...rest: unknown[]
          ) => {
            const authorization = options.authorization
              ? (rest[0] as AuthorizationProvider)
              : undefined;
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
              subscription: options.subscription,
            });
          },
          inject: [
            options.client,
            GOLEM_EVENT_BUS,
            HookRegistry,
            GOLEM_ENGINE,
            GOLEM_EXTENSION_SPECS,
            ...authorizationInject,
          ],
        },
        {
          provide: GOLEM_GRAPHQL,
          useFactory: (
            schema: GraphQLSchema,
            specs: ExtractedExtensions,
          ): GolemGraphQLArtifacts => createGolemGraphQLArtifacts(
            schema,
            specs.computedFields,
            specs.customOperations,
          ),
          inject: [GOLEM_INTERNAL_SCHEMA, GOLEM_EXTENSION_SPECS],
        },
      ],
      exports: [GOLEM_GRAPHQL, GOLEM_EVENT_BUS, GOLEM_ENGINE, HookRegistry, options.client],
    };
  }
}
