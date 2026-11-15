import { atom, read, update } from 'claude-code'
import type { EngineInterface, Register } from 'claude-code'

import type { Machine } from '../types'

// A live view of the Runlease machines this computer tracks: a status line
// entry, a band when a lease is about to end, and a pane with snooze and
// delete. It only ever reads `runlease list --local`, which makes no cloud
// call, and acts on a machine only when the person presses a button.

const PANE = 'runlease'
const SOON_MS = 10 * 60 * 1000
const POLL_MS = 60 * 1000

const machines = atom({ plugin: 'runlease', key: 'machines' } as const, [], {
  shape: 'machines-1',
})
const now = atom({ plugin: 'runlease', key: 'now' } as const, 0)
const isInstalled = atom({ plugin: 'runlease', key: 'isInstalled' } as const, true)
const dismissed = atom({ plugin: 'runlease', key: 'dismissed' } as const, [], {
  shape: 'dismissed-1',
})
const confirming = atom({ plugin: 'runlease', key: 'confirming' } as const, null)

/** What `runlease list --local --output json` prints, as far as this mod reads it. */
type Row = {
  name?: unknown
  provider?: unknown
  region?: unknown
  state?: unknown
  expiresAt?: unknown
  usdPerHour?: unknown
}

export function parseList(stdout: string): Machine[] {
  let rows: unknown
  try {
    rows = JSON.parse(stdout)
  } catch {
    return []
  }
  if (!Array.isArray(rows)) {
    return []
  }
  const list: Machine[] = []
  for (const row of rows as Row[]) {
    if (row === null || typeof row !== 'object' || typeof row.name !== 'string') {
      continue
    }
    const ends = typeof row.expiresAt === 'string' ? Date.parse(row.expiresAt) : NaN
    list.push({
      name: row.name,
      provider: typeof row.provider === 'string' ? row.provider : '',
      region: typeof row.region === 'string' ? row.region : '',
      state: typeof row.state === 'string' ? row.state : '',
      expiresAt: Number.isNaN(ends) ? null : ends,
      usdPerHour: typeof row.usdPerHour === 'number' ? row.usdPerHour : null,
    })
  }
  return list
}

export function left(machine: Machine, at: number): string {
  if (machine.expiresAt === null) {
    return 'no lease'
  }
  const minutes = Math.round((machine.expiresAt - at) / 60000)
  if (minutes <= 0) {
    return 'lease ended'
  }
  if (minutes < 60) {
    return `${minutes}m left`
  }
  const hours = Math.floor(minutes / 60)
  const rest = minutes % 60
  return rest === 0 ? `${hours}h left` : `${hours}h${rest}m left`
}

/** The machine whose lease ends first, among those that have one. */
export function soonest(list: readonly Machine[]): Machine | null {
  let first: Machine | null = null
  for (const machine of list) {
    if (machine.expiresAt === null) {
      continue
    }
    if (first === null || machine.expiresAt < (first.expiresAt ?? Infinity)) {
      first = machine
    }
  }
  return first
}

export function statusLine(list: readonly Machine[], at: number): string | undefined {
  if (list.length === 0) {
    return undefined
  }
  const count = list.length === 1 ? '1 machine' : `${list.length} machines`
  const first = soonest(list)
  return first === null
    ? `runlease: ${count}`
    : `runlease: ${count} · ${first.name} ${left(first, at)}`
}

const bandKey = (machine: Machine) => `${machine.name}@${machine.expiresAt}`

/** Reads the tracked machines and redraws everything that shows them. */
async function refresh($: EngineInterface): Promise<Machine[]> {
  const at = await $.clock.now()
  let list: Machine[] = []
  try {
    const ran = await $.process.run(
      ['runlease', 'list', '--local', '--output', 'json'],
      { timeoutMs: 10000 },
    )
    if (ran.exitCode === 0) {
      list = parseList(ran.stdout)
    }
    await update($, isInstalled, () => true)
  } catch {
    // The command could not start: not installed, or not on the PATH.
    await update($, isInstalled, () => false)
  }
  await update($, machines, () => list)
  await update($, now, () => at)
  $.ui.status(statusLine(list, at))
  return list
}

