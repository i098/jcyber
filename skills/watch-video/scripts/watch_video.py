#!/usr/bin/env python3
"""Watch a video: transcribe the audio and OCR the on-screen text.

    python3 watch_video.py <path-or-url> [--interval 15] [--out DIR]

Prints a manifest, a timestamped transcript, and the deduplicated on-screen text.
Everything it produces stays in --out so a later pass can look at frames directly.
"""
from __future__ import annotations

import argparse
import json
import os
import re
import shutil
import subprocess
import sys
import tempfile
from pathlib import Path


def run(cmd: list[str], **kw) -> subprocess.CompletedProcess:
    return subprocess.run(cmd, capture_output=True, text=True, **kw)


def need(binary: str, hint: str) -> str:
    path = shutil.which(binary)
    if not path:
        sys.exit(f"missing {binary}: {hint}")
    return path


UA = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36"


def fetch_instagram(src: str, work: Path) -> Path | None:
    """Public reels without login: read og:video off the page, then stream it.

    yt-dlp usually manages this, but Instagram rotates its API and sometimes hands
    anonymous clients an empty media list. The og:video meta tag is what the public
    embed uses, so it stays readable without cookies.
    """
    import urllib.request

    req = urllib.request.Request(src, headers={"User-Agent": UA, "Accept-Language": "en-US,en;q=0.9"})
    try:
        html = urllib.request.urlopen(req, timeout=30).read().decode("utf-8", "replace")
    except Exception:
        return None
    for pattern in (r'<meta[^>]+property="og:video"[^>]+content="([^"]+)"',
                    r'<meta[^>]+content="([^"]+)"[^>]+property="og:video"'):
        m = re.search(pattern, html)
        if not m:
            continue
        url = m.group(1).replace("&amp;", "&")
        dst = work / "source.mp4"
        req = urllib.request.Request(url, headers={"User-Agent": UA})
        try:
            with urllib.request.urlopen(req, timeout=60) as resp, open(dst, "wb") as f:
                shutil.copyfileobj(resp, f)
        except Exception:
            continue
        if dst.exists() and dst.stat().st_size > 1000:
            return dst
    return None


def fetch(src: str, work: Path) -> Path:
    if not re.match(r"^https?://", src):
        path = Path(src).expanduser()
        if not path.exists():
            sys.exit(f"no such file: {path}")
        return path
    ytdlp = shutil.which("yt-dlp")
    if not ytdlp:
        sys.exit("missing yt-dlp for URLs: pip3 install yt-dlp")
    target = work / "source.%(ext)s"
    r = run([ytdlp, "-q", "--no-warnings", "--no-playlist", "-f", "bv*+ba/b",
             "--user-agent", UA, "-o", str(target), src])
    found = [p for p in work.glob("source.*") if p.is_file()]
    if r.returncode or not found:
        if "instagram.com" in src:
            fallback = fetch_instagram(src, work)
            if fallback:
                return fallback
        detail = (r.stderr.strip().splitlines() or [f"exit {r.returncode}"])[-1]
        sys.exit(f"download failed: {detail}")
    return found[0]


def probe(ffprobe: str, video: Path) -> dict:
    r = run([ffprobe, "-v", "quiet", "-print_format", "json", "-show_format", "-show_streams", str(video)])
    if r.returncode:
        sys.exit(f"ffprobe failed on {video}")
    data = json.loads(r.stdout or "{}")
    fmt = data.get("format") or {}
    streams = data.get("streams") or []
    clip = next((s for s in streams if s.get("codec_type") == "video"), {})
    audio = next((s for s in streams if s.get("codec_type") == "audio"), {})
    fps = 0.0
    if clip.get("avg_frame_rate") and "/" in clip["avg_frame_rate"]:
        num, den = clip["avg_frame_rate"].split("/")
        fps = float(num) / float(den) if float(den) else 0.0
    return {
        "duration_s": float(fmt.get("duration") or 0),
        "size_mb": round(int(fmt.get("size") or 0) / 1e6, 1),
        "resolution": f"{clip.get('width')}x{clip.get('height')}" if clip else "no video stream",
        "fps": round(fps, 2),
        "codec": clip.get("codec_name") or "-",
        "audio": audio.get("codec_name") or ("none" if not audio else "-"),
    }


