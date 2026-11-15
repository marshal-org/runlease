import { expect, test } from 'claude-code/testing'

import { left, parseList, soonest, statusLine } from '../hooks/register'

const NOW = Date.parse('2026-11-16T10:00:00Z')

const LIST = JSON.stringify([
  {
    name: 'box',
    provider: 'gcp',
    region: 'us-central1-a',
    state: 'running',
    expiresAt: '2026-11-16T10:07:00Z',
    usdPerHour: 0.026,
  },
  { name: 'forever', provider: 'aws', region: 'eu-north-1', state: 'running' },
])

const PANE = {
  plugin: 'runlease',
  component: 'Pane',
  requestId: 'runlease',
  props: {
    title: 'Runlease',
    isFocused: false,
    bodyColumns: 60,
    placement: 'dock',
    scroll: { offset: 0, bodyRows: 20 },
    view: {},
  },
} as const

test('the list is read, and bad output is an empty list', () => {
  const list = parseList(LIST)
  expect(list.length).toBe(2)
  expect(list[0]?.expiresAt).toBe(NOW + 7 * 60000)
  expect(list[1]?.expiresAt).toBe(null)
  expect(parseList('not json').length).toBe(0)
  expect(parseList('{"a":1}').length).toBe(0)
})

test('time left reads as a person would say it', () => {
  const [box, forever] = parseList(LIST)
  expect(left(box!, NOW)).toBe('7m left')
  expect(left(box!, NOW + 8 * 60000)).toBe('lease ended')
  expect(left({ ...box!, expiresAt: NOW + 125 * 60000 }, NOW)).toBe('2h5m left')
  expect(left(forever!, NOW)).toBe('no lease')
})

test('the status line names the lease that ends first, and clears with none', () => {
  const list = parseList(LIST)
  expect(soonest(list)?.name).toBe('box')
  expect(statusLine(list, NOW)).toBe('runlease: 2 machines · box 7m left')
  expect(statusLine([], NOW)).toBe(undefined)
})

test('the pane lists machines and deleting takes two presses', async ($, on) => {
  const ran: string[][] = []
  on('command.register', () => ({ value: { command: 'runlease' } }))
  on('ui.open', () => ({ value: { isPlaced: true } }))
  on('ui.status', () => ({ value: undefined }))
  on('clock.now', () => ({ value: NOW }))
  on('clock.every', () => ({ value: undefined }))
  on('session.start', (_, e) => ({ cwd: e.cwd }))
  on('process.run', (_, e) => {
    ran.push([...e.argv])
    return {
      value: {
        exitCode: 0,
        stdout: LIST,
        stderr: '',
        isStdoutTruncated: false,
        isStderrTruncated: false,
      },
    }
  })

  await $.session.start({ cwd: '/', surface: 'terminal', isInteractive: true })
  await $.command.run({
    command: 'runlease',
    args: '',
    origin: { kind: 'composer' },
    presentation: { isFullscreen: false, columns: 120 },
  })

  for (const surface of ['terminal', 'desktop'] as const) {
    const ui = await $.ui.mount({ ...PANE, surface })
    expect(await ui.find({ type: 'Text', text: /box/ })).toBeDefined()
    expect(await ui.find({ type: 'Text', text: /7m left/ })).toBeDefined()

    const before = ran.filter(argv => argv[1] === 'down').length
    await ui.press({ key: 'down-box' })
    expect(ran.filter(argv => argv[1] === 'down').length).toBe(before)
    await ui.press({ key: 'down-box' })
    const downs = ran.filter(argv => argv[1] === 'down')
    expect(downs.length).toBe(before + 1)
    expect(downs.at(-1)?.join(' ')).toBe('runlease down box --yes')
    await ui.unmount()
  }
})

test('dismissing one lease warning does not hide another', async ($, on) => {
  const two = JSON.stringify([
    { name: 'a', state: 'running', expiresAt: '2026-11-16T10:03:00Z' },
    { name: 'b', state: 'running', expiresAt: '2026-11-16T10:06:00Z' },
  ])
  on('command.register', () => ({ value: { command: 'runlease' } }))
  on('ui.open', () => ({ value: { isPlaced: true } }))
  on('ui.status', () => ({ value: undefined }))
  on('clock.now', () => ({ value: NOW }))
  on('clock.every', () => ({ value: undefined }))
  on('session.start', (_, e) => ({ cwd: e.cwd }))
  on('process.run', () => ({
    value: {
      exitCode: 0,
      stdout: two,
      stderr: '',
      isStdoutTruncated: false,
      isStderrTruncated: false,
    },
  }))

  await $.session.start({ cwd: '/', surface: 'terminal', isInteractive: true })
  await $.command.run({
    command: 'runlease',
    args: '',
    origin: { kind: 'composer' },
    presentation: { isFullscreen: false, columns: 120 },
  })

  const BAND = {
    plugin: 'runlease',
    component: 'AbovePrompt',
    surface: 'terminal',
    props: {
      hasSurvey: false,
      isCollapsed: false,
      isWorking: false,
      maxRows: 4,
      bodyColumns: 100,
      scroll: { offset: 0, bodyRows: 4 },
      view: {},
    },
  } as const
  let ui = await $.ui.mount(BAND)
  expect(await ui.find({ type: 'Text', text: /a 3m left/ })).toBeDefined()
  await ui.press({ key: 'dismiss' })
  await ui.unmount()

  ui = await $.ui.mount(BAND)
  expect(await ui.find({ type: 'Text', text: /b 6m left/ })).toBeDefined()
  await ui.unmount()
})
