import { atom, read, update } from 'claude-code'
import type { EngineInterface } from 'claude-code'

import type { Busy, Machine } from '../types'
import type { Config } from './config'
import { barCells, burn, count, cut, EMPTY_BOARD, leaseText, money, nextToEnd, rate, span, spent, tone, type Tone } from './model'
import { extend, refresh, remove, type Kit } from './state'

const boardAtom = atom({ plugin: 'runlease', key: 'board' } as const, EMPTY_BOARD, { shape: 'board-1' })
const busyAtom = atom({ plugin: 'runlease', key: 'busy' } as const, null as Busy)
const confirmingAtom = atom({ plugin: 'runlease', key: 'confirming' } as const, null as string | null)
const selectedAtom = atom({ plugin: 'runlease', key: 'selected' } as const, null as string | null)

const INSTALL = 'curl -fsSL https://runlease.online/install.sh | sh'
const DOCS = 'https://runlease.online/how-it-works.html'
const MINUTE = 60 * 1000

const TONE_COLOR: Record<Tone, 'success' | 'warning' | 'error' | undefined> = {
  none: undefined,
  ok: 'success',
  soon: 'warning',
  ended: 'error',
}

/** Opens one machine's row, or closes it when it is the open one. */
export async function toggle($: EngineInterface, name: string, isOpen: boolean): Promise<void> {
  await update($, selectedAtom, () => (isOpen ? '' : name))
}

export async function copy($: EngineInterface, text: string, what: string): Promise<void> {
  const copied = await $.ui.copy({ text })
  $.ui.toast(copied.isCopied ? `Copied: ${what}` : `Could not copy. It is: ${text}`, { timeoutMs: copied.isCopied ? 3000 : 10000 })
}

/** Puts the start of a prompt in the box; the person finishes and sends it. */
export async function draft($: EngineInterface, text: string): Promise<void> {
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
  const where = [machine.provider, machine.region, machine.instanceType].filter(Boolean).join(' · ')
  const cost = [rate(machine), spent(machine, at) === null ? '' : `${money(spent(machine, at) ?? 0)} so far`].filter(Boolean).join(' · ')
  const bar = barCells(machine, at, Math.min(24, Math.max(8, width - cost.length - 5)))

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
          {machine.state !== 'running' && machine.state !== '' ? ` · ${machine.state}` : ''}
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

export async function renderPane($: EngineInterface, kit: Kit, width: number, config: Config) {
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
  const summary = [count(list.length), hourly === null ? '' : `$${hourly.toFixed(2)}/h`, first === null || list.length < 2 ? '' : `next ends in ${span((first.expiresAt ?? at) - at)}`]
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
