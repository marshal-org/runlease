// Pure logic of the Runlease mod: reading what the CLI prints, and turning it
// into the words, bars and changes the screen shows. No engine calls here.

import type { Board, Machine } from '../types'

export const EMPTY_BOARD: Board = { machines: [], readAt: 0, isInstalled: true }

const MINUTE = 60 * 1000
const HOUR = 60 * MINUTE

function str(v: unknown): string {
  return typeof v === 'string' ? v : ''
}

function time(v: unknown): number | null {
  const at = typeof v === 'string' ? Date.parse(v) : NaN
  return Number.isFinite(at) ? at : null
}

/** What `runlease list --local --output json` prints, as far as this mod reads it. */
export function parseList(stdout: string): Machine[] {
  let rows: unknown
  try {
    rows = JSON.parse(stdout)
  } catch {
    return []
  }
  if (!Array.isArray(rows)) return []
  const list: Machine[] = []
  for (const row of rows as unknown[]) {
    if (row === null || typeof row !== 'object') continue
    const r = row as Record<string, unknown>
    if (typeof r.name !== 'string' || r.name === '') continue
    list.push({
      name: r.name,
      provider: str(r.provider),
      region: str(r.region),
      instanceType: str(r.instanceType),
      state: str(r.state),
      createdAt: time(r.createdAt),
      expiresAt: time(r.expiresAt),
      usdPerHour: typeof r.usdPerHour === 'number' && Number.isFinite(r.usdPerHour) ? r.usdPerHour : null,
      isPriceApprox: r.priceApprox === true,
    })
  }
  // The lease that ends first comes first; machines with no lease go last.
  return list.sort((a, b) => (a.expiresAt ?? Infinity) - (b.expiresAt ?? Infinity) || a.name.localeCompare(b.name))
}

/** A length of time as a person would say it: "45s", "7m", "3h 12m", "2d 4h". */
export function span(ms: number): string {
  const seconds = Math.max(0, Math.round(ms / 1000))
  if (seconds < 60) return `${seconds}s`
  const minutes = Math.floor(seconds / 60)
  if (minutes < 60) return `${minutes}m`
  const hours = Math.floor(minutes / 60)
  if (hours < 48) return minutes % 60 === 0 ? `${hours}h` : `${hours}h ${minutes % 60}m`
  return hours % 24 === 0 ? `${hours / 24}d` : `${Math.floor(hours / 24)}d ${hours % 24}h`
}

export type Tone = 'none' | 'ok' | 'soon' | 'ended'

/** How urgent a lease is: no lease, plenty left, inside the warning window, or over. */
export function tone(machine: Machine, at: number, warnMs: number): Tone {
  if (machine.expiresAt === null) return 'none'
  const left = machine.expiresAt - at
  if (left <= 0) return 'ended'
  return left < warnMs ? 'soon' : 'ok'
}

export function leaseText(machine: Machine, at: number): string {
  if (machine.expiresAt === null) return 'no lease'
  const left = machine.expiresAt - at
  return left <= 0 ? 'lease ended' : `${span(left)} left`
}

/** How much of the lease is left, from 0 to 1; null when it cannot be told. */
export function remaining(machine: Machine, at: number): number | null {
  if (machine.expiresAt === null || machine.createdAt === null) return null
  const whole = machine.expiresAt - machine.createdAt
  if (whole <= 0) return null
  return Math.min(1, Math.max(0, (machine.expiresAt - at) / whole))
}

/** The lease as a bar of `width` cells: how many are still full. */
export function barCells(machine: Machine, at: number, width: number): { full: number; empty: number } | null {
  const part = remaining(machine, at)
  if (part === null || width < 4) return null
  // A lease with any time left never draws as an empty bar.
  const full = part > 0 ? Math.max(1, Math.round(part * width)) : 0
  return { full, empty: width - full }
}

export function money(usd: number): string {
  return usd < 10 ? `$${usd.toFixed(2)}` : `$${usd.toFixed(0)}`
}

/** The hourly rate: three decimals, as spot prices are quoted. */
export function rate(machine: Machine): string {
  if (machine.usdPerHour === null) return ''
  return `${machine.isPriceApprox ? '~' : ''}$${machine.usdPerHour.toFixed(3)}/h`
}

/** What the machine has cost since it was launched; null when it cannot be told. */
export function spent(machine: Machine, at: number): number | null {
  if (machine.usdPerHour === null || machine.createdAt === null) return null
  // A machine stops costing when its lease ends: it deletes itself then.
  const until = machine.expiresAt === null ? at : Math.min(at, machine.expiresAt)
  return (Math.max(0, until - machine.createdAt) / HOUR) * machine.usdPerHour
}

/** What the machines still running cost per hour together; null when no rate is known. */
export function burn(list: readonly Machine[], at: number): number | null {
  // A machine whose lease has ended is deleting itself and no longer counts.
  const rates = list
    .filter(m => m.expiresAt === null || m.expiresAt > at)
    .map(m => m.usdPerHour)
    .filter((r): r is number => r !== null)
  return rates.length === 0 ? null : rates.reduce((sum, r) => sum + r, 0)
}

