import { spawnSync } from 'node:child_process'
import { copyFileSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import type { TestProject } from 'vitest/node'
import { startSocialProcess } from './process.js'

declare module 'vitest' {
  export interface ProvidedContext {
    socialUrl: string
    socialBinary: string
    socialRoot: string
    socialTemplateDatabase: string
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

  const template = join(work, 'template.sqlite')
  copyFileSync(join(work, 'social.sqlite'), template)
  let social
  try {
    social = await startSocialProcess({ binary, cwd: socialRoot, dsn })
  } catch (error) {
    rmSync(work, { recursive: true, force: true })
    throw error
  }
  project.provide('socialUrl', social.url)
  project.provide('socialBinary', binary)
  project.provide('socialRoot', socialRoot)
  project.provide('socialTemplateDatabase', template)

  return async () => {
    await social.stop()
    rmSync(work, { recursive: true, force: true })
  }
}
