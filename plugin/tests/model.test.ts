import { expect, test } from 'claude-code/testing'

import { readConfig } from '../hooks/config'
import {
  barCells,
  burn,
  changes,
  extendArg,
  isRunleaseCommand,
  isRunleaseTool,
  launchedName,
  leaseText,
  money,
  perHour,
  nextToEnd,
  parseList,
  receipt,
  soonest,
  span,
  spent,
  statusLine,
  tone,
} from '../hooks/model'
import { LIST, NOW } from './fixtures'

const WARN = 10 * 60000

test('the list is read soonest lease first, and bad output is an empty list', () => {
  const list = parseList(LIST)
  expect(list.map(m => m.name)).toEqual(['box', 'forever'])
  expect(list[0]?.expiresAt).toBe(NOW + 7 * 60000)
  expect(list[0]?.instanceType).toBe('e2-standard-8')
  expect(list[1]?.expiresAt).toBe(null)
  expect(parseList('not json').length).toBe(0)
  expect(parseList('{"a":1}').length).toBe(0)
  expect(parseList('[{"provider":"gcp"}, null, 3]').length).toBe(0)
})

test('time reads as a person would say it', () => {
  expect(span(45000)).toBe('45s')
  expect(span(7 * 60000)).toBe('7m')
  expect(span(120 * 60000)).toBe('2h')
  expect(span(192 * 60000)).toBe('3h 12m')
  expect(span(52 * 3600000)).toBe('2d 4h')
  const [box, forever] = parseList(LIST)
  expect(leaseText(box!, NOW)).toBe('7m left')
  expect(leaseText(box!, NOW + 8 * 60000)).toBe('lease ended')
  expect(leaseText(forever!, NOW)).toBe('no lease')
})

test('a lease is ok, about to end, or over', () => {
  const [box, forever] = parseList(LIST)
  expect(tone(box!, NOW - 60 * 60000, WARN)).toBe('ok')
  expect(tone(box!, NOW, WARN)).toBe('soon')
  expect(tone(box!, NOW + 7 * 60000, WARN)).toBe('ended')
  expect(tone(forever!, NOW, WARN)).toBe('none')
})

test('the bar drains with the lease and is never empty while time is left', () => {
  const [box, forever] = parseList(LIST)
  // Launched 53 minutes ago on a one hour lease: 7 of 60 minutes left.
  expect(barCells(box!, NOW, 20)).toEqual({ full: 2, empty: 18 })
  expect(barCells(box!, NOW + 6.9 * 60000, 20)).toEqual({ full: 1, empty: 19 })
  expect(barCells(box!, NOW + 8 * 60000, 20)).toEqual({ full: 0, empty: 20 })
  expect(barCells(forever!, NOW, 20)).toBe(null)
})

test('cost so far is the rate over the time since launch', () => {
  const [box] = parseList(LIST)
  expect(spent(box!, NOW)?.toFixed(4)).toBe(((53 / 60) * 0.15).toFixed(4))
  // It stops at the end of the lease, and an ended machine leaves the hourly total.
  expect(spent(box!, NOW + 3600000)?.toFixed(4)).toBe((0.15).toFixed(4))
  expect(burn([box!], NOW)).toBe(0.15)
  expect(burn([box!], NOW + 3600000)).toBe(null)
  expect(nextToEnd([box!], NOW + 3600000)).toBe(null)
})

test('a cheap machine never reads as free', () => {
  // A small spot machine is a fraction of a cent an hour: two decimals would print $0.00.
  expect(perHour(0.0004)).toBe('under $0.001/h')
  expect(perHour(0.0022)).toBe('$0.002/h')
  expect(perHour(0.15)).toBe('$0.15/h')
  expect(perHour(1.5)).toBe('$1.50/h')
  expect(money(0.0005)).toBe('under $0.01')
  expect(money(0)).toBe('$0.00')
  expect(money(0.13)).toBe('$0.13')
  const [cheap] = parseList(JSON.stringify([{ name: 'tiny', provider: 'aws', state: 'running', usdPerHour: 0.0022, createdAt: '2026-11-16T09:55:00Z', expiresAt: '2026-11-16T10:30:00Z' }]))
  expect(statusLine({ machines: [cheap!], readAt: NOW, isInstalled: true })).toBe('1 machine · tiny 30m left · $0.002/h')
})

