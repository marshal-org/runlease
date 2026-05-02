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

## Step 4 — docs/GAPS.md

Read `docs/GAPS.md`. Based on the diff, identify any gaps that are now closed. For each one:
- Mark the table row with a strikethrough or move it to a "Closed" section if one exists
- Show the user which gaps you're marking closed and confirm before editing

## Step 5 — Ofir-Todo.md

Read `Ofir-Todo.md`. Based on the diff, identify any checklist items that are now done. Show the user which items you're checking off and confirm before editing.

## Step 6 — .env.example coverage

Scan `apps/*/internal/config/*.go` for any `os.Getenv(` or `os.LookupEnv(` calls. Cross-reference with `.env.example`. Report any env vars that appear in config files but are missing from `.env.example`. If any are missing, show the user the proposed additions and ask before editing.

## Step 7 — Final summary

Print a short summary:
- Checks: pass/fail
- CHANGELOG: entry added (or skipped)
- GAPS.md: N gaps closed (or none)
- Ofir-Todo.md: N items checked off (or none)
- .env.example: N vars added (or up to date)

Then print: "Ready to commit."
