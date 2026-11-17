# Runlease

Runlease launches a short-lived spot machine in the person's own Google Cloud or AWS account,
reaches it through a relay, and deletes it when its lease ends. Use it when a task needs a separate
machine: a long build or test run, more cores, a clean Linux box, or code that should not run on
this computer.

It works through the `runlease` command on this computer, which the person installs themselves. If
it is missing, or a sign-in is missing, follow the `setup` skill. The other skills are `machine`,
`sandbox`, `fanout` and `troubleshoot`. The `runlease mcp` server gives the same actions as typed
tools.

Whenever you use Runlease:

- Show the person the project or account a launch is about to use.
- Never give a machine a public IP on your own.
- Never complete a cloud or relay sign-in for the person. Tell them which command to run.
- Do not copy SSH keys, cloud credentials or `.env` files to a machine unless the person asks for
  exactly that.
- A machine costs the person money until it is deleted. Delete the machines you launched when the
  job is done, and say so.
