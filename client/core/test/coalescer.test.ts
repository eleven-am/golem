import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { Coalescer } from '../src/index.js'

beforeEach(() => {
  vi.useFakeTimers()
})

afterEach(() => {
  vi.useRealTimers()
})

describe('Coalescer', () => {
  it('fires once, 50 ms after a single request', () => {
    const fire = vi.fn()
    const coalescer = new Coalescer(fire)
    coalescer.request()
    vi.advanceTimersByTime(49)
    expect(fire).not.toHaveBeenCalled()
    vi.advanceTimersByTime(1)
    expect(fire).toHaveBeenCalledTimes(1)
    expect(coalescer.pending).toBe(false)
  })

  it('keeps pushing the trailing edge while requests keep arriving', () => {
    const fire = vi.fn()
    const coalescer = new Coalescer(fire)
    for (let elapsed = 0; elapsed < 300; elapsed += 30) {
      coalescer.request()
      vi.advanceTimersByTime(30)
    }
    expect(fire).not.toHaveBeenCalled()
    vi.advanceTimersByTime(50)
    expect(fire).toHaveBeenCalledTimes(1)
  })

  it('never waits past 500 ms from the first request', () => {
    const fire = vi.fn()
    const coalescer = new Coalescer(fire)
    for (let elapsed = 0; elapsed < 490; elapsed += 10) {
      coalescer.request()
      vi.advanceTimersByTime(10)
    }
    expect(fire).not.toHaveBeenCalled()
    coalescer.request()
    vi.advanceTimersByTime(10)
    expect(fire).toHaveBeenCalledTimes(1)
  })

  it('bounds a two second storm to the number of maximum waits', () => {
    const fire = vi.fn()
    const coalescer = new Coalescer(fire)
    for (let elapsed = 0; elapsed < 2000; elapsed += 5) {
      coalescer.request()
      vi.advanceTimersByTime(5)
    }
    vi.advanceTimersByTime(100)
    expect(fire.mock.calls.length).toBeLessThanOrEqual(5)
    expect(fire.mock.calls.length).toBeGreaterThanOrEqual(4)
  })

  it('flush fires at once and cancel drops the request', () => {
    const fire = vi.fn()
    const coalescer = new Coalescer(fire)
    coalescer.request()
    coalescer.flush()
    expect(fire).toHaveBeenCalledTimes(1)
    coalescer.request()
    coalescer.cancel()
    vi.advanceTimersByTime(1000)
    expect(fire).toHaveBeenCalledTimes(1)
  })
})
