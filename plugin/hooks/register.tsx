import { atom, read, update } from 'claude-code'
import type { Elements, EngineInterface, Register } from 'claude-code'

import type { Board, Busy, Machine } from '../types'
import { readConfig, type Config } from './config'
import {
  bandKey, barCells, burn, changes, count, cut, EMPTY_BOARD, extendArg, isRunleaseCommand, isRunleaseTool, launchedName,
  leaseText, money, nextToEnd, parseList, perHour, rate, receipt, soonest, span, spent, statusLine, tone, type Tone,
} from './model'

// A live view of the Runlease machines this computer tracks: a status line, a
// pane with each lease and its cost, a band when a lease is about to end, and
// one line under the tool call that launched a machine. Reading is always
// `runlease list --local`, which makes no cloud call. A machine is changed
// only when the person presses a button.
//
// Everything that touches the engine ($) is in this one file, each call
// written as $.noun.method(...), so what the module can do is read in one
// place. model.ts and config.ts hold the logic that needs no engine.

const PANE = 'runlease'
const PANE_TITLE = 'Runlease'

// What every surface draws with. Link is missing from none today, but a
// surface this was not compiled against may lack it: the pane works without.
type Kit = Pick<Elements['terminal'], 'Box' | 'Text' | 'Button'> & Partial<Pick<Elements['terminal'], 'Link'>>

const boardAtom = atom({ plugin: 'runlease', key: 'board' } as const, EMPTY_BOARD, { shape: 'board-1' })
const busyAtom = atom({ plugin: 'runlease', key: 'busy' } as const, null as Busy)
const confirmingAtom = atom({ plugin: 'runlease', key: 'confirming' } as const, null as string | null)
const selectedAtom = atom({ plugin: 'runlease', key: 'selected' } as const, null as string | null)
const dismissedAtom = atom({ plugin: 'runlease', key: 'dismissed' } as const, [] as string[], { shape: 'dismissed-1' })

// Module state: rebuilt on every load, which is right (a reload drops the
// timers too). What the screen draws lives in $.state.
const mod = {
  config: readConfig({}) as Config,
  isReading: false,
  // True once this session ran a command that launches a machine.
  hasLaunched: false,
  // True once this session ran any runlease command.
  hasUsed: false,
}

