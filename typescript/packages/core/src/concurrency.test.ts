import { runPolicyChecks } from './concurrency';

describe('bounded policy concurrency', () => {
  it('caps concurrent checks and reports the earliest input-order failure', async () => {
    let active = 0;
    let maximum = 0;
    const checks = Array.from({ length: 20 }, (_, index) => async () => {
      active += 1;
      maximum = Math.max(maximum, active);
      await new Promise((resolve) => setTimeout(resolve, index === 2 ? 5 : 1));
      active -= 1;
      if (index === 2 || index === 5) throw new Error(`failure-${index}`);
    });

    await expect(runPolicyChecks(checks, 4)).rejects.toThrow('failure-2');
    expect(maximum).toBeLessThanOrEqual(4);
  });
});

