#!/usr/bin/env node
import { generatorHandler } from '@prisma/generator-helper';
import { mkdir, writeFile } from 'fs/promises';
import { join } from 'path';
import { emitDatamodelModule } from './emit';
import { emitClientModule } from './emit-client';
import { emitTypesModule } from './emit-types';

generatorHandler({
  onManifest: () => ({
    prettyName: 'golem',
    defaultOutput: './generated/golem',
  }),
  onGenerate: async (options) => {
    const output = options.generator.output?.value;
    if (!output) {
      throw new Error('The golem generator requires an output path');
    }
    const clientImport = (options.generator.config.clientImport as string | undefined) ?? '../prisma/client';
    await mkdir(output, { recursive: true });
    await writeFile(
      join(output, 'index.ts'),
      emitDatamodelModule(options.dmmf.datamodel, options.datasources[0]?.provider),
    );
    await writeFile(
      join(output, 'client.ts'),
      emitClientModule(options.dmmf.datamodel.models.map((m) => m.name), clientImport),
    );
    await writeFile(
      join(output, 'types.ts'),
      emitTypesModule(options.dmmf.datamodel.models.map((m) => m.name), clientImport),
    );
  },
});
