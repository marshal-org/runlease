import type { On } from 'claude-code'
import { expect, mock, test } from 'claude-code/testing'

import { BOX, FOREVER, LIST, NOW } from './fixtures'

const PANE = {
  plugin: 'runlease',
  component: 'Pane',
  requestId: 'runlease',
  props: { title: 'Runlease', isFocused: false, bodyColumns: 60, placement: 'dock', scroll: { offset: 0, bodyRows: 40 }, view: {} },
} as const

const BAND = {
  plugin: 'runlease',
  component: 'AbovePrompt',
  requestId: 'band',
  props: { hasSurvey: false, isCollapsed: false, isWorking: false, maxRows: 10, bodyColumns: 80, scroll: { offset: 0, bodyRows: 10 }, view: {} },
} as const

type World = { list: string | 'missing'; ran: string[][]; status: (string | undefined)[]; toasts: string[]; notes: string[]; copied: string[]; filled: string[]; opened: number }

// The engine beneath the plugin: the CLI, the clock, the screen.
function world(on: On, list: string | 'missing' = LIST) {
  const w: World = { list, ran: [], status: [], toasts: [], notes: [], copied: [], filled: [], opened: 0 }
  const clock = mock.clock(on, { now: NOW })
  on('process.run', (_, e) => {
    w.ran.push([...e.argv])
    if (w.list === 'missing') return { deny: 'spawn runlease ENOENT' }
    return { value: { exitCode: 0, stdout: w.list, stderr: '', isStdoutTruncated: false, isStderrTruncated: false } }
  })
  on('command.register', (_, e) => ({ value: { command: e.name } }))
  on('ui.open', () => {
    w.opened += 1
    return { value: { isPlaced: true } }
  })
  on('ui.status', (_, e) => {
    w.status.push(e.text)
    return { value: undefined }
  })
  on('ui.toast', (_, e) => {
    w.toasts.push(e.text)
    return { value: undefined }
  })
  on('ui.notify', (_, e) => {
    w.notes.push(e.text)
    return { value: { isSent: true, channel: 'terminal_bell' } }
  })
  on('ui.copy', (_, e) => {
    w.copied.push(e.text)
    return { value: { isCopied: true } }
  })
  on('prompt.fill', (_, e) => {
    w.filled.push(e.text)
    return { isFilled: true }
  })
  on('session.start', (_, e) => ({ cwd: e.cwd }))
  return { w, clock }
}

const START = { cwd: '/w', surface: 'terminal', isInteractive: true } as const

test('the pane shows each machine with its lease and cost, on every surface', async ($, on) => {
  const { w, clock } = world(on)
  await $.session.start(START)
  await clock.advance(100)
  expect(w.status.at(-1)).toBe('2 machines · box 7m left · $0.15/h')

  for (const surface of ['terminal', 'desktop'] as const) {
    const ui = await $.ui.mount({ ...PANE, surface })
    expect(await ui.find({ type: 'Text', text: /2 machines · \$0\.15\/h/ })).toBeDefined()
    expect(await ui.find({ type: 'Text', text: /7m left/ })).toBeDefined()
    expect(await ui.find({ type: 'Text', text: /gcp · us-central1-a · e2-standard-8/ })).toBeDefined()
    expect(await ui.find({ type: 'Text', text: /\$0\.150\/h · \$0\.13 so far/ })).toBeDefined()
    expect(await ui.find({ type: 'Text', text: /no lease/ })).toBeDefined()
    await ui.unmount()
  }
})

test('adding time snoozes to what is left plus the addition', async ($, on) => {
  const { w, clock } = world(on, JSON.stringify([BOX]))
  await $.session.start(START)
  await clock.advance(100)
  const ui = await $.ui.mount({ ...PANE, surface: 'terminal' })
  // The only machine's row is open from the start.
  await ui.press({ key: 'add60:box' })
  expect(w.ran.find(argv => argv[1] === 'snooze')).toEqual(['runlease', 'snooze', 'box', '67m'])
  expect(w.toasts.some(t => t === 'box: 1h added to the lease')).toBe(true)
  await ui.unmount()
})

