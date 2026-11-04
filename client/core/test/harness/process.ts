import { spawn, type ChildProcess } from 'node:child_process'
import { createServer } from 'node:net'

export function freePort(): Promise<number> {
  return new Promise((resolvePort, reject) => {
    const probe = createServer()
    probe.once('error', reject)
    probe.listen(0, '127.0.0.1', () => {
      const address = probe.address()
      probe.close(() => (typeof address === 'object' && address !== null ? resolvePort(address.port) : reject(new Error('no port'))))
    })
  })
}

async function waitReady(baseUrl: string, child: ChildProcess, output: () => string): Promise<void> {
  const deadline = Date.now() + 60_000
  while (Date.now() < deadline) {
    if (child.exitCode !== null) {
      throw new Error(`social exited with ${child.exitCode} before it was ready\n${output()}`)
    }
    try {
      const response = await fetch(`${baseUrl}/health/ready`)
      if (response.status === 204) {
        return
      }
    } catch {
      await new Promise((r) => setTimeout(r, 100))
    }
    await new Promise((r) => setTimeout(r, 100))
  }
  throw new Error(`social was not ready within 60s\n${output()}`)
}

export interface SocialProcess {
  readonly url: string
  readonly port: number
  stop(graceMilliseconds?: number): Promise<void>
}

export interface SocialLaunch {
  readonly binary: string
  readonly cwd: string
  readonly dsn: string
  readonly env?: Readonly<Record<string, string>>
  readonly port?: number
}

export async function startSocialProcess(launch: SocialLaunch): Promise<SocialProcess> {
  const port = launch.port ?? (await freePort())
  const url = `http://127.0.0.1:${port}`
  let log = ''
  const child = spawn(launch.binary, [], {
    cwd: launch.cwd,
    env: {
      ...process.env,
      GOLEM_PROVIDER: 'sqlite',
      GOLEM_DATABASE_DSN: launch.dsn,
      GOLEM_HTTP_ADDRESS: `127.0.0.1:${port}`,
      GOLEM_ALLOW_INSECURE_HEADER_AUTH: '1',
      ...launch.env,
    },
  })
  child.stdout?.on('data', (chunk) => (log += chunk))
  child.stderr?.on('data', (chunk) => (log += chunk))
  try {
    await waitReady(url, child, () => log)
  } catch (error) {
    child.kill('SIGKILL')
    throw error
  }
  return {
    url,
    port,
    async stop(graceMilliseconds = 3_000) {
      if (child.exitCode !== null) {
        return
      }
      const exited = new Promise<void>((done) => child.once('exit', () => done()))
      child.kill('SIGTERM')
      await Promise.race([exited, new Promise((r) => setTimeout(r, graceMilliseconds))])
      if (child.exitCode === null) {
        child.kill('SIGKILL')
        await exited
      }
    },
  }
}
