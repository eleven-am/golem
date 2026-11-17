import { defineConfig } from 'vitest/config'

const generatedFixture = process.env.GOLEM_GENERATED_CLIENT_FIXTURE
if (generatedFixture !== undefined && generatedFixture !== 'continuation' && generatedFixture !== 'invalidation' && generatedFixture !== 'restore' && generatedFixture !== 'mixed') {
  throw new Error('unknown generated client fixture')
}

export default defineConfig({
  test: {
    include: generatedFixture ? [`test/${generatedFixture}.http.test.ts`] : ['test/**/*.test.ts'],
    globalSetup: generatedFixture ? [] : ['test/harness/global-setup.ts'],
    testTimeout: 30_000,
    hookTimeout: 600_000,
    fileParallelism: false,
    ...(generatedFixture ? { maxWorkers: 1, minWorkers: 1 } : {}),
    teardownTimeout: 40_000,
  },
})
