module.exports = {
  testEnvironment: 'node',
  testMatch: ['<rootDir>/test/**/*.e2e.test.ts'],
  transform: {
    '^.+\\.ts$': ['ts-jest', { tsconfig: '<rootDir>/tsconfig.spec.json' }],
  },
};