test('deleting takes two presses, and the question goes away by itself', async ($, on) => {
  const { w, clock } = world(on)
  await $.session.start(START)
  await clock.advance(100)
  const ui = await $.ui.mount({ ...PANE, surface: 'terminal' })
  await ui.press({ key: 'row:box' })
  await ui.press({ key: 'down:box' })
  expect(w.ran.some(argv => argv[1] === 'down')).toBe(false)
  expect(await ui.find({ type: 'Button', text: /Press again to delete/ })).toBeDefined()

  // Left unanswered, a later press asks again instead of deleting.
  await clock.advance(7000)
  await ui.press({ key: 'down:box' })
  expect(w.ran.some(argv => argv[1] === 'down')).toBe(false)

  await ui.press({ key: 'down:box' })
  expect(w.ran.find(argv => argv[1] === 'down')).toEqual(['runlease', 'down', 'box', '--yes'])
  await ui.unmount()
})

test('a row offers the ssh command and a prompt about the machine', async ($, on) => {
  const { w, clock } = world(on, JSON.stringify([BOX]))
  await $.session.start(START)
  await clock.advance(100)
  const ui = await $.ui.mount({ ...PANE, surface: 'terminal' })
  await ui.press({ key: 'ssh:box' })
  expect(w.copied).toEqual(['runlease ssh box'])
  await ui.press({ key: 'ask:box' })
  expect(w.filled).toEqual(['On the Runlease machine box: '])
  await ui.unmount()
})

test('with no machines the pane offers to launch one', async ($, on) => {
  const { w, clock } = world(on, '[]')
  await $.session.start(START)
  await clock.advance(100)
  expect(w.status.at(-1)).toBe(undefined)
  const ui = await $.ui.mount({ ...PANE, surface: 'terminal' })
  expect(await ui.find({ type: 'Text', text: /No machines running/ })).toBeDefined()
  await ui.press({ key: 'launch' })
  expect(w.filled).toEqual(['/runlease:up '])
  await ui.unmount()
})

test('without the CLI the pane gives the install line', async ($, on) => {
  const { w, clock } = world(on, 'missing')
  await $.session.start(START)
  await clock.advance(100)
  const ui = await $.ui.mount({ ...PANE, surface: 'terminal' })
  expect(await ui.find({ type: 'Text', text: /runlease\.online\/install\.sh/ })).toBeDefined()
  await ui.press({ key: 'copy-install' })
  expect(w.copied[0]).toBe('curl -fsSL https://runlease.online/install.sh | sh')
  await ui.unmount()
})

test('a lease about to end raises the band, a toast and one notification', async ($, on) => {
  const early = { ...BOX, expiresAt: '2026-11-16T10:12:00Z' }
  const { w, clock } = world(on, JSON.stringify([early, FOREVER]))
  await $.session.start(START)
  await clock.advance(100)
  // 12 minutes left: outside the 10 minute window, so nothing yet.
  expect(w.toasts.length).toBe(0)

  await clock.advance(3 * 60000)
  expect(w.toasts.some(t => /box: \d+m left, then it deletes itself/.test(t))).toBe(true)
  expect(w.notes.length).toBe(1)
  await clock.advance(60000)
  expect(w.notes.length).toBe(1)

  const band = await $.ui.mount({ ...BAND, surface: 'terminal' })
  expect(await band.find({ type: 'Text', text: /then it deletes itself/ })).toBeDefined()
  await band.press({ key: 'band-add60' })
  expect(w.ran.some(argv => argv[1] === 'snooze' && argv[2] === 'box')).toBe(true)
  await band.unmount()
})

test('a machine launched in this session opens the pane and is named under its call', async ($, on) => {
  const { w, clock } = world(on, '[]')
  on('tool.call', () => ({ result: { stdout: '', stderr: '', interrupted: false } }) as never)
  await $.session.start(START)
  await clock.advance(100)
  expect(w.opened).toBe(0)

  w.list = JSON.stringify([BOX])
  // The call's input type is a union of every tool's; a plain function type keeps the checker out of it.
  const call = $.tool.call as unknown as (input: Record<string, unknown>) => Promise<unknown>
  await call({ tool: 'Bash', tool_use_id: 't1', command: 'runlease up -p gcp -n box --ttl 1h -d' })
  await clock.advance(100)
  expect(w.toasts.some(t => t.startsWith('box is up on gcp us-central1-a'))).toBe(true)
  expect(w.opened).toBe(1)

  const row = await $.ui.mount({
    plugin: 'runlease',
    component: 'ToolUse',
    requestId: 't1',
    surface: 'terminal',
    props: { tool_use_id: 't1', tool: 'Bash', input: { command: 'runlease up -p gcp -n box --ttl 1h -d' }, isRunning: false, isErrored: false, isInterrupted: false },
  })
  expect(await row.find({ type: 'Text', text: /Leased: box · gcp us-central1-a · e2-standard-8/ })).toBeDefined()
  await row.unmount()
})
