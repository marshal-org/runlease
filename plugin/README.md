# Runlease for Claude Code

**Give Claude a real machine when the task outgrows your laptop.**

A two-hour build. An install script you would not run on your own computer. A test suite split
across eight machines. With this plugin Claude Code launches a Linux VM in your own cloud
account, does the work there, and gives the machine back. You see every machine, its lease and
its cost in a pane beside the chat.

- **Your cloud, at spot prices.** The machines run in your own Google Cloud or AWS account.
  `runlease up` prints the hourly rate and the cost of the lease before it launches.
- **Nothing left running.** Every machine has a lease and deletes itself when it ends. The timer
  is on the VM, so it holds even if your session crashes.
- **No open ports.** Machines are reached through a relay that passes encrypted traffic and
  cannot read it. They need no inbound firewall rule.
- **Claude stays on your computer.** Your conversation and your credentials do not move to the
  machine.

Built by the [Marshal](https://marshal.codes) team. [Runlease](https://runlease.online) is open
source under Apache-2.0.

## Install

You need the `runlease` CLI first:

```bash
curl -fsSL https://runlease.online/install.sh | sh
runlease login     # the free public relay
runlease doctor    # checks sign-ins, project and network, and prints the fix for what is wrong
```

Then, at the prompt of a Claude Code session:

```
/plugin install runlease --marketplace marshal-org/runlease
```

## What it adds

| Part | What it is |
|---|---|
| Skills | `setup`, `machine`, `sandbox`, `fanout`, `troubleshoot`. Claude reads them when the task fits |
| Slash commands | `/runlease:up`, `/runlease:status`, `/runlease:down`, `/runlease:doctor`, and `/runlease` for the pane |
| Typed tools | The `runlease mcp` server: `runlease_up`, `runlease_exec`, `runlease_push`, `runlease_pull`, `runlease_list`, `runlease_snooze`, `runlease_down`, `runlease_status`, `runlease_doctor` |
| A live view | A machines pane, a status line entry, a band above the prompt when a lease is about to end, and toasts |

### The live view

The pane (`/runlease`) lists each machine with its cloud, region and size, the time left on its
lease as a bar that drains, its hourly rate and what it has cost so far. Open a row to add time
to the lease, copy its `ssh` command, start a prompt about it, or delete it. Delete takes two
presses.

```
Runlease                                         1 machine · $0.15/h
────────────────────────────────────────────────────────────────────
● box ▾                                                     41m left
  gcp · europe-west1-b · e2-standard-8
  ━━━━━━━━━━━━━━━━────────  $0.15/h · $0.05 so far
  Lease  +30m  +1h  +4h
  Use in prompt  Copy ssh  Delete
────────────────────────────────────────────────────────────────────
Refresh  Launch another  Docs
Each machine deletes itself when its lease ends.
```

When a lease has less than ten minutes left, a band appears above the prompt:

```
● box 7m left, then it deletes itself   +30m  +1h  Dismiss
```

The view reads `runlease list --local`, which makes no cloud call. Nothing is extended or
deleted unless you press a button.

## What the skills hold Claude to

- It shows you the project or account a launch is about to use.
- It never gives a machine a public IP on its own.
- It never completes a cloud or relay sign-in for you. It tells you which command to run.
- It does not copy SSH keys, cloud credentials or `.env` files to a machine unless you ask for
  exactly that.
- It deletes the machines it launched when the job is done, and says so.

## What it runs, sends and fetches

Everything goes through the `runlease` CLI on your computer. The plugin holds no credentials of
its own, sends no telemetry and makes no network call itself.

- **Runs:** two programs. `node`, on `mcp/launch.js`, a file in this plugin whose only job is to
  start `runlease mcp`, the server behind the typed tools. And `runlease`, the CLI you installed,
  with exactly these commands:

  | Command | When | Why |
  |---|---|---|
  | `runlease list --local --output json` | every 30 seconds, and after a Runlease command | reads the machine list from a file on your computer |
  | `runlease snooze <machine> <time>` | you press `+30m`, `+1h` or `+4h` | adds time to that lease |
  | `runlease down <machine> --yes` | you press Delete twice | deletes that machine |

- **In your cloud:** when Claude or you launch a machine, the CLI calls the Google Cloud or AWS
  API with the sign-in you already have, and creates a spot VM in your account, on your bill. It
  deletes the VM when the lease ends or when you say so.
- **Sends:** commands and files you or Claude send to a machine travel over SSH, through the
  relay you signed in to (`connect.runlease.online` by default, or your own). The relay passes
  encrypted traffic and cannot read it.
- **Fetches:** nothing. Installing the CLI is a step you run yourself.
- **Shows you, on your own computer:** a desktop notification when a lease is about to end
  (the machine's name and the time left), and toasts. `Copy ssh` puts `runlease ssh <machine>`
  on your clipboard, and `Use in prompt` writes the start of a prompt in the prompt box for you
  to finish and send. None of this leaves your computer.
- **Reads:** `runlease list --local` every 30 seconds by default, which reads a file on your
  computer and makes no cloud call.

### What each hook does

| Hook | What it does |
|---|---|
| `session.start` | registers the `/runlease` command, reads the machine list once and starts the 30-second refresh |
| `command.run` (`/runlease`) | opens the pane |
| `tool.call` | lets every call through unchanged. After a shell command that names `runlease`, or one of the typed tools, it reads the machine list again. It never blocks, changes or records a call |
| `session.end` | if this session used Runlease and machines are still running, names them in a toast |
| `ui.render` | draws the pane, the band above the prompt, and one line under the tool call that launched a machine |

The plugin calls no other plugin and needs none.

## Settings

`/config`, under the plugin: when the pane opens (`auto`, `command`, `off`), how many minutes
before a lease ends to warn, how often to refresh, and whether to raise a desktop notification.

## Developing

```bash
claude plugin validate plugin
claude plugin test plugin
claude --plugin-dir ./plugin     # a session that reloads the plugin as you edit
```

Full reference: [docs/claude-plugin.md](../docs/claude-plugin.md). Issues:
<https://github.com/marshal-org/runlease/issues>. Security reports: [SECURITY.md](../SECURITY.md). License: [Apache-2.0](LICENSE).
