# Runlease for Claude Code

**Give Claude a real machine when the task outgrows your laptop.**

A two-hour build. An install script you would not run on your own computer. A test suite split
across eight machines. With this plugin Claude Code launches a Linux VM in your own cloud
account, does the work there, and gives the machine back.

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

## Before you start

The plugin drives the `runlease` command, which you install yourself:
the one-line installer is at the top of
<https://runlease.online>. Then run `runlease login` and `runlease doctor` once.
`doctor` checks your sign-ins, project and network, and prints the fix for what is wrong.

## What it adds

| Part | What it is |
|---|---|
| Skills | `setup`, `machine`, `sandbox`, `fanout`, `troubleshoot`. Claude reads them when the task fits |
| Slash commands | `/runlease:up`, `/runlease:status`, `/runlease:down`, `/runlease:doctor` |

This edition is skills and commands only: Markdown, with no program, hook or server inside it.

## What the skills hold Claude to

- It shows you the project or account a launch is about to use.
- It never gives a machine a public IP on its own.
- It never completes a cloud or relay sign-in for you. It tells you which command to run.
- It does not copy SSH keys, cloud credentials or `.env` files to a machine unless you ask for
  exactly that.
- It deletes the machines it launched when the job is done, and says so.

## What it runs, sends and fetches

The plugin holds no credentials, sends no telemetry and makes no network call. Its skills tell
Claude to run the `runlease` command on your computer, through Claude Code's own Bash tool and
its permission prompts.

- **Runs:** `runlease` subcommands such as `up`, `exec`, `push`, `pull`, `list`, `snooze`,
  `down` and `doctor`.
- **In your cloud:** a launch calls the Google Cloud or AWS API with the sign-in you already
  have, and creates a spot VM in your account, on your bill. The VM is deleted when the lease
  ends or when you say so.
- **Sends:** commands and files you or Claude send to a machine travel over SSH, through the
  relay you signed in to (`connect.runlease.online` by default, or your own). The relay passes
  encrypted traffic and cannot read it.
- **Fetches:** nothing.

## The full plugin

The same repository holds a fuller edition with a live machines pane, a status line entry, a
warning before a lease ends, and typed tools. It is described at
<https://runlease.online/claude-code-sandbox.html> and installs from this repository's own
marketplace:

```
/plugin install runlease --marketplace marshal-org/runlease
```

Issues: <https://github.com/marshal-org/runlease/issues>. License: [Apache-2.0](LICENSE).
