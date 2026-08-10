import { Injectable, Logger } from '@nestjs/common';
import {
  errorMessage,
  type JobLifecycleObserver,
  type JobLifecycleTransition,
} from './job.types';

@Injectable()
export class JobObserverRegistry {
  private readonly logger = new Logger(JobObserverRegistry.name);
  private readonly observers: JobLifecycleObserver[] = [];

  register(observer: JobLifecycleObserver): void {
    if (!this.observers.includes(observer)) {
      this.observers.push(observer);
    }
  }

  count(): number {
    return this.observers.length;
  }

  async notify(transition: JobLifecycleTransition): Promise<void> {
    for (const observer of this.observers) {
      try {
        await observer.onTransition(transition);
      } catch (error) {
        this.logger.error(
          `Job lifecycle observer failed for ${transition.jobId}: ${errorMessage(error)}`,
        );
      }
    }
  }
}