test('the status line names the lease that ends first, and clears with none', () => {
  const machines = parseList(LIST)
  expect(soonest(machines)?.name).toBe('box')
  expect(statusLine({ machines, readAt: NOW, isInstalled: true })).toBe('2 machines · box 7m left · $0.15/h')
  expect(statusLine({ machines: [], readAt: NOW, isInstalled: true })).toBe(undefined)
})

test('changes: a new machine, a lease about to end once, a machine gone', () => {
  const machines = parseList(LIST)
  const empty = { machines: [], readAt: NOW - 60000, isInstalled: true }
  const now = { machines, readAt: NOW, isInstalled: true }
  // The first read reports nothing.
  expect(changes({ machines: [], readAt: 0, isInstalled: true }, now, WARN).length).toBe(0)
  expect(changes(empty, now, WARN).map(c => c.kind)).toEqual(['up', 'up'])

  const before = { machines, readAt: NOW - 5 * 60000, isInstalled: true }
  const soon = changes(before, now, WARN)
  expect(soon.map(c => c.kind)).toEqual(['soon'])
  expect(soon[0]?.text).toBe('box: 7m left, then it deletes itself')
  // Already inside the window on both reads: said once, not again.
  expect(changes(now, { ...now, readAt: NOW + 60000 }, WARN).length).toBe(0)

  const later = { machines: machines.filter(m => m.name !== 'box'), readAt: NOW + 8 * 60000, isInstalled: true }
  expect(changes(now, later, WARN)[0]?.text).toBe('box deleted itself at the end of its lease')
  const early = { ...later, readAt: NOW + 60000 }
  expect(changes(now, early, WARN)[0]?.text).toBe('box is gone')
})

test('adding time asks for what is left plus the addition', () => {
  const [box, forever] = parseList(LIST)
  // snooze sets the deadline from now: 7 minutes left + 60 added.
  expect(extendArg(box!, NOW, 60 * 60000)).toBe('67m')
  expect(extendArg(box!, NOW + 30 * 60000, 30 * 60000)).toBe('30m')
  expect(extendArg(forever!, NOW, 30 * 60000)).toBe('30m')
})

test('runlease commands and tools are told from the rest', () => {
  expect(isRunleaseCommand('runlease up -n box')).toBe(true)
  expect(isRunleaseCommand('cd x && runlease list')).toBe(true)
  expect(isRunleaseCommand('cat runlease.md')).toBe(false)
  expect(isRunleaseTool('mcp__plugin_runlease_runlease__runlease_up')).toBe(true)
  expect(isRunleaseTool('mcp__other__list')).toBe(false)
  expect(launchedName('Bash', { command: 'runlease up -p gcp -n box --ttl 1h -d' })).toBe('box')
  expect(launchedName('Bash', { command: 'runlease up --name=box' })).toBe('box')
  expect(launchedName('Bash', { command: 'runlease list' })).toBe('')
  expect(launchedName('mcp__runlease__runlease_up', { name: 'box' })).toBe('box')
  expect(launchedName('Read', { file_path: 'x' })).toBe('')
  expect(receipt(parseList(LIST)[0]!, NOW)).toBe('box · gcp us-central1-a · e2-standard-8 · 7m left · $0.150/h')
})

test('settings fall back to safe values', () => {
  expect(readConfig({})).toEqual({ pane: 'auto', pollMs: 30000, warnMs: 600000, notify: true })
  expect(readConfig({ pane: 'nonsense', pollSeconds: 1, warnMinutes: 'x', notify: false })).toEqual({
    pane: 'auto',
    pollMs: 10000,
    warnMs: 600000,
    notify: false,
  })
})
