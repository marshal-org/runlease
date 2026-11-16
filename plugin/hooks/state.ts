import { atom, read, update } from 'claude-code'
import type { Elements, EngineInterface } from 'claude-code'

import type { Board, Busy } from '../types'
import { readConfig, type Config } from './config'
import { changes, cut, EMPTY_BOARD, extendArg, parseList, span, statusLine } from './model'

export const PANE = 'runlease'
export const PANE_TITLE = 'Runlease'

// What every surface draws with. Link is missing from none today, but a
// surface this was not compiled against may lack it: the pane works without.
export type Kit = Pick<Elements['terminal'], 'Box' | 'Text' | 'Button'> & Partial<Pick<Elements['terminal'], 'Link'>>

// Atoms are declared in each file that reads or writes them. Same ref, same value.
const boardAtom = atom({ plugin: 'runlease', key: 'board' } as const, EMPTY_BOARD, { shape: 'board-1' })
const busyAtom = atom({ plugin: 'runlease', key: 'busy' } as const, null as Busy)
const confirmingAtom = atom({ plugin: 'runlease', key: 'confirming' } as const, null as string | null)

// Module state: rebuilt on every load, which is right (a reload drops the
// timers too). What the screen draws lives in $.state.
export const mod = {
  config: readConfig({}) as Config,
  isReading: false,
  // True once this session ran a command that launches a machine.
  hasLaunched: false,
  // True once this session ran any runlease command.
  hasUsed: false,
}

const CONFIRM_MS = 6000

function lastLine(s: string): string {
  const lines = s.trim().split('\n')
  return (lines[lines.length - 1] ?? '').trim()
}

/** Tells the person what changed between two reads. */
function announce($: EngineInterface, prev: Board, next: Board): void {
  for (const change of changes(prev, next, mod.config.warnMs)) {
    $.ui.toast(change.text, { timeoutMs: change.isUrgent ? 8000 : 4000 })
    // A desktop notification only for a lease about to end: the one change
    // that costs work when it is missed.
    if (change.kind === 'soon' && mod.config.notify) {
      void $.ui.notify(change.text, { title: PANE_TITLE, key: `lease:${change.name}` }).catch(() => undefined)
    }
    // The pane opens by itself only for a machine this session launched.
    if (change.kind === 'up' && mod.config.pane === 'auto' && mod.hasLaunched) {
      void $.ui.open({ id: PANE, title: PANE_TITLE }).catch(() => undefined)
    }
  }
}

/**
 * Reads the machines this computer tracks and redraws everything that shows
 * them. `runlease list --local` makes no cloud call.
 */
export async function refresh($: EngineInterface): Promise<Board> {
  const prev = await read($, boardAtom)
  if (mod.isReading) return prev
  mod.isReading = true
  try {
    const at = await $.clock.now()
    // One failed read must not make running machines vanish from view: the
    // last good list stays, and only the clock moves.
    let next: Board = { ...prev, readAt: at }
    try {
      const ran = await $.process.run(['runlease', 'list', '--local', '--output', 'json'], { timeoutMs: 10000 })
      next = ran.exitCode === 0 ? { machines: parseList(ran.stdout), readAt: at, isInstalled: true } : { ...next, isInstalled: true }
    } catch (error) {
      // Only "could not start" means not installed; a timeout does not.
      if (/ENOENT|not found|no such file/i.test(String(error))) next = { machines: [], readAt: at, isInstalled: false }
    }
    await update($, boardAtom, () => next)
    $.ui.status(statusLine(next))
    announce($, prev, next)
    return next
  } finally {
    mod.isReading = false
  }
}

/** Runs one runlease command for a button, one at a time, and says how it went. */
async function act($: EngineInterface, busy: NonNullable<Busy>, argv: string[], timeoutMs: number, done: string): Promise<void> {
  if ((await read($, busyAtom)) !== null) return
  await update($, busyAtom, () => busy)
  try {
    const ran = await $.process.run(argv, { timeoutMs })
    if (ran.exitCode === 0) {
      $.ui.toast(done)
    } else {
      const why = lastLine(ran.stderr) || lastLine(ran.stdout) || `runlease exited with ${ran.exitCode}`
      $.ui.toast(`${busy.name}: ${cut(why, 200)}`, { timeoutMs: 10000 })
    }
  } catch (error) {
    $.ui.toast(`${busy.name}: ${cut(error instanceof Error ? error.message : String(error), 200)}`, { timeoutMs: 10000 })
  } finally {
    await update($, busyAtom, () => null)
  }
  await refresh($)
}

/** Adds time to a lease. The press that calls this is the person's own act. */
export async function extend($: EngineInterface, name: string, addMs: number): Promise<void> {
  const machine = (await read($, boardAtom)).machines.find(m => m.name === name)
  if (machine === undefined) return
  const length = extendArg(machine, await $.clock.now(), addMs)
  await act($, { name, action: 'extend' }, ['runlease', 'snooze', name, length], 60000, `${name}: ${span(addMs)} added to the lease`)
}

/** Deletes a machine. It cannot be undone, so it takes two presses. */
export async function remove($: EngineInterface, name: string): Promise<void> {
  if ((await read($, confirmingAtom)) !== name) {
    await update($, confirmingAtom, () => name)
    // An unanswered question goes away: a later stray press must not delete.
    $.clock.after(CONFIRM_MS, () => void update($, confirmingAtom, asked => (asked === name ? null : asked)))
    return
  }
  await update($, confirmingAtom, () => null)
  await act($, { name, action: 'delete' }, ['runlease', 'down', name, '--yes'], 240000, `${name} deleted`)
}
