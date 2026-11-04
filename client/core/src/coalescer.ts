export const COALESCE_WINDOW_MS = 50
export const COALESCE_MAX_WAIT_MS = 500

export class Coalescer {
  private readonly fire: () => void
  private readonly window: number
  private readonly maxWait: number
  private timer: ReturnType<typeof setTimeout> | undefined
  private firstRequestAt: number | undefined

  constructor(fire: () => void, window = COALESCE_WINDOW_MS, maxWait = COALESCE_MAX_WAIT_MS) {
    this.fire = fire
    this.window = window
    this.maxWait = maxWait
  }

  get pending(): boolean {
    return this.timer !== undefined
  }

  request(): void {
    const now = Date.now()
    this.firstRequestAt ??= now
    if (this.timer !== undefined) {
      clearTimeout(this.timer)
    }
    const remaining = this.firstRequestAt + this.maxWait - now
    this.timer = setTimeout(() => this.flush(), Math.max(0, Math.min(this.window, remaining)))
  }

  flush(): void {
    this.cancel()
    this.fire()
  }

  cancel(): void {
    if (this.timer !== undefined) {
      clearTimeout(this.timer)
    }
    this.timer = undefined
    this.firstRequestAt = undefined
  }
}