const CONFIRM_MS = 6000
const MINUTE = 60 * 1000

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
async function refresh($: EngineInterface): Promise<Board> {
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

type Ran = { exitCode: number | null; stdout: string; stderr: string }

/**
 * Runs one runlease command for a button, one at a time, and says how it went.
 * The command itself is written where it is called, so every program this
 * module starts can be read off a `$.process.run` line.
 */
async function act($: EngineInterface, busy: NonNullable<Busy>, run: () => Promise<Ran>, done: string): Promise<void> {
  if ((await read($, busyAtom)) !== null) return
  await update($, busyAtom, () => busy)
  try {
    const ran = await run()
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
async function extend($: EngineInterface, name: string, addMs: number): Promise<void> {
  const machine = (await read($, boardAtom)).machines.find(m => m.name === name)
  if (machine === undefined) return
  const length = extendArg(machine, await $.clock.now(), addMs)
  const snooze = () => $.process.run(['runlease', 'snooze', name, length], { timeoutMs: 60000 })
  await act($, { name, action: 'extend' }, snooze, `${name}: ${span(addMs)} added to the lease`)
}

/** Deletes a machine. It cannot be undone, so it takes two presses. */
async function remove($: EngineInterface, name: string): Promise<void> {
  if ((await read($, confirmingAtom)) !== name) {
    await update($, confirmingAtom, () => name)
    // An unanswered question goes away: a later stray press must not delete.
    $.clock.after(CONFIRM_MS, () => void update($, confirmingAtom, asked => (asked === name ? null : asked)))
    return
  }
  await update($, confirmingAtom, () => null)
  const down = () => $.process.run(['runlease', 'down', name, '--yes'], { timeoutMs: 240000 })
  await act($, { name, action: 'delete' }, down, `${name} deleted`)
}

// ---- The band above the prompt ----

async function dismiss($: EngineInterface, machine: Machine): Promise<void> {
  await update($, dismissedAtom, keys => [...keys, bandKey(machine)].slice(-50))
}

/**
 * The machine the band warns about: the lease that ends first among those
 * inside the warning window that the person has not dismissed. Dismissing one
 * machine's warning must not hide another's.
 */
async function warned($: EngineInterface, config: Config): Promise<Machine | null> {
  const board = await read($, boardAtom)
  const hidden = await read($, dismissedAtom)
  const first = soonest(board.machines.filter(machine => !hidden.includes(bandKey(machine))))
  if (first === null) return null
  const how = tone(first, board.readAt, config.warnMs)
  return how === 'soon' || how === 'ended' ? first : null
}

/** The band above the prompt while a lease is about to end. */
async function renderBand($: EngineInterface, kit: Kit, machine: Machine) {
  const { Box, Text, Button } = kit
  const board = await read($, boardAtom)
  const busy = await read($, busyAtom)
  const name = machine.name
  const isOver = tone(machine, board.readAt, MINUTE) === 'ended'

  return (
    <Box flexDirection="row" gap={1} flexWrap="wrap">
      <Text color={isOver ? 'error' : 'warning'} bold>
        {isOver ? '○' : '●'} {name}
      </Text>
      <Text color={isOver ? 'error' : 'warning'}>{isOver ? 'lease ended, it is deleting itself' : `${leaseText(machine, board.readAt)}, then it deletes itself`}</Text>
      {busy !== null && busy.name === name && <Text dimColor>Extending the lease…</Text>}
      {!isOver && busy === null && <Button key="band-add30" label="+30m" onPress={() => extend($, name, 30 * MINUTE)} />}
      {!isOver && busy === null && <Button key="band-add60" label="+1h" variant="primary" onPress={() => extend($, name, 60 * MINUTE)} />}
      <Button key="band-dismiss" label="Dismiss" role="dismiss" onPress={() => dismiss($, machine)} />
    </Box>
  )
}

/** The machine a tool call launched, once it is tracked: one line under the call's row. */
async function launched($: EngineInterface, name: string): Promise<string> {
  if (name === '') return ''
  const board = await read($, boardAtom)
  const machine = board.machines.find(m => m.name === name)
  return machine === undefined ? '' : receipt(machine, board.readAt)
}

// ---- The pane ----

const INSTALL = 'curl -fsSL https://runlease.online/install.sh | sh'
const DOCS = 'https://runlease.online/how-it-works.html'

const TONE_COLOR: Record<Tone, 'success' | 'warning' | 'error' | undefined> = {
  none: undefined,
  ok: 'success',
  soon: 'warning',
  ended: 'error',
}

/** Opens one machine's row, or closes it when it is the open one. */
async function toggle($: EngineInterface, name: string, isOpen: boolean): Promise<void> {
  await update($, selectedAtom, () => (isOpen ? '' : name))
}

async function copy($: EngineInterface, text: string, what: string): Promise<void> {
  const copied = await $.ui.copy({ text })
  $.ui.toast(copied.isCopied ? `Copied: ${what}` : `Could not copy. It is: ${text}`, { timeoutMs: copied.isCopied ? 3000 : 10000 })
}

/** Puts the start of a prompt in the box; the person finishes and sends it. */
async function draft($: EngineInterface, text: string): Promise<void> {
  const filled = await $.prompt.fill({ text })
  if (!filled.isFilled) $.ui.toast(`Type: ${text.trim()}`)
}

function rule(kit: Kit, width: number) {
  const { Text } = kit
  return <Text dimColor>{'─'.repeat(Math.max(8, width))}</Text>
}

function dot(kit: Kit, machine: Machine, how: Tone) {
  const { Text } = kit
  if (how === 'ended') return <Text color="error">○</Text>
  if (machine.state !== 'running') return <Text color="warning">◐</Text>
  return <Text color={how === 'soon' ? 'warning' : 'success'}>●</Text>
}

function actions($: EngineInterface, kit: Kit, machine: Machine, busy: Busy, asked: string | null) {
  const { Box, Text, Button } = kit
  const name = machine.name
  if (busy !== null && busy.name === name) {
    return (
      <Box paddingLeft={2}>
        <Text color="warning">{busy.action === 'delete' ? 'Deleting…' : 'Extending the lease…'}</Text>
      </Box>
    )
  }
  return (
    <Box flexDirection="column" paddingLeft={2}>
      {machine.expiresAt !== null && (
        <Box flexDirection="row" gap={1} flexWrap="wrap">
          <Text dimColor>Lease</Text>
          <Button key={`add30:${name}`} label="+30m" hotkey="1" onPress={() => extend($, name, 30 * MINUTE)} />
          <Button key={`add60:${name}`} label="+1h" hotkey="2" onPress={() => extend($, name, 60 * MINUTE)} />
          <Button key={`add240:${name}`} label="+4h" hotkey="3" onPress={() => extend($, name, 240 * MINUTE)} />
        </Box>
      )}
      <Box flexDirection="row" gap={1} flexWrap="wrap">
        <Button key={`ask:${name}`} label="Use in prompt" hotkey="u" onPress={() => draft($, `On the Runlease machine ${name}: `)} />
        <Button key={`ssh:${name}`} label="Copy ssh" hotkey="c" onPress={() => copy($, `runlease ssh ${name}`, `runlease ssh ${name}`)} />
        <Button
          key={`down:${name}`}
          label={asked === name ? 'Press again to delete' : 'Delete'}
          hotkey="d"
          variant={asked === name ? 'primary' : 'secondary'}
          onPress={() => remove($, name)}
        />
      </Box>
    </Box>
  )
}

function machineRows($: EngineInterface, kit: Kit, width: number, at: number, config: Config, machine: Machine, isOpen: boolean, busy: Busy, asked: string | null) {
  const { Box, Text, Button } = kit
  const name = machine.name
  const how = tone(machine, at, config.warnMs)
  const lease = leaseText(machine, at)
  const where = [machine.provider, machine.region, machine.instanceType, machine.state === 'running' ? '' : machine.state].filter(Boolean).join(' · ')
  const cost = [rate(machine), spent(machine, at) === null ? '' : `${money(spent(machine, at) ?? 0)} so far`].filter(Boolean).join(' · ')
  // The bar takes what the cost leaves; on a pane too narrow for it, it is left out.
  const bar = barCells(machine, at, Math.min(24, width - cost.length - 5))

  return (
    <Box key={`m:${name}`} flexDirection="column" marginTop={1}>
      <Box flexDirection="row" justifyContent="space-between">
        <Button key={`row:${name}`} plain onPress={() => toggle($, name, isOpen)}>
          {dot(kit, machine, how)} <Text bold>{cut(name, Math.max(8, width - lease.length - 6))}</Text> <Text dimColor>{isOpen ? '▾' : '▸'}</Text>
        </Button>
        <Text color={TONE_COLOR[how]} bold={how === 'soon' || how === 'ended'}>
          {lease}
        </Text>
      </Box>
      <Box paddingLeft={2}>
        <Text dimColor wrap="truncate-end">
          {where}
        </Text>
      </Box>
      <Box flexDirection="row" gap={2} paddingLeft={2}>
        {bar !== null && (
          <Text>
            <Text color={TONE_COLOR[how] ?? 'success'}>{'━'.repeat(bar.full)}</Text>
            <Text dimColor>{'─'.repeat(bar.empty)}</Text>
          </Text>
        )}
        {cost !== '' && <Text dimColor>{cost}</Text>}
      </Box>
      {isOpen && actions($, kit, machine, busy, asked)}
    </Box>
  )
}

function notInstalled($: EngineInterface, kit: Kit) {
  const { Box, Text, Button, Link } = kit
  return (
    <Box flexDirection="column" gap={1}>
      <Text bold>Runlease</Text>
      <Text>The runlease command is not on this computer yet. Install it with one line:</Text>
      <Text color="suggestion">{INSTALL}</Text>
      <Box flexDirection="row" gap={1} flexWrap="wrap">
        <Button key="copy-install" label="Copy the install line" variant="primary" onPress={() => copy($, INSTALL, 'the install line')} />
        <Button key="refresh" label="Check again" hotkey="r" onPress={() => refresh($).then(() => undefined)} />
        {Link && <Link key="docs" href="https://runlease.online/install.html" label="Install guide" />}
      </Box>
    </Box>
  )
}

function empty($: EngineInterface, kit: Kit) {
  const { Box, Text, Button, Link } = kit
  return (
    <Box flexDirection="column" gap={1}>
      <Text>No machines running.</Text>
      <Text dimColor>A leased machine is a real Linux VM in your own cloud account. It deletes itself when its lease ends.</Text>
      <Box flexDirection="row" gap={1} flexWrap="wrap">
        <Button key="launch" label="Launch a machine" variant="primary" hotkey="l" onPress={() => draft($, '/runlease:up ')} />
        <Button key="doctor" label="Check setup" hotkey="s" onPress={() => draft($, '/runlease:doctor')} />
        {Link && <Link key="docs" href={DOCS} label="How it works" />}
      </Box>
    </Box>
  )
}

async function renderPane($: EngineInterface, kit: Kit, width: number, config: Config) {
  const { Box, Text, Button, Link } = kit
  const board = await read($, boardAtom)
  if (!board.isInstalled) return notInstalled($, kit)
  if (board.readAt === 0) return <Text dimColor>Reading your machines…</Text>

  const list = board.machines
  const at = board.readAt
  const busy = await read($, busyAtom)
  const asked = await read($, confirmingAtom)
  const selected = await read($, selectedAtom)
  // With one machine its row is open from the start; '' means the person closed it.
  const open = selected ?? (list.length === 1 ? (list[0]?.name ?? '') : '')

  const hourly = burn(list, at)
  const first = nextToEnd(list, at)
  const summary = [count(list.length), hourly === null ? '' : perHour(hourly), first === null || list.length < 2 ? '' : `next ends in ${span((first.expiresAt ?? at) - at)}`]
    .filter(Boolean)
    .join(' · ')

  return (
    <Box flexDirection="column">
      <Box flexDirection="row" justifyContent="space-between">
        <Text bold color="claude">
          Runlease
        </Text>
        {list.length > 0 && <Text dimColor>{cut(summary, Math.max(10, width - 10))}</Text>}
      </Box>
      {rule(kit, width)}
      {list.length === 0 && empty($, kit)}
      {list.map(machine => machineRows($, kit, width, at, config, machine, machine.name === open, busy, asked))}
      {list.length > 0 && (
        <Box flexDirection="column" marginTop={1}>
          {rule(kit, width)}
          <Box flexDirection="row" gap={1} flexWrap="wrap">
            <Button key="refresh" label="Refresh" hotkey="r" onPress={() => refresh($).then(() => undefined)} />
            <Button key="launch" label="Launch another" hotkey="l" onPress={() => draft($, '/runlease:up ')} />
            {Link && <Link key="docs" href={DOCS} label="Docs" />}
          </Box>
          <Text dimColor>Each machine deletes itself when its lease ends.</Text>
        </Box>
      )}
    </Box>
  )
}

// ---- Registration ----

function kitOf(t: Kit): Kit {
  return { Box: t.Box, Text: t.Text, Button: t.Button, ...(t.Link && { Link: t.Link }) }
}

export const register: Register = (on, options) => {
  mod.config = readConfig(options)

  on('session.start', async ($, e, next) => {
    if (mod.config.pane !== 'off') {
      await $.command.register({ name: 'runlease', description: 'Show your Runlease machines: lease, cost, extend and delete' })
    }
    const started = await next(e)
    void refresh($)
    $.clock.every(mod.config.pollMs, () => void refresh($))
    return started
  })

  on('command.run', { command: 'runlease' }, async $ => {
    const board = await refresh($)
    const opened = await $.ui.open({ id: PANE, title: PANE_TITLE })
    // The engine writes the plugin's name before the answer.
    if (!opened.isPlaced) return { text: `The pane is waiting: ${opened.reason}` }
    return { text: board.machines.length === 0 ? 'Pane opened. No machines running.' : `Pane opened: ${count(board.machines.length)}.` }
  })

  // A shell command that names runlease, or one of the typed tools that
  // `runlease mcp` serves, has probably changed what is running.
  on('tool.call', async ($, e, next) => {
    const ran = await next(e)
    const command = e.tool === 'Bash' ? String((e as { command?: unknown }).command ?? '') : ''
    if (isRunleaseCommand(command) || isRunleaseTool(e.tool)) {
      mod.hasUsed = true
      mod.hasLaunched ||= /\brunlease\s+up\b/.test(command) || e.tool.endsWith('__runlease_up')
      void refresh($)
    }
    return ran
  }).catch(($, e, next) => next(e)) // never the reason a call is held up

  on('session.end', async ($, e, next) => {
    if (mod.hasUsed) {
      const board = await refresh($)
      if (board.machines.length > 0) {
        const names = board.machines.map(machine => machine.name).join(', ')
        $.ui.toast(`Still running: ${names}. Each deletes itself when its lease ends.`)
      }
    }
    return next(e)
  })

  on('ui.render', { component: 'AbovePrompt' }, async ($, e, next) => {
    if (e.props.hasSurvey) return next(e)
    const machine = await warned($, mod.config)
    if (machine === null) return next(e)
    return renderBand($, kitOf($.ui.resolve(e) as Kit), machine)
  })

  on('ui.render', { component: 'Pane', requestId: 'runlease' }, async ($, e) =>
    renderPane($, kitOf($.ui.resolve(e) as Kit), e.props.bodyColumns, mod.config),
  )

  // Under the call that launched a machine: what was launched, in one quiet line.
  on('ui.render', { component: 'ToolUse' }, async ($, e, next) => {
    const row = await next(e)
    if (e.props.isRunning || e.props.isErrored || e.props.isInterrupted) return row
    const line = await launched($, launchedName(e.props.tool, e.props.input))
    if (line === '') return row
    const { Box, Text } = $.ui.resolve(e)
    return (
      <Box flexDirection="column">
        {row}
        <Text dimColor>  ⎿  Leased: {line}</Text>
      </Box>
    )
  }).catch(($, e, next) => next(e))
}
