import { atom, read, update } from 'claude-code'
import type { EngineInterface } from 'claude-code'

import type { Busy, Machine } from '../types'
import type { Config } from './config'
import { bandKey, EMPTY_BOARD, leaseText, receipt, soonest, tone } from './model'
import { extend, type Kit } from './state'

const boardAtom = atom({ plugin: 'runlease', key: 'board' } as const, EMPTY_BOARD, { shape: 'board-1' })
const busyAtom = atom({ plugin: 'runlease', key: 'busy' } as const, null as Busy)
const dismissedAtom = atom({ plugin: 'runlease', key: 'dismissed' } as const, [] as string[], { shape: 'dismissed-1' })

const MINUTE = 60 * 1000

export async function dismiss($: EngineInterface, machine: Machine): Promise<void> {
  await update($, dismissedAtom, keys => [...keys, bandKey(machine)].slice(-50))
}

/**
 * The machine the band warns about: the lease that ends first among those
 * inside the warning window that the person has not dismissed. Dismissing one
 * machine's warning must not hide another's.
 */
export async function warned($: EngineInterface, config: Config): Promise<Machine | null> {
  const board = await read($, boardAtom)
  const hidden = await read($, dismissedAtom)
  const first = soonest(board.machines.filter(machine => !hidden.includes(bandKey(machine))))
  if (first === null) return null
  const how = tone(first, board.readAt, config.warnMs)
  return how === 'soon' || how === 'ended' ? first : null
}

/** The band above the prompt while a lease is about to end. */
export async function renderBand($: EngineInterface, kit: Kit, machine: Machine) {
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
export async function launched($: EngineInterface, name: string): Promise<string> {
  if (name === '') return ''
  const board = await read($, boardAtom)
  const machine = board.machines.find(m => m.name === name)
  return machine === undefined ? '' : receipt(machine, board.readAt)
}
