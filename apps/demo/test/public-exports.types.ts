import {
  BigIntScalar,
  DecimalScalar,
  GolemEngine,
  type FieldDependencyTree,
  type GolemEngineRef,
} from '@eleven-am/golem-core';
import {
  CustomQuery,
  GOLEM_GRAPHQL,
  GolemModule,
  type GolemGraphQLArtifacts,
} from '@eleven-am/golem';
import { GolemAuthorizationAdapter } from '@eleven-am/golem-authorizer';
import { emitDatamodelModule } from '@eleven-am/golem-generator';
import {
  GolemQueueModule,
  InMemoryJobStore,
  JobQueue,
  QueuePayloadError,
  type JobStore,
} from '@eleven-am/golem-queue';

const dependencies: FieldDependencyTree = {
  organization: { company: { blocked: true } },
};
const engineRef: GolemEngineRef = {};
const store: JobStore = new InMemoryJobStore();

void [
  BigIntScalar,
  DecimalScalar,
  GolemEngine,
  CustomQuery,
  GOLEM_GRAPHQL,
  GolemModule,
  GolemAuthorizationAdapter,
  emitDatamodelModule,
  GolemQueueModule,
  JobQueue,
  QueuePayloadError,
  dependencies,
  engineRef,
  store,
];

type _GraphQLArtifactsContract = GolemGraphQLArtifacts;
