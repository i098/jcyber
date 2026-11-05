# watch-video — reference

## Dependencies

On Debian/Ubuntu, once:

```bash
sudo apt-get install -y ffmpeg tesseract-ocr python3-pip
sudo pip3 install --break-system-packages --ignore-installed typing_extensions faster-whisper yt-dlp
```

`ffmpeg`/`ffprobe` do the decoding, `tesseract` reads on-screen text,
`faster-whisper` transcribes speech (CPU, int8 — no GPU needed), `yt-dlp` downloads
URLs. The first transcription downloads a whisper model into
`~/.cache/huggingface` (`base` ≈ 150 MB, `tiny` ≈ 75 MB).

## Sources it handles

| Source | How |
|---|---|
| local file | read directly (`.mp4`, `.mov`, `.mkv`, `.webm`, …) |
| Instagram reel / post | `yt-dlp`, falling back to the page's `og:video` meta when Instagram hands anonymous clients an empty media list — no login, no cookies |
| YouTube, TikTok, X, Vimeo, Loom | `yt-dlp` |
| direct video URL | `yt-dlp` (or the og:video fallback for Instagram) |

Public Instagram reels work anonymously. Age-gated, private, or "collaborator only"
posts need cookies — pass them with `--cookies` support added below if that ever
matters; today the skill simply reports the failure.

## Artefacts

`--out DIR` keeps everything:

- `source.*` — the downloaded video
- `audio.wav` — 16 kHz mono, what whisper heard
- `frames/``[sssss].png` — one image per OCR sample, so you can look at a moment
  the text skimmed past
- stdout — the manifest, transcript, and on-screen digest

Without `--out`, a temp directory is used and only its path is printed.

## Tuning

- `--interval 5` for dense UI screens; 15 s is a reasonable default for talking heads.
- `--model small` when names and numbers must be right; `tiny` for a 45-minute talk.
- `--lang zh,en` hints whisper; it is a hint, not a constraint.
- `--no-transcript` when only the visuals matter (silent screen recordings).
