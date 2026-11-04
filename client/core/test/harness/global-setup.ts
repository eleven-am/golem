import { spawn, spawnSync, type ChildProcess } from 'node:child_process'
import { mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { createServer } from 'node:net'
import { tmpdir } from 'node:os'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import type { TestProject } from 'vitest/node'

declare module 'vitest' {
  export interface ProvidedContext {
    socialUrl: string
  }
}

const here = dirname(fileURLToPath(import.meta.url))
const repoRoot = resolve(here, '../../../..')
const goRoot = join(repoRoot, 'go')
const socialRoot = join(goRoot, 'examples', 'social')

function run(command: string, args: string[], cwd: string, env: NodeJS.ProcessEnv): void {
  const result = spawnSync(command, args, { cwd, env, encoding: 'utf8' })
  if (result.status !== 0) {
    throw new Error(`${command} ${args.join(' ')} failed in ${cwd}\n${result.stdout}\n${result.stderr}`)
  }
}

function freePort(): Promise<number> {
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

export default async function setup(project: TestProject): Promise<() => Promise<void>> {
  const work = mkdtempSync(join(tmpdir(), 'golem-client-social-'))
  const workFile = join(work, 'go.work')
  writeFileSync(workFile, `go 1.26.0\n\nuse (\n\t${goRoot}\n\t${socialRoot}\n)\n\nreplace github.com/eleven-am/golem/go v0.0.0 => ${goRoot}\n`)
  const baseEnv = { ...process.env, GOFLAGS: '-p=1' }
  const cli = join(work, 'golem')
  const binary = join(work, 'social')
  run('go', ['build', '-o', cli, './cmd/golem'], goRoot, { ...baseEnv, GOWORK: 'off' })
  run('go', ['build', '-o', binary, './cmd/social'], socialRoot, { ...baseEnv, GOWORK: workFile })
  const dsn = `file:${join(work, 'social.sqlite')}`
  run(cli, ['migration', 'apply', '--provider', 'sqlite', '--dsn', dsn, '--migrations', 'migrations'], socialRoot, { ...baseEnv, GOWORK: 'off' })

  const port = await freePort()
  const baseUrl = `http://127.0.0.1:${port}`
  let log = ''
  const child = spawn(binary, [], {
    cwd: socialRoot,
    env: {
      ...process.env,
      GOLEM_PROVIDER: 'sqlite',
      GOLEM_DATABASE_DSN: dsn,
      GOLEM_HTTP_ADDRESS: `127.0.0.1:${port}`,
      GOLEM_ALLOW_INSECURE_HEADER_AUTH: '1',
    },
  })
  child.stdout?.on('data', (chunk) => (log += chunk))
  child.stderr?.on('data', (chunk) => (log += chunk))
  try {
    await waitReady(baseUrl, child, () => log)
  } catch (error) {
    child.kill('SIGKILL')
    rmSync(work, { recursive: true, force: true })
    throw error
  }
  project.provide('socialUrl', baseUrl)

  return async () => {
    const exited = new Promise<void>((done) => child.once('exit', () => done()))
    child.kill('SIGTERM')
    await Promise.race([exited, new Promise((r) => setTimeout(r, 3_000))])
    if (child.exitCode === null) {
      child.kill('SIGKILL')
    }
    rmSync(work, { recursive: true, force: true })
  }
}
