export type Machine = {
  name: string
  provider: string
  region: string
  state: string
  /** Milliseconds since the epoch when the lease ends; null with no lease. */
  expiresAt: number | null
  usdPerHour: number | null
}

declare module 'claude-code' {
  interface PluginState {
    runlease: {
      machines: Shaped<Machine[]>
      /** When the list was last read, in milliseconds since the epoch. */
      now: number
      /** False once `runlease` could not be run at all. */
      isInstalled: boolean
      /** The band was dismissed for this machine and deadline. */
      dismissed: string | null
      /** The machine whose delete button was pressed once and awaits a second press. */
      confirming: string | null
    }
  }
}
