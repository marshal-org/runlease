// Rows as `runlease list --local --output json` prints them (cmd/runlease/list.go,
// listJSONEntry), trimmed to the fields the plugin reads.

export const NOW = Date.parse('2026-11-16T10:00:00Z')

export const BOX = {
  name: 'box',
  id: 'rl-box-1',
  provider: 'gcp',
  instanceType: 'e2-standard-8',
  region: 'us-central1-a',
  state: 'running',
  createdAt: '2026-11-16T09:07:00Z',
  expiresAt: '2026-11-16T10:07:00Z',
  usdPerHour: 0.15,
}

export const FOREVER = { name: 'forever', provider: 'aws', region: 'eu-north-1', state: 'running' }

export const LIST = JSON.stringify([FOREVER, BOX])
