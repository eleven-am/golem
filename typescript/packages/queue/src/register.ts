declare global {
  interface GolemRegister {}
}

export type RegisteredJobs = GolemRegister extends { jobs: infer TJobs extends object }
  ? TJobs
  : Record<string, Record<string, unknown>>;

export type JobType = keyof RegisteredJobs & string;

export type JobPayload<TType extends JobType> = RegisteredJobs[TType] extends infer TPayload
  ? TPayload extends Record<string, unknown>
    ? TPayload
    : Record<string, unknown>
  : Record<string, unknown>;
