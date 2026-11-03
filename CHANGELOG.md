# Changelog
## [Unreleased] - 2026-11-03

- Task thinking effort is selectable: `thinking_effort: "auto"` on task creation
  binds the task to the active model's provider-default variant, while hard tasks
  stay on the max-thinking rotation pool. Exposed to the iMessage front desk as a
  `create_task` field, so the spawner picks effort by task difficulty.
- iMessage bridge gains `rename_task` (via `PATCH /api/tasks/{id}`), and
  `create_task` now spells out that `name` is the display title and `description`
  is long-form context, so names stop landing in descriptions.
- Asset graph: legend and labels are in English, and colors follow the app theme
  instead of staying light inside dark mode.
- LLM Token Usage panel defaults to the New view; Legacy remains selectable.


## [Unreleased] - 2026-10-31

- Traffic capture now defaults **on**: the recording proxy, traffic tools and
  capture prompt reach agents out of the box.
- Automatic agent traffic binding now defaults **on**, so findings carry their
  request/response evidence without flipping a switch first.
- Login page: replaced the missing `/logo.svg` with `/logo.png`; the logo no
  longer renders as a broken image.
- Web client: 401s no longer reload `/login` and `/setup`, which had put the
  login page in an endless remount loop whenever a password was set.
- Build: fixed two strict-null type errors that failed `next build`.

## [1.0.0] - 2026-10-21

- Initial jcyber release.
- English UI, prompts, and seed data throughout.
- Attribution scrub: no upstream project names in binary or runtime traffic;
  unattributed user agents; module paths renamed; update source repointed.
- Operational stealth defaults for workers; dashboard binds to localhost.