export const register: Register = on => {
  let isRunSeen = false

  on('session.start', async ($, e, next) => {
    await $.command.register({
      name: 'runlease',
      description: 'Show Runlease machines in a pane, with snooze and delete',
    })
    const started = await next(e)
    void refresh($)
    $.clock.every(POLL_MS, () => void refresh($))
    return started
  })

  on('command.run', { command: 'runlease' }, async $ => {
    await refresh($)
    await $.ui.open({ id: PANE, title: 'Runlease' })
    return { text: 'Runlease pane opened.' }
  })

  // A shell command that names runlease has probably changed what is running.
  on('tool.call', { tool: 'Bash' }, async ($, e, next) => {
    const ran = await next(e)
    if (/\brunlease\b/.test(e.command)) {
      isRunSeen = true
      void refresh($)
    }
    return ran
  }).catch(($, e, next) => next(e)) // never the reason a command is held up

  on('session.end', async ($, e, next) => {
    if (isRunSeen || (await read($, machines)).length > 0) {
      const list = await refresh($)
      if (list.length > 0) {
        const names = list.map(machine => machine.name).join(', ')
        $.ui.toast(
          `Runlease: still running: ${names}. Each deletes itself when its lease ends.`,
        )
      }
    }
    return next(e)
  })

  on('ui.render', { component: 'AbovePrompt' }, async ($, e, next) => {
    const at = await read($, now)
    const hidden = await read($, dismissed)
    // The soonest lease the person has not dismissed: dismissing one machine's
    // warning must not hide another's.
    const first = soonest(
      (await read($, machines)).filter(machine => !hidden.includes(bandKey(machine))),
    )
    const isSoon =
      first !== null && first.expiresAt !== null && first.expiresAt - at < SOON_MS
    if (e.props.hasSurvey || first === null || !isSoon) {
      return next(e)
    }
    const { Box, Button, Text } = $.ui.resolve(e)
    const name = first.name

    return (
      <Box>
        <Text>
          Runlease: {name} {left(first, at)}{' '}
        </Text>
        <Button
          key="snooze"
          label="Snooze 1h"
          onPress={async () => {
            await $.process.run(['runlease', 'snooze', name, '1h'], { timeoutMs: 60000 })
            await refresh($)
          }}
        />
        <Text> </Text>
        <Button
          key="dismiss"
          label="Dismiss"
          onPress={() => update($, dismissed, keys => [...keys, bandKey(first)].slice(-50))}
        />
      </Box>
    )
  })

  on('ui.render', { component: 'Pane', requestId: PANE }, async ($, e) => {
    const { Box, Button, Text } = $.ui.resolve(e)
    const list = await read($, machines)
    const at = await read($, now)
    const asked = await read($, confirming)

    if (!(await read($, isInstalled))) {
      return (
        <Box flexDirection="column">
          <Text>runlease is not installed on this computer.</Text>
          <Text dimColor>curl -fsSL https://runlease.online/install.sh | sh</Text>
        </Box>
      )
    }

    return (
      <Box flexDirection="column">
        {list.length === 0 && <Text dimColor>No machines running.</Text>}
        {list.map(machine => (
          <Box key={machine.name} flexDirection="column">
            <Text>
              {machine.name}{' '}
              <Text dimColor>
                {machine.provider} {machine.region} · {machine.state} · {left(machine, at)}
                {machine.usdPerHour === null ? '' : ` · $${machine.usdPerHour.toFixed(3)}/h`}
              </Text>
            </Text>
            <Box>
              <Button
                key={`snooze-${machine.name}`}
                label="Snooze 1h"
                onPress={async () => {
                  await $.process.run(['runlease', 'snooze', machine.name, '1h'], {
                    timeoutMs: 60000,
                  })
                  await refresh($)
                }}
              />
              <Text> </Text>
              <Button
                key={`down-${machine.name}`}
                label={asked === machine.name ? 'Press again to delete' : 'Delete'}
                onPress={async () => {
                  // Deleting is not undoable, so it takes two presses.
                  if (asked !== machine.name) {
                    await update($, confirming, () => machine.name)
                    return
                  }
                  await update($, confirming, () => null)
                  await $.process.run(['runlease', 'down', machine.name, '--yes'], {
                    timeoutMs: 240000,
                  })
                  await refresh($)
                }}
              />
            </Box>
          </Box>
        ))}
        <Box>
          <Button key="refresh" label="Refresh" onPress={() => refresh($).then(() => undefined)} />
        </Box>
      </Box>
    )
  })
}
