import { Injectable } from '@nestjs/common';
import type { JobScope } from './job.types';

interface ActiveJob {
  readonly scope: JobScope | null;
  readonly controller: AbortController;
}

function sameScope(a: JobScope | null, b: JobScope): boolean {
  return a !== null && a.type === b.type && a.id === b.id;
}

@Injectable()
export class JobCancellationRegistry {
  private readonly activeJobs = new Map<string, ActiveJob>();
  private readonly completionWaiters = new Map<string, Set<() => void>>();

  register(
    jobId: string,
    scope: JobScope | null,
    controller: AbortController,
  ): void {
    this.activeJobs.set(jobId, { scope, controller });
  }

  unregister(jobId: string): void {
    this.activeJobs.delete(jobId);
    const waiters = this.completionWaiters.get(jobId);
    this.completionWaiters.delete(jobId);
    for (const resolve of waiters ?? []) resolve();
  }

  abortForScope(scope: JobScope, reason: string): void {
    for (const job of this.activeJobs.values()) {
      if (sameScope(job.scope, scope)) {
        job.controller.abort(new Error(reason));
      }
    }
  }

  abortJobs(jobIds: readonly string[], reason: string): void {
    for (const jobId of jobIds) {
      this.activeJobs.get(jobId)?.controller.abort(new Error(reason));
    }
  }

  abortJobsMissingFrom(activeJobIds: ReadonlySet<string>): void {
    for (const [jobId, job] of this.activeJobs) {
      if (!activeJobIds.has(jobId)) {
        job.controller.abort(new Error(`Job ${jobId} was cancelled`));
      }
    }
  }

  ids(): string[] {
    return [...this.activeJobs.keys()];
  }

  waitForJobs(jobIds: readonly string[]): Promise<void> {
    return Promise.all(
      jobIds.map(
        (jobId) =>
          new Promise<void>((resolve) => {
            if (!this.activeJobs.has(jobId)) {
              resolve();
              return;
            }
            const waiters = this.completionWaiters.get(jobId);
            if (waiters) {
              waiters.add(resolve);
            } else {
              this.completionWaiters.set(jobId, new Set([resolve]));
            }
          }),
      ),
    ).then(() => undefined);
  }
}
