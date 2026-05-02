You are helping wrap up a development session before committing and pushing. Work through these steps in order.

## Step 1 — Automated checks

Run `make wrap-up` and report the result. If it fails, stop and tell the user what failed. Do not proceed until it passes.

## Step 2 — Understand what changed

Run `git diff main...HEAD --stat` and `git diff main...HEAD` to understand everything that changed this session. Keep this diff in mind for all subsequent steps.

## Step 3 — CHANGELOG.md

Read `CHANGELOG.md`. Draft a new entry for the current session following the exact format of existing entries:
- Version header: `## [0.0.X] — YYYY-MM-DD` (increment patch from the last entry)
- Group changes under `### Added`, `### Changed`, `### Fixed` subsections as appropriate
- Be specific: name the files, endpoints, types, and UI components that changed

Show the draft to the user and ask for approval before writing it. Once approved, prepend it to `CHANGELOG.md` after the `---` separator line.

## Step 4 — Final summary

Print a short summary:
- Checks: pass/fail
- CHANGELOG: entry added (or skipped)

Then print: "Ready to commit."
