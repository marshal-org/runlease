# Antigravity and Gemini CLI

The skills, commands and typed tools of the [Claude Code plugin](claude-plugin.md) also install
in Google's coding agents: the Antigravity CLI (`agy`) and app, as a plugin, and the Gemini CLI,
as an extension. Both install straight from this repository.

Google replaced the Gemini CLI with the Antigravity CLI for free and Google One accounts on
June 18, 2026. If you are not on a paid or enterprise Gemini CLI licence, use Antigravity.

## Install

You need the `runlease` CLI first, on your `PATH`: see the
[install page](https://runlease.online/install.html). Neither plugin bundles or downloads it.

```bash
curl -fsSL https://runlease.online/install.sh | sh
runlease login     # the free public relay
runlease doctor    # checks sign-ins, project and network, and prints the fix for what is wrong
```

Antigravity:

```bash
agy plugin install https://github.com/marshal-org/runlease
```

Gemini CLI:

```bash
gemini extensions install https://github.com/marshal-org/runlease
```

Both commands clone this whole repository, because each format wants its manifest at the root
of the repository it installs from.

## What you get

| Part | Antigravity | Gemini CLI |
|---|---|---|
| Skills | `setup`, `machine`, `sandbox`, `fanout`, `troubleshoot` | The same five |
| Commands | `/runlease-up`, `/runlease-status`, `/runlease-down`, `/runlease-doctor`. Antigravity plugins have no commands, so these are skills that run only when you call them | `/runlease:up`, `/runlease:status`, `/runlease:down`, `/runlease:doctor` |
| Typed tools | The `runlease mcp` server, started as `runlease mcp` | The same server |
| Standing instructions | [`rules/AGENTS.md`](../rules/AGENTS.md), always on | The same file, as the extension's context file |

The skills are the same text Claude Code gets, and they hold the agent to the same rules: show
the project or account before a launch, never add a public IP or complete a sign-in on its own,
keep credentials off the machine, and delete what it launched.

The typed tools are listed in [mcp.md](mcp.md). The agent may show them with a prefix: Antigravity
puts the plugin and server name in front of each tool.

## What you do not get

The live view is built on Claude Code's plugin interface and exists only there: the machines pane,
the status line entry, the band above the prompt when a lease is about to end, the toasts and
desktop notification, and the plugin settings. In Antigravity and the Gemini CLI, ask the agent
for the state of your machines, run the status command above, or run `runlease list` yourself.

## How the files are laid out

`plugin/` is the Claude Code plugin and the only place to edit skills and commands. The Google
formats cannot point at it, so the rest is generated:

| Path | For | Source |
|---|---|---|
| `.antigravity-plugin/plugin.json`, `mcp_config.json` | Antigravity manifest and MCP server | Written by hand |
| `gemini-extension.json` | Gemini CLI manifest and MCP server | Written by hand |
| `rules/AGENTS.md` | Both | Written by hand |
| `skills/` | Both | Copy of `plugin/skills/` |
| `antigravity/skills/` | Antigravity | One skill per file in `plugin/commands/` |
| `commands/runlease/*.toml` | Gemini CLI | One command per file in `plugin/commands/` |

After changing anything under `plugin/skills/` or `plugin/commands/`:

```bash
scripts/gen-agent-plugins.py           # rewrite the generated files
scripts/gen-agent-plugins.py --check   # exit 1 if they are out of date, or if the manifests
                                       # disagree with plugin/ on version or MCP server
agy plugin validate .
gemini extensions validate .
```

`agy plugin validate .` reports one warning, that `commands/` is never loaded. That directory
is the Gemini CLI's, and the warning is expected.
