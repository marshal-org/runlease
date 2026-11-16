import type { PluginOptions } from 'claude-code'

export type Mode = 'auto' | 'command' | 'off'

export type Config = {
  pane: Mode
  pollMs: number
  /** A lease with less than this left is "about to end". */
  warnMs: number
  notify: boolean
}

function mode(v: unknown, fallback: Mode): Mode {
  return v === 'auto' || v === 'command' || v === 'off' ? v : fallback
}

function number(v: unknown, fallback: number, floor: number): number {
  const n = Number(v ?? fallback)
  return Math.max(floor, Number.isFinite(n) ? n : fallback)
}

export function readConfig(given?: PluginOptions): Config {
  const options = given ?? {}
  return {
    pane: mode(options.pane, 'auto'),
    // Never faster than every 10 seconds: each read starts the CLI once.
    pollMs: number(options.pollSeconds, 30, 10) * 1000,
    warnMs: number(options.warnMinutes, 10, 1) * 60 * 1000,
    notify: options.notify !== false,
  }
}
