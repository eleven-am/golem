module.exports = {
  testEnvironment: 'node',
  testMatch: ['<rootDir>/test/**/*.e2e.test.ts'],
  transform: {
    '^.+\\.ts$': [
      'ts-jest',
      {
        tsconfig: '<rootDir>/tsconfig.spec.json',
        diagnostics: { ignoreCodes: [151002] },
      },
    ],
    'node_modules[/\\\\]kysely[/\\\\].+\\.js$': [
      'ts-jest',
      {
        tsconfig: { isolatedModules: true, allowJs: true, module: 'commonjs', target: 'es2022' },
      },
    ],
  },
  transformIgnorePatterns: ['/node_modules/(?!kysely/)'],
};
