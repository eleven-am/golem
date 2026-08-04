import type { JobEvent, JobQueue, JobWork } from '@eleven-am/golem-queue';
import { QueueHandler } from '@eleven-am/golem-queue';
import '../src/generated/golem';

declare global {
  interface GolemRegister {
    jobs: {
      'article-extract': { articleId: string };
      'article-summarize': { articleId: string; model: string };
    };
  }
}

declare const queue: JobQueue;

async function enqueues(): Promise<void> {
  await queue.add('article-extract', { articleId: 'a1' });
  await queue.add('article-summarize', { articleId: 'a1', model: 'fast' });

  // @ts-expect-error the registered jobs reject an unknown job type
  await queue.add('artcile-extract', { articleId: 'a1' });

  // @ts-expect-error the registered jobs reject a payload the handler does not expect
  await queue.add('article-extract', { id: 'a1' });

  // @ts-expect-error the registered jobs reject a missing payload field
  await queue.add('article-summarize', { articleId: 'a1' });
}

@QueueHandler({ type: 'article-extract' })
class ValidHandler {}

// @ts-expect-error the registered jobs reject an unknown job type on a handler
@QueueHandler({ type: 'artcile-extract' })
class InvalidHandler {}

void enqueues;
void ValidHandler;
void InvalidHandler;

@QueueHandler({ type: 'article-extract' })
class TypedHandler implements JobWork<'article-extract'> {
  async handle({ payload, attempt, maxAttempts, signal }: JobEvent<'article-extract'>): Promise<void> {
    const id: string = payload.articleId;
    const remaining: number = maxAttempts - attempt;
    void id;
    void remaining;
    void signal;
  }
}

class MismatchedPayload implements JobWork<'article-extract'> {
  // @ts-expect-error handle must accept the payload registered for this job type
  async handle({ payload }: JobEvent<'article-summarize'>): Promise<void> {
    void payload;
  }
}

void TypedHandler;
void MismatchedPayload;
