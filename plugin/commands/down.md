---
description: Delete a Runlease machine now
argument-hint: "[name | --all]"
---

Delete Runlease machines. Argument: `$ARGUMENTS`.

- A name: run `runlease down NAME --yes` and confirm it is gone with `runlease list`.
- `--all`: list the machines first, name each one in your reply, then delete them one by one.
- No argument: run `runlease list`. With exactly one machine, say which and delete it. With several,
  ask which.

Only delete machines tracked on this computer. For a machine `list` marks as untracked, ask first.