def transcribe(ffmpeg: str, video: Path, work: Path, model: str, langs: str) -> list[tuple[float, str]]:
    """Decode to raw PCM ourselves and hand whisper a float array.

    Letting faster-whisper open the file drags in PyAV, whose `metadata_errors`
    argument only exists on some builds; ffmpeg is already installed and exact.
    """
    wav = work / "audio.wav"
    raw = work / "audio.raw"
    r = run([ffmpeg, "-y", "-loglevel", "error", "-i", str(video), "-vn", "-ac", "1", "-ar", "16000",
             "-f", "s16le", str(raw)])
    if r.returncode or not raw.exists():
        return []
    try:
        from faster_whisper import WhisperModel  # type: ignore
        import numpy as np
    except ImportError:
        print("(no transcript: pip3 install faster-whisper)", file=sys.stderr)
        return []
    audio = np.fromfile(raw, dtype=np.int16).astype("float32") / 32768.0
    language = (langs.split(",")[0] or None) if langs else None
    engine = WhisperModel(model, device="cpu", compute_type="int8")
    segments, _ = engine.transcribe(audio, language=language, vad_filter=True)
    return [(float(s.start), s.text.strip()) for s in segments if s.text.strip()]


def ocr_frames(ffmpeg: str, tesseract: str, video: Path, work: Path, duration: float, interval: int) -> list[tuple[float, str]]:
    frames = work / "frames"
    frames.mkdir(exist_ok=True)
    step = max(1, interval)
    out = []
    t = 0.0
    while t < max(duration, 1):
        png = frames / f"{int(t):05d}.png"
        r = run([ffmpeg, "-y", "-ss", f"{t:.2f}", "-i", str(video), "-frames:v", "1",
                 "-vf", "scale='min(1280,iw)':-2", str(png)])
        if r.returncode or not png.exists():
            t += step
            continue
        text = run([tesseract, str(png), "stdout", "-l", "eng"]).stdout
        text = " ".join(text.split())
        if len(text) >= 8:
            out.append((t, text))
        t += step
    return out


def hhmmss(seconds: float) -> str:
    m, s = divmod(int(seconds), 60)
    h, m = divmod(m, 60)
    return f"{h}:{m:02d}:{s:02d}" if h else f"{m:02d}:{s:02d}"


def main() -> None:
    ap = argparse.ArgumentParser()
    ap.add_argument("source")
    ap.add_argument("--out")
    ap.add_argument("--interval", type=int, default=15, help="seconds between OCR frames")
    ap.add_argument("--lang", default="en", help="whisper language hint")
    ap.add_argument("--model", default="base", help="whisper model size")
    ap.add_argument("--no-ocr", action="store_true")
    ap.add_argument("--no-transcript", action="store_true")
    args = ap.parse_args()

    ffmpeg = need("ffmpeg", "apt-get install ffmpeg")
    ffprobe = need("ffprobe", "apt-get install ffmpeg")

    work = Path(args.out).expanduser() if args.out else Path(tempfile.mkdtemp(prefix="watch-"))
    work.mkdir(parents=True, exist_ok=True)
    video = fetch(args.source, work)
    meta = probe(ffprobe, video)

    print(f"file: {video}")
    print(f"duration: {hhmmss(meta['duration_s'])}  size: {meta['size_mb']} MB  "
          f"video: {meta['resolution']} @ {meta['fps']} fps ({meta['codec']})  audio: {meta['audio']}")

    transcript: list[tuple[float, str]] = []
    if not args.no_transcript:
        transcript = transcribe(ffmpeg, video, work, args.model, args.lang)

    onscreen: list[tuple[float, str]] = []
    if not args.no_ocr:
        tesseract = shutil.which("tesseract")
        if tesseract:
            onscreen = ocr_frames(ffmpeg, tesseract, video, work, meta["duration_s"], args.interval)
        else:
            print("(no OCR: apt-get install tesseract-ocr)", file=sys.stderr)

    if not transcript and not onscreen:
        print("nothing readable: no speech detected and no on-screen text found", file=sys.stderr)

    print("\n=== TRANSCRIPT ===")
    for at, text in transcript:
        print(f"[{hhmmss(at)}] {text}")
    if not transcript:
        print("(none)")

    print("\n=== ON SCREEN ===")
    seen: set[str] = set()
    for at, text in onscreen:
        key = text.lower()
        if key in seen:
            continue
        seen.add(key)
        print(f"[{hhmmss(at)}] {text}")
    if not onscreen:
        print("(none)")

    print(f"\nartefacts: {work}")


if __name__ == "__main__":
    main()
