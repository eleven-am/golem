import { AsyncLocalStorage } from 'node:async_hooks';

export interface BufferedEvent {
  publish(): Promise<void>;
}

interface EventBufferStore {
  buffer: BufferedEvent[];
}

const storage = new AsyncLocalStorage<EventBufferStore>();

export function bufferEvent(event: BufferedEvent): boolean {
  const store = storage.getStore();
  if (!store) {
    return false;
  }
  store.buffer.push(event);
  return true;
}

export async function withBufferedEvents<T>(fn: () => Promise<T>): Promise<T> {
  const store: EventBufferStore = { buffer: [] };
  const result = await storage.run(store, fn);
  for (const event of store.buffer) {
    await event.publish();
  }
  return result;
}
