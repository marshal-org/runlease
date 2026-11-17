# The Claude Code plugin

The plugin gives Claude Code a real machine when a task outgrows your laptop: a long build, code
you would not run locally, a test suite split across machines. Claude launches a spot VM in your
own cloud account, works there, and gives it back, and you see each machine, its lease and its
cost while it works. It is built by the [Marshal](https://marshal.codes) team and lives in
[`plugin/`](../plugin) in this repository.

## Install

You need the `runlease` CLI first: see the [install page](https://runlease.online/install.html).
Then, at the prompt of a Claude Code terminal session:

```
/plugin install runlease --marketplace marshal-org/runlease
```

Or from a shell:

```bash
claude plugin install runlease --marketplace marshal-org/runlease
```

To try it from a clone without installing: `claude --plugin-dir ./plugin`.

## What you get

**Skills.** Claude reads these when the task fits; you do not call them.

| Skill | When Claude uses it |
|---|---|
| `runlease:setup` | Runlease is not installed, or a sign-in, project, region or relay is missing |
| `runlease:machine` | A task needs a separate machine: a long build, more cores, a clean Linux box, a port to preview |
| `runlease:sandbox` | Code that should not run on your computer: generated code, an unfamiliar repository, an install script |
| `runlease:fanout` | A job that splits across several machines: test shards, a version matrix |
| `runlease:troubleshoot` | A command fails, a machine never connects, or machines may have been left running |

**Slash commands.**

| Command | Does |
|---|---|
| `/runlease:up [gcp\|aws] [name] [lease]` | Launches one machine and reports how to use it |
| `/runlease:status` | Machines, time left on each lease, and the relay sign-in |
| `/runlease:down [name \| --all]` | Deletes a machine now |
| `/runlease:doctor` | Checks sign-ins, project, network and relay, and walks through the fixes |
| `/runlease` | Opens the machines pane |

**Typed tools.** The plugin registers `runlease mcp` as an MCP server, so Claude gets
`runlease_up`, `runlease_exec`, `runlease_push`, `runlease_pull`, `runlease_list`,
`runlease_snooze`, `runlease_down`, `runlease_status` and `runlease_doctor`. You can then allow
"run a command on a leased machine" without allowing every shell command. See [mcp.md](mcp.md).

**A live view.**

- A status line entry: `runlease: 2 machines · box 41m left · $0.30/h`.
- A pane (`/runlease`). Each machine has its cloud, region and size, the time left on its lease
  as a bar that drains, its hourly rate and what it has cost so far. Open a row to add 30
  minutes, 1 hour or 4 hours to the lease, copy its `ssh` command, start a prompt about it, or
  delete it. Delete takes two presses.
- A band above the prompt when a lease is about to end, with **+30m**, **+1h** and **Dismiss**.
- A toast when a machine comes up, when one is gone, and when a lease is about to end. The last
  one also raises a desktop notification.
- One line under the call that launched a machine, naming what was launched.
- A note when the session ends with machines still running.

The view reads `runlease list --local` every 30 seconds. That command makes no cloud call.
Nothing is extended or deleted unless you press a button.

**Settings** (`/config`, under the plugin).

| Setting | Default | Does |
|---|---|---|
| Runlease pane | `auto` | `auto` opens the pane when the session launches a machine, on a wide terminal. `command` opens it only with `/runlease`. `off` turns it off |
| Warn before a lease ends | 10 minutes | When the band, the toast and the notification appear |
| Refresh every | 30 seconds | How often the list is read. Never faster than every 10 seconds |
| Notify before a lease ends | on | The desktop notification |

**What it runs and reads.** The plugin runs the `runlease` CLI on your computer with the cloud
sign-ins you already have. It holds no credentials, sends no telemetry and makes no network call
itself.

To work on the plugin: `claude plugin validate plugin`, `claude plugin test plugin`, and
`claude --plugin-dir ./plugin` for a session that reloads it as you edit.

## What the skills hold Claude to

- It shows you the project or account `runlease up` is about to use.
- It never adds `--public-ip` on its own. If your subnet has no NAT gateway, it shows you the
  choices and waits.
- It never completes a cloud or relay sign-in for you. It tells you which command to run.
- It does not copy SSH keys, cloud credentials or `.env` files to a machine unless you ask for
  exactly that.
- It deletes the machines it launched when the job is done, and says so.

## What it costs

The plugin is free and so is the public relay. The machines run in your cloud account, on your
bill, at spot prices. `runlease up` prints the hourly rate and the cost of the lease before it
launches.

## The plugin directory edition

`claude-directory/` is the edition submitted to the Claude plugin directory: the same skills
and slash commands, with no MCP server, no live view and no code file. The directory takes
those only from an organization it has verified. Its skills and commands are copies:
edit `plugin/`, then run `scripts/gen-agent-plugins.py`. Its `plugin.json` and `README.md` are
written by hand, and the script's `--check` fails if its version differs from `plugin/`.

```bash
claude plugin validate claude-directory --profile directory --strict
```
