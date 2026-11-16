import type { Register } from 'claude-code'

import { launched, renderBand, warned } from './band'
import { readConfig } from './config'
import { count, isRunleaseCommand, isRunleaseTool, launchedName } from './model'
import { renderPane } from './pane'
import { mod, PANE, PANE_TITLE, refresh, type Kit } from './state'

// A live view of the Runlease machines this computer tracks: a status line, a
// pane with each lease and its cost, a band when a lease is about to end, and
// one line under the tool call that launched a machine. Reading is always
// `runlease list --local`, which makes no cloud call. A machine is changed
// only when the person presses a button.

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
