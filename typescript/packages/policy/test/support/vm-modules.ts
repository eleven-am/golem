const FLAG = 'experimental-vm-modules';

export const VM_MODULES_ENABLED =
  process.execArgv.some((argument) => argument.includes(FLAG)) ||
  (process.env.NODE_OPTIONS ?? '').includes(FLAG);

export const VM_MODULES_HINT =
  'the Prisma-backed suites need node --experimental-vm-modules; `npm test -w @eleven-am/golem-policy` supplies it, a bare `npx jest` does not';
