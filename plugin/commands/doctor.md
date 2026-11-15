---
description: Check Runlease sign-ins, project, network and relay, and fix what can be fixed
---

Run `runlease doctor` and act on the result using the `runlease:troubleshoot` skill.

For each failing check: if the fix is a sign-in, ask the person to run the command it names (they
must do it themselves, with `! <command>`). If the fix is a setting you can change safely, such as
the default region, propose it and wait for a yes. Rerun `runlease doctor` at the end and report
what is now passing.
