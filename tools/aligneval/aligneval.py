#!/usr/bin/env python3
"""aligneval — стенд сравнения выравнивания текста песни по аудио (не часть сервиса poisq).

Берёт трек и «эталонный» LRC (человеческий, например от LRCLIB), отделяет вокал (Demucs), выравнивает текст
моделью Qwen3-ForcedAligner и сравнивает время начала каждой строки с эталоном. Вариант «mix» выравнивает
полный микс без отделения вокала — так видно, что даёт Demucs.

Запуск в контейнере см. tools/aligneval/README.md. Ничего не пишет рядом с треком: всё в рабочей папке.
"""
import argparse
import json
import os
import re
import statistics
import subprocess
import sys
import time

MAX_SEC = 295.0  # лимит Qwen3-ForcedAligner: 300 сек на вызов


def parse_lrc(text):
    """[(секунды, строка)] из LRC; строки без таймкода и пустые пропускаются."""
    out = []
    rx = re.compile(r"\[(\d{1,3}):(\d{2})(?:[.:](\d{1,3}))?\]")
    for raw in text.splitlines():
        stamps = list(rx.finditer(raw))
        if not stamps:
            continue
        line = rx.sub("", raw).strip()
        if not line:
            continue
        for m in stamps:
            frac = m.group(3) or "0"
            sec = int(m.group(1)) * 60 + int(m.group(2)) + int(frac) / (10 ** len(frac))
            out.append((sec, line))
    out.sort(key=lambda x: x[0])
    return out


def tokens(line):
    return re.findall(r"\w+", line.lower().replace("ё", "е"))


def percentile(xs, p):
    if not xs:
        return float("nan")
    xs = sorted(xs)
    k = (len(xs) - 1) * p
    lo, hi = int(k), min(int(k) + 1, len(xs) - 1)
    return xs[lo] + (xs[hi] - xs[lo]) * (k - lo)


def line_metrics(errors):
    """errors — signed (pred - gold) в секундах."""
    if not errors:
        return {}
    offset = statistics.median(errors)
    absx = [abs(e) for e in errors]
    adj = [abs(e - offset) for e in errors]

    def within(xs, t):
        return 100.0 * sum(1 for x in xs if x <= t) / len(xs)

    return {
        "lines": len(errors),
        "median_signed_s": round(offset, 3),
        "median_abs_s": round(statistics.median(absx), 3),
        "p90_abs_s": round(percentile(absx, 0.9), 3),
        "within_0.3s_pct": round(within(absx, 0.3), 1),
        "within_0.5s_pct": round(within(absx, 0.5), 1),
        "within_1.0s_pct": round(within(absx, 1.0), 1),
        "after_offset_median_abs_s": round(statistics.median(adj), 3),
        "after_offset_within_0.3s_pct": round(within(adj, 0.3), 1),
    }


def map_words_to_lines(lines_tokens, items):
    """Сопоставляет слова модели (text, start, end) строкам. Возвращает список индексов первого слова строки."""
    total = sum(len(t) for t in lines_tokens)
    if len(items) == total:
        starts, pos = [], 0
        for t in lines_tokens:
            starts.append(pos)
            pos += len(t)
        return starts
    # Запасной вариант: жадное сопоставление по тексту
    flat = [w for t in lines_tokens for w in t]
    norm_items = [" ".join(tokens(it[0])) for it in items]
    j, pos_map = 0, []
    for w in flat:
        while j < len(norm_items) and w not in norm_items[j].split():
            j += 1
        pos_map.append(j if j < len(norm_items) else None)
        j += 1
    starts, pos = [], 0
    for t in lines_tokens:
        starts.append(pos_map[pos] if pos < len(pos_map) else None)
        pos += len(t)
    return starts


def run(cmd, **kw):
    print("+", " ".join(cmd), flush=True)
    subprocess.run(cmd, check=True, **kw)


