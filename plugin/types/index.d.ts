export type Machine = {
  name: string
  provider: string
  region: string
  /** The cloud's own size name, such as e2-standard-8; empty when unknown. */
  instanceType: string
  state: string
  /** Milliseconds since the epoch when the machine was launched; null when unknown. */
  createdAt: number | null
  /** Milliseconds since the epoch when the lease ends; null with no lease. */
  expiresAt: number | null
  usdPerHour: number | null
  /** True when the hourly rate is an estimate. */
  isPriceApprox: boolean
}

/** What the pane, the band and the status line draw: one read of `runlease list --local`. */
export type Board = {
  machines: Machine[]
  /** Milliseconds since the epoch of the last read; 0 before the first. */
  readAt: number
  /** False once `runlease` could not be started at all. */
  isInstalled: boolean
}

/** The machine a button is acting on, and what it is doing, while the command runs. */
export type Busy = { name: string; action: 'extend' | 'delete' } | null

declare module 'claude-code' {
  interface PluginState {
    runlease: {
      board: Shaped<Board>
      /** The machine whose row is open in the pane; null opens the only one. */
      selected: string | null
      /** The machine whose delete button was pressed once and awaits a second press. */
      confirming: string | null
      busy: Busy
      /** Machine-and-deadline keys the band was dismissed for. */
      dismissed: Shaped<string[]>
    }
  }
}