/** The machine whose lease ends first, among those that have one. */
export function soonest(list: readonly Machine[]): Machine | null {
  let first: Machine | null = null
  for (const machine of list) {
    if (machine.expiresAt === null) continue
    if (first === null || machine.expiresAt < (first.expiresAt ?? Infinity)) first = machine
  }
  return first
}

export function count(n: number): string {
  return n === 1 ? '1 machine' : `${n} machines`
}

/** The lease that ends next among those still running; null with none. */
export function nextToEnd(list: readonly Machine[], at: number): Machine | null {
  return soonest(list.filter(m => m.expiresAt !== null && m.expiresAt > at))
}

/**
 * The line under the prompt. Nothing while no machine runs. The engine writes
 * the plugin's name before it, so the line does not repeat it.
 */
export function statusLine(board: Board): string | undefined {
  const list = board.machines
  if (list.length === 0) return undefined
  const parts = [count(list.length)]
  const first = soonest(list)
  if (first !== null) parts.push(`${first.name} ${leaseText(first, board.readAt)}`)
  const hourly = burn(list, board.readAt)
  if (hourly !== null) parts.push(`$${hourly.toFixed(2)}/h`)
  return parts.join(' · ')
}

export type Change = { kind: 'up' | 'gone' | 'soon'; name: string; text: string; isUrgent: boolean }

/**
 * What is new since the last read, for toasts and notifications. The first
 * read reports nothing: the pane shows what was already there.
 */
export function changes(prev: Board, next: Board, warnMs: number): Change[] {
  if (prev.readAt === 0) return []
  const out: Change[] = []
  const before = new Map(prev.machines.map(m => [m.name, m]))
  const after = new Set(next.machines.map(m => m.name))
  for (const m of next.machines) {
    const old = before.get(m.name)
    if (old === undefined) {
      const where = [m.provider, m.region].filter(Boolean).join(' ')
      out.push({ kind: 'up', name: m.name, text: `${m.name} is up${where ? ` on ${where}` : ''} · ${leaseText(m, next.readAt)}`, isUrgent: false })
      continue
    }
    // Only the moment a lease crosses into the warning window, once per deadline.
    if (tone(m, next.readAt, warnMs) === 'soon' && tone(old, prev.readAt, warnMs) === 'ok') {
      out.push({ kind: 'soon', name: m.name, text: `${m.name}: ${leaseText(m, next.readAt)}, then it deletes itself`, isUrgent: true })
    }
  }
  for (const m of prev.machines) {
    if (after.has(m.name)) continue
    const isLeaseOver = m.expiresAt !== null && m.expiresAt <= next.readAt
    out.push({ kind: 'gone', name: m.name, text: isLeaseOver ? `${m.name} deleted itself at the end of its lease` : `${m.name} is gone`, isUrgent: false })
  }
  return out
}

/**
 * `runlease snooze` sets the deadline to a length of time from now, so adding
 * time means asking for what is left plus the addition. Whole minutes, rounded up.
 */
export function extendArg(machine: Machine, at: number, addMs: number): string {
  const left = machine.expiresAt === null ? 0 : Math.max(0, machine.expiresAt - at)
  return `${Math.max(1, Math.ceil((left + addMs) / MINUTE))}m`
}

/** The key a dismissed warning is remembered under: a new deadline warns again. */
export function bandKey(machine: Machine): string {
  return `${machine.name}@${machine.expiresAt}`
}

/** True for a shell command that names the runlease CLI. */
export function isRunleaseCommand(command: string): boolean {
  return /(^|[\s;&|(])runlease(\s|$)/.test(command)
}

/** True for one of the tools `runlease mcp` serves, under any server name. */
export function isRunleaseTool(tool: string): boolean {
  return /^mcp__.*__runlease_[a-z]+$/.test(tool)
}

/**
 * The machine a tool call launched, by its name: a `runlease up -n NAME` shell
 * command, or the `runlease_up` tool. Empty when the call launched nothing or
 * left the name to the CLI.
 */
export function launchedName(tool: string, input: unknown): string {
  const i = input !== null && typeof input === 'object' ? (input as Record<string, unknown>) : {}
  if (tool === 'Bash') {
    const command = str(i.command)
    if (!/(^|[\s;&|(])runlease\s+up(\s|$)/.test(command)) return ''
    const named = /\s(?:-n|--name)[=\s]+["']?([A-Za-z0-9][A-Za-z0-9._-]*)/.exec(command)
    return named?.[1] ?? ''
  }
  if (/^mcp__.*__runlease_up$/.test(tool)) return str(i.name)
  return ''
}

/** One line a machine can be summed up in, under the tool call that launched it. */
export function receipt(machine: Machine, at: number): string {
  return [machine.name, [machine.provider, machine.region].filter(Boolean).join(' '), machine.instanceType, leaseText(machine, at), rate(machine)]
    .filter(Boolean)
    .join(' · ')
}

export function cut(s: string, max: number): string {
  if (max <= 1) return ''
  return s.length <= max ? s : s.slice(0, max - 1) + '…'
}

export function firstLine(s: string): string {
  return (s.trim().split('\n')[0] ?? '').trim()
}