def stage(name, t0):
    print(f"[{time.time() - t0:7.1f}s] {name}", flush=True)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("audio", help="путь к аудиофайлу")
    ap.add_argument("gold_lrc", help="эталонный LRC (человеческий тайминг)")
    ap.add_argument("--work", default="/work", help="рабочая папка (кэш стемов и результаты)")
    ap.add_argument("--lang", default="", help="Russian/English/... (по умолчанию определяется по тексту)")
    ap.add_argument("--variants", default="vocals,mix", help="что выравнивать: vocals, mix")
    ap.add_argument("--device", default="cpu")
    args = ap.parse_args()

    t0 = time.time()
    os.makedirs(args.work, exist_ok=True)
    gold_all = parse_lrc(open(args.gold_lrc, encoding="utf-8", errors="replace").read())
    if len(gold_all) < 5:
        sys.exit("в эталонном LRC слишком мало строк с таймкодами")

    base = os.path.join(args.work, "track")
    mix_wav = base + "_mix.wav"
    if not os.path.exists(mix_wav):
        run(["ffmpeg", "-loglevel", "error", "-y", "-i", args.audio, "-t", str(MAX_SEC), "-ac", "2", "-ar", "44100", mix_wav])
    stage("микс подготовлен (первые %d сек)" % MAX_SEC, t0)

    # Эталонные строки только внутри обрезанного окна
    gold = [(s, l) for s, l in gold_all if s < MAX_SEC - 8]
    lines_tokens = [tokens(l) for _, l in gold]
    keep = [i for i, t in enumerate(lines_tokens) if t]
    gold = [gold[i] for i in keep]
    lines_tokens = [lines_tokens[i] for i in keep]
    text = " ".join(w for t in lines_tokens for w in t)
    lang = args.lang or ("Russian" if re.search(r"[а-яё]", text) else "English")
    print(f"строк в эталоне: {len(gold)}, слов: {len(text.split())}, язык: {lang}")

    wavs = {}
    variants = [v.strip() for v in args.variants.split(",") if v.strip()]
    if "mix" in variants:
        mix16 = base + "_mix16k.wav"
        if not os.path.exists(mix16):
            run(["ffmpeg", "-loglevel", "error", "-y", "-i", mix_wav, "-ac", "1", "-ar", "16000", mix16])
        wavs["mix"] = mix16
    if "vocals" in variants:
        voc16 = base + "_vocals16k.wav"
        if not os.path.exists(voc16):
            t1 = time.time()
            run([sys.executable, "-m", "demucs", "--two-stems=vocals", "-n", "htdemucs", "-o", os.path.join(args.work, "stems"), mix_wav])
            stem = os.path.join(args.work, "stems", "htdemucs", "track_mix", "vocals.wav")
            run(["ffmpeg", "-loglevel", "error", "-y", "-i", stem, "-ac", "1", "-ar", "16000", voc16])
            print(f"Demucs: {time.time() - t1:.1f} сек")
        wavs["vocals"] = voc16
    stage("аудио готово", t0)

    import torch
    from qwen_asr import Qwen3ForcedAligner

    model = Qwen3ForcedAligner.from_pretrained("Qwen/Qwen3-ForcedAligner-0.6B", dtype=torch.float32, device_map=args.device)
    stage("модель загружена", t0)

    report = {}
    for name, wav in wavs.items():
        t1 = time.time()
        res = model.align(audio=wav, text=text, language=lang)
        items = [(it.text, float(it.start_time), float(it.end_time)) for it in res[0]]
        print(f"[{name}] выравнивание заняло {time.time() - t1:.1f} сек, слов от модели: {len(items)}")
        starts = map_words_to_lines(lines_tokens, items)

        errors, rows = [], []
        pred_lrc = []
        for (g_sec, g_line), idx in zip(gold, starts):
            if idx is None or idx >= len(items):
                continue
            p = items[idx][1]
            errors.append(p - g_sec)
            rows.append((abs(p - g_sec), g_sec, p, g_line))
            pred_lrc.append("[%02d:%05.2f] %s" % (int(p // 60), p % 60, g_line))
        m = line_metrics(errors)
        report[name] = m
        with open(os.path.join(args.work, f"aligned_{name}.lrc"), "w", encoding="utf-8") as f:
            f.write("\n".join(pred_lrc) + "\n")
        print(f"[{name}] метрики: {json.dumps(m, ensure_ascii=False)}")
        print(f"[{name}] худшие строки (ошибка, эталон, модель, текст):")
        for e, g, p, l in sorted(rows, reverse=True)[:6]:
            print(f"   {e:5.2f}s  {g:7.2f}  {p:7.2f}  {l[:50]}")

    with open(os.path.join(args.work, "report.json"), "w", encoding="utf-8") as f:
        json.dump(report, f, ensure_ascii=False, indent=2)
    stage("готово; результаты в " + args.work, t0)


if __name__ == "__main__":
    main()
