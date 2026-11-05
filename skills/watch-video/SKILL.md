---
name: watch-video
description: >
  Watch a video file or URL and report what happens in it. Use when the user sends a
  video, a screen recording, a clip, an Instagram reel, or a link to one (YouTube,
  Loom, X, TikTok, a direct .mp4) and asks what is in it, what was said, what is on
  screen, or to summarize it. Extracts the audio transcript, OCRs the on-screen text
  at keyframes, and returns a timestamped digest. Public Instagram reels work without
  logging in.
---

# Watch video

Turns a video into something you can actually read: a timestamped transcript plus the
text that was on screen. Works on a local path or a URL.

## Run it

```bash
python3 skills/watch-video/scripts/watch_video.py <path-or-url> [--out /tmp/watch-<name>]
```

Defaults: transcript on, OCR every 15 s, 1280 px frames, English. Useful flags:

| Flag | Meaning |
|---|---|
| `--interval 5` | sample on-screen text more often (denser OCR) |
| `--lang zh,en` | whisper language hint(s) |
| `--model base` | whisper model size (`tiny` is fastest, `small` more accurate) |
| `--no-ocr` / `--no-transcript` | skip half of the work |
| `--out DIR` | keep artefacts somewhere you can inspect later |

The script prints a manifest first, then `=== TRANSCRIPT ===` (one line per utterance
with `[mm:ss]`), then `=== ON SCREEN ===` (deduplicated OCR text with the timestamp
where it first appears). Read that output; it is the video.

If the script reports a missing dependency, install it with apt (`ffmpeg`,
`tesseract-ocr`, `python3-pip`) and `pip3 install faster-whisper` — see
`reference.md` for the exact commands and for how to read the artefacts it leaves
under `--out`.

## How to answer

1. Lead with what happens: the story or the demo, in order.
2. Quote the exact words that matter and put `[mm:ss]` on claims you took from the
   transcript, so the user can jump to them.
3. Separate what was *said* from what was *shown* when they disagree (a demo shows a
   failure the narrator calls a success — say both).
4. If the video has no speech, say so and rely on on-screen text; if neither has
   content, say the clip looks silent and static rather than inventing a plot.

## Limits

- OCR only sees text large enough to survive a 1280 px frame; fine print is lost.
- Whisper mangles names, ids and jargon: never copy an identifier from the transcript
  into a task without checking it against the on-screen text.
- A video longer than ~30 minutes: run with `--interval 60` and `--model tiny` first;
  it is a summary, not a transcript archive.
