# The Claude Code plugin

The plugin teaches Claude Code how to use Runlease, and shows what is running while it works. It
lives in [`plugin/`](../plugin) in this repository.

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

- A status line entry: `runlease: 2 machines · box 41m left`.
- A band above the prompt when a lease has under ten minutes left, with **Snooze 1h** and
  **Dismiss**.
- A pane (`/runlease`) listing each machine with its cloud, state, time left and hourly rate, and
  **Snooze 1h** and **Delete** buttons. Delete takes two presses.
- A note when the session ends with machines still running.

The view reads `runlease list --local` once a minute. That command makes no cloud call. Nothing is
snoozed or deleted unless you press a button.

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
