// Starts the Runlease MCP server: the `runlease mcp` command of the runlease
// CLI the person installed. This file holds no server of its own and makes no
// network call. It exists so the plugin starts a file it ships, which then
// starts the installed program.
const { spawn } = require('node:child_process')

const child = spawn('runlease', ['mcp'], { stdio: 'inherit', windowsHide: true })

child.on('error', error => {
  const why = error.code === 'ENOENT' ? 'the runlease command is not installed. See https://runlease.online' : String(error)
  process.stderr.write(`runlease mcp did not start: ${why}\n`)
  process.exit(127)
})
child.on('exit', (code, signal) => process.exit(code ?? (signal ? 1 : 0)))
for (const signal of ['SIGINT', 'SIGTERM']) process.on(signal, () => child.kill(signal))
