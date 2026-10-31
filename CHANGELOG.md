# Changelog

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
