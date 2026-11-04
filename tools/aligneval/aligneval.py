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


def lis_indices(vals):
    """Индексы наибольшей неубывающей подпоследовательности (строки с согласованным порядком времён)."""
    import bisect

    tails, tails_idx, prev = [], [], [-1] * len(vals)
    for i, v in enumerate(vals):
        k = bisect.bisect_right(tails, v)
        if k == len(tails):
            tails.append(v)
            tails_idx.append(i)
        else:
            tails[k] = v
            tails_idx[k] = i
        prev[i] = tails_idx[k - 1] if k > 0 else -1
    out, cur = [], tails_idx[-1] if tails_idx else -1
    while cur != -1:
        out.append(cur)
        cur = prev[cur]
    return set(out)


def eval_lrc_pair(gold, pred):
    """Сравнивает два LRC по совпадающим строкам (по тексту, в порядке следования)."""
    pnorm = [(" ".join(tokens(l)), s) for s, l in pred]
    errors, j = [], 0
    for g_sec, g_line in gold:
        key = " ".join(tokens(g_line))
        k = j
        while k < len(pnorm) and pnorm[k][0] != key:
            k += 1
        if k < len(pnorm):
            errors.append(pnorm[k][1] - g_sec)
            j = k + 1
    return errors


def snap_with_rms(items, starts_idx, rms, frame=0.01, rel_thr=0.12, max_shift=2.5, min_shift=0.15):
    """Подтягивает начало каждой строки к реальному началу голоса.
    Выравниватели часто «растягивают» первое слово строки назад, на тишину перед ним (после проигрыша
    следующая строка появляется слишком рано). rms — огибающая громкости вокала по кадрам frame сек."""
    if not rms:
        return list(items), 0
    thr = rel_thr * percentile(rms, 0.95)
    out, moved = list(items), 0
    for a in starts_idx:
        text, s, e = out[a]
        i0 = max(0, int(s / frame))
        i1 = min(len(rms), int(min(e, s + max_shift) / frame) + 1)
        first = next((k for k in range(i0, i1) if rms[k] > thr), None)
        if first is None:
            continue
        new_s = first * frame
        if new_s - s >= min_shift and new_s < e - 0.05:
            out[a] = (text, new_s, e)
            moved += 1
    return out, moved


def line_flags(items, lines_tokens, rms, frame=0.01, rel_thr=0.12):
    """Признаки того, что строка поставлена «на выдумку»: выравниватель всегда назначает время всем словам,
    даже если в записи их нет (текст не совпадает с аудио). Подозрительна строка, если в её интервале мало
    голоса, слова идут слишком быстро или слишком растянуты. Возвращает [(индекс, начало, конец, доля голоса, слов/с, причины)]."""
    thr = rel_thr * percentile(rms, 0.95) if rms else 0.0
    out, pos = [], 0
    for i, t in enumerate(lines_tokens):
        a, b = pos, pos + len(t)
        pos = b
        if not t:
            continue
        st, en = items[a][1], items[b - 1][2]
        dur = max(en - st, 1e-6)
        i0, i1 = int(st / frame), min(len(rms), int(en / frame) + 1)
        seg = rms[i0:i1]
        ratio = (sum(1 for v in seg if v > thr) / len(seg)) if seg else 0.0
        rate = len(t) / dur
        reasons = []
        if rms and ratio < 0.35:
            reasons.append("голоса мало (%.0f%% кадров)" % (ratio * 100))
        if rate > 6.0:
            reasons.append("слишком быстро (%.1f слов/с)" % rate)
        if dur / len(t) > 2.5:
            reasons.append("слишком растянуто (%.1f с/слово)" % (dur / len(t)))
        out.append((i, st, en, ratio, rate, reasons))
    return out


def refine_items(model, wav, items, lines_tokens, lang, work, total_dur):
    """Второй проход: каждая строка выравнивается ещё раз в коротком окне вместе с соседними строками
    (предыдущая + текущая + следующая). Модель видит только локальный кусок аудио, поэтому реже путает
    повторы припева. Берётся результат для средней строки."""
    starts_idx, pos = [], 0
    for t in lines_tokens:
        starts_idx.append(pos)
        pos += len(t)
    ends_idx = starts_idx[1:] + [len(items)]
    new_items, changed = list(items), 0
    n = len(lines_tokens)
    for i in range(n):
        lo, hi = max(0, i - 1), min(n - 1, i + 1)
        t0 = max(0.0, items[starts_idx[lo]][1] - 1.5)
        t1 = min(total_dur, items[ends_idx[hi] - 1][2] + 1.5)
        if t1 - t0 < 1.0:
            continue
        sub_text = " ".join(w for k in range(lo, hi + 1) for w in lines_tokens[k])
        tmp = os.path.join(work, f"refine_{i}.wav")
        run(["ffmpeg", "-loglevel", "error", "-y", "-ss", str(t0), "-t", str(t1 - t0), "-i", wav, "-ac", "1", "-ar", "16000", tmp])
        res = model.align(audio=tmp, text=sub_text, language=lang)
        sub = [(it.text, float(it.start_time) + t0, float(it.end_time) + t0) for it in res[0]]
        if len(sub) != sum(len(lines_tokens[k]) for k in range(lo, hi + 1)):
            continue
        off = sum(len(lines_tokens[k]) for k in range(lo, i))
        mine = sub[off:off + len(lines_tokens[i])]
        # не принимаем результат, если он вышел за пределы окна соседей (подозрительный скачок)
        if mine and abs(mine[0][1] - items[starts_idx[i]][1]) > 8.0:
            continue
        if any(abs(a[1] - b[1]) > 0.05 for a, b in zip(mine, new_items[starts_idx[i]:ends_idx[i]])):
            changed += 1
        new_items[starts_idx[i]:ends_idx[i]] = mine
    return new_items, changed


def vocal_rms(wav16, frame=0.01):
    import numpy as np
    import soundfile as sf

    x, sr = sf.read(wav16, dtype="float32")
    if x.ndim > 1:
        x = x.mean(axis=1)
    hop = int(sr * frame)
    n = len(x) // hop
    if n == 0:
        return []
    r = np.sqrt((x[: n * hop].reshape(n, hop) ** 2).mean(axis=1))
    return r.tolist()


def word_sim(a, b):
    import difflib

    return 1.0 if a == b else difflib.SequenceMatcher(None, a, b).ratio()


def match_ref_to_asr(ref, asr, min_sim=0.6, gap_ref=-0.95, gap_asr=-0.4):
    """Глобальное выравнивание (Нидлман—Вунш) слов эталона и слов распознавания, строго по порядку.
    Повторы припева не путаются, потому что распознавание содержит их в том порядке, в каком они спеты.
    Слова эталона, которых нет в записи, остаются без пары. Возвращает список: для каждого слова эталона
    индекс слова распознавания или None."""
    P, Q = len(ref), len(asr)
    dp = [[0.0] * (Q + 1) for _ in range(P + 1)]
    back = [[0] * (Q + 1) for _ in range(P + 1)]  # 1 = диагональ, 2 = пропуск слова эталона, 3 = пропуск слова ASR
    for i in range(1, P + 1):
        dp[i][0], back[i][0] = i * gap_ref, 2
    for j in range(1, Q + 1):
        dp[0][j], back[0][j] = j * (gap_asr / 4), 3
    sims = {}
    for i in range(1, P + 1):
        for j in range(1, Q + 1):
            key = (ref[i - 1], asr[j - 1])
            if key not in sims:
                sims[key] = word_sim(*key)
            sm = sims[key]
            diag = dp[i - 1][j - 1] + (3.0 * sm if sm >= min_sim else -1.0)
            up = dp[i - 1][j] + gap_ref
            left = dp[i][j - 1] + gap_asr
            best, d = diag, 1
            if up > best:
                best, d = up, 2
            if left > best:
                best, d = left, 3
            dp[i][j], back[i][j] = best, d
    out = [None] * P
    i, j = P, Q
    while i > 0 and j > 0:
        d = back[i][j]
        if d == 1:
            if sims.get((ref[i - 1], asr[j - 1]), 0) >= min_sim:
                out[i - 1] = j - 1
            i, j = i - 1, j - 1
        elif d == 2:
            i -= 1
        else:
            j -= 1
    return out


def groq_words(wav16, lang_iso, api_key):
    """Whisper large-v3 на ЧИСТОМ вокале: слова с таймкодами (грубая карта «что и когда спето»)."""
    import requests

    with open(wav16, "rb") as f:
        r = requests.post(
            "https://api.groq.com/openai/v1/audio/transcriptions",
            headers={"Authorization": "Bearer " + api_key},
            files={"file": ("vocals.wav", f, "audio/wav")},
            data={"model": "whisper-large-v3", "response_format": "verbose_json", "temperature": "0",
                  "language": lang_iso, "timestamp_granularities[]": "word"},
            timeout=180,
        )
    if r.status_code != 200:
        sys.exit(f"Groq HTTP {r.status_code}: {r.text[:300]}")
    return [(w["word"], float(w["start"]), float(w["end"])) for w in r.json().get("words", [])]


def asr_anchor_lines(model, voc16, lines_tokens, lang_name, lang_iso, work, api_key):
    """Подход «распознавание → якоря → точное выравнивание»:
    1) Whisper по вокалу даёт, что и когда спето (устойчиво к повторам и проигрышам);
    2) слова эталона сопоставляются с распознанными строго по порядку; строки без пары считаются «не найденными в записи»;
    3) для найденных строк время уточняется Qwen3-ForcedAligner в коротком окне вокруг якоря."""
    asr = groq_words(voc16, lang_iso, api_key)
    asr_tokens = [(" ".join(tokens(w)), st, en) for w, st, en in asr]
    asr_tokens = [t for t in asr_tokens if t[0]]
    ref = [w for t in lines_tokens for w in t]
    pair = match_ref_to_asr(ref, [t[0] for t in asr_tokens])

    results, pos = [], 0
    for i, toks in enumerate(lines_tokens):
        idxs = pair[pos:pos + len(toks)]
        pos += len(toks)
        got = [(k, j) for k, j in enumerate(idxs) if j is not None]
        if not toks or len(got) < max(1, (len(toks) + 1) // 2):
            results.append({"line": i, "status": "нет в записи", "start": None, "end": None, "matched": len(got), "of": len(toks)})
            continue
        k0, j0 = got[0]
        k1, j1 = got[-1]
        a_start = max(0.0, asr_tokens[j0][1] - 0.45 * k0)
        a_end = asr_tokens[j1][2] + 0.45 * (len(toks) - 1 - k1)
        t0, t1 = max(0.0, a_start - 1.5), a_end + 1.5
        status, start, end = "ASR", a_start, a_end
        tmp = os.path.join(work, f"anchor_{i}.wav")
        run(["ffmpeg", "-loglevel", "error", "-y", "-ss", str(t0), "-t", str(t1 - t0), "-i", voc16, "-ac", "1", "-ar", "16000", tmp])
        try:
            res = model.align(audio=tmp, text=" ".join(toks), language=lang_name)
            sub = [(it.text, float(it.start_time) + t0, float(it.end_time) + t0) for it in res[0]]
            if len(sub) == len(toks) and abs(sub[0][1] - a_start) <= 3.0:
                status, start, end = "ASR+выравнивание", sub[0][1], sub[-1][2]
        except Exception as e:  # noqa: BLE001
            print(f"   строка {i + 1}: выравнивание в окне не удалось: {e}")
        results.append({"line": i, "status": status, "start": start, "end": end, "matched": len(got), "of": len(toks)})
    return results, asr


def run(cmd, **kw):
    print("+", " ".join(cmd), flush=True)
    subprocess.run(cmd, check=True, **kw)


def stage(name, t0):
    print(f"[{time.time() - t0:7.1f}s] {name}", flush=True)


def repair_items(model, wav, items, lines_tokens, lang, work):
    """Строки, выбивающиеся из порядка времён (типично: повторяющийся припев попал не в то место),
    перевыравниваются отдельно в окне между надёжными соседними строками."""
    starts_idx, pos = [], 0
    for t in lines_tokens:
        starts_idx.append(pos)
        pos += len(t)
    ends_idx = starts_idx[1:] + [len(items)]
    S = [items[a][1] for a in starts_idx]
    good = lis_indices(S)
    bad = [i for i in range(len(S)) if i not in good]
    if not bad:
        return items, starts_idx, 0

    items = list(items)
    fixed, i = 0, 0
    while i < len(bad):
        j = i
        while j + 1 < len(bad) and bad[j + 1] == bad[j] + 1:
            j += 1
        a, b = bad[i], bad[j]  # подряд идущие плохие строки a..b
        left = max([k for k in good if k < a], default=None)
        right = min([k for k in good if k > b], default=None)
        w0 = items[ends_idx[left] - 1][2] if left is not None else 0.0
        w1 = items[starts_idx[right]][1] if right is not None else items[-1][2] + 5.0
        i = j + 1
        if w1 - w0 < 1.0:
            continue
        w0c = max(0.0, w0 - 0.3)
        sub_text = " ".join(w for k in range(a, b + 1) for w in lines_tokens[k])
        tmp = os.path.join(work, f"repair_{a}_{b}.wav")
        run(["ffmpeg", "-loglevel", "error", "-y", "-ss", str(w0c), "-t", str(w1 - w0c + 0.3), "-i", wav, "-ac", "1", "-ar", "16000", tmp])
        res = model.align(audio=tmp, text=sub_text, language=lang)
        sub = [(it.text, float(it.start_time) + w0c, float(it.end_time) + w0c) for it in res[0]]
        n_expected = sum(len(lines_tokens[k]) for k in range(a, b + 1))
        if len(sub) != n_expected:
            continue
        items[starts_idx[a]:ends_idx[b]] = sub
        fixed += b - a + 1
    return items, starts_idx, fixed


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("audio", help="путь к аудиофайлу")
    ap.add_argument("gold_lrc", nargs="?", default="", help="эталонный LRC (человеческий тайминг); не нужен с --text")
    ap.add_argument("--work", default="/work", help="рабочая папка (кэш стемов и результаты)")
    ap.add_argument("--lang", default="", help="Russian/English/... (по умолчанию определяется по тексту)")
    ap.add_argument("--variants", default="vocals,mix", help="что выравнивать: vocals, mix")
    ap.add_argument("--device", default="cpu")
    ap.add_argument("--pred", default="", help="только сравнить с эталоном готовый LRC (например, из poisq), без моделей")
    ap.add_argument("--text", default="", help="режим без эталона: файл с текстом (по строке); метрик не будет, только aligned_*.lrc")
    ap.add_argument("--refine", action="store_true", help="второй проход: перевыровнять каждую строку в окне с соседями")
    ap.add_argument("--snap-onset", dest="snap_onset", action="store_true", help="подтянуть начало строк к реальному началу голоса")
    ap.add_argument("--lead", type=float, default=0.5, help="на сколько сек раньше писать время строк в итоговый LRC (по умолчанию 0.5)")
    ap.add_argument("--asr-anchor", dest="asr_anchor", action="store_true",
                    help="якоря от Whisper по вокалу (нужен GROQ_API_KEY в окружении) + точное выравнивание в окне")
    ap.add_argument("--only-anchor", dest="only_anchor", action="store_true", help="с --asr-anchor: пропустить обычный проход Qwen по всей дорожке")
    ap.add_argument("--repair", action="store_true", help="перевыровнять строки с нарушенным порядком времён в окне между соседями")
    args = ap.parse_args()

    t0 = time.time()
    if args.asr_anchor and not os.environ.get("GROQ_API_KEY"):
        sys.exit("для --asr-anchor задайте GROQ_API_KEY: sudo --preserve-env=GROQ_API_KEY docker run -e GROQ_API_KEY ...")
    os.makedirs(args.work, exist_ok=True)
    no_gold = bool(args.text)
    if no_gold:
        stamp = re.compile(r"\[\d{1,3}:\d{2}(?:[.:]\d{1,3})?\]")
        raw_text = open(args.text, encoding="utf-8", errors="replace").read()
        gold_all = [(0.0, stamp.sub("", l).strip()) for l in raw_text.splitlines() if stamp.sub("", l).strip()]
    else:
        if not args.gold_lrc:
            sys.exit("нужен эталонный LRC или --text файл")
        gold_all = parse_lrc(open(args.gold_lrc, encoding="utf-8", errors="replace").read())
        if len(gold_all) < 5:
            sys.exit("в эталонном LRC слишком мало строк с таймкодами")

    if args.pred and not no_gold:
        pred = parse_lrc(open(args.pred, encoding="utf-8", errors="replace").read())
        errs = eval_lrc_pair(gold_all, pred)
        print(f"совпавших строк: {len(errs)} из {len(gold_all)}")
        print("метрики:", json.dumps(line_metrics(errs), ensure_ascii=False))
        return

    base = os.path.join(args.work, "track")
    mix_wav = base + "_mix.wav"
    if not os.path.exists(mix_wav):
        run(["ffmpeg", "-loglevel", "error", "-y", "-i", args.audio, "-t", str(MAX_SEC), "-ac", "2", "-ar", "44100", mix_wav])
    stage("микс подготовлен (первые %d сек)" % MAX_SEC, t0)

    # Эталонные строки только внутри обрезанного окна
    gold = list(gold_all) if no_gold else [(s, l) for s, l in gold_all if s < MAX_SEC - 8]
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

    # qwen_asr при импорте тянет nagisa (токенизатор японского) -> dynet, собранный под AVX2: на CPU без AVX2
    # это даёт "Illegal instruction". Для русского/английского он не нужен, поэтому подставляем заглушку.
    import types

    if "nagisa" not in sys.modules:
        stub = types.ModuleType("nagisa")

        def _unavailable(*_a, **_k):
            raise RuntimeError("nagisa отключён заглушкой (нет AVX2); японский текст не поддерживается")

        stub.tagging = _unavailable
        sys.modules["nagisa"] = stub

    import torch
    from qwen_asr import Qwen3ForcedAligner

    model = Qwen3ForcedAligner.from_pretrained("Qwen/Qwen3-ForcedAligner-0.6B", dtype=torch.float32, device_map=args.device)
    stage("модель загружена", t0)

    report = {}
    for name, wav in ([] if args.only_anchor else list(wavs.items())):
        t1 = time.time()
        res = model.align(audio=wav, text=text, language=lang)
        items = [(it.text, float(it.start_time), float(it.end_time)) for it in res[0]]
        print(f"[{name}] выравнивание заняло {time.time() - t1:.1f} сек, слов от модели: {len(items)}")
        starts = map_words_to_lines(lines_tokens, items)

        if args.repair and len(items) == sum(len(t) for t in lines_tokens):
            items, starts, fixed = repair_items(model, wav, items, lines_tokens, lang, args.work)
            print(f"[{name}] repair: перевыровнено строк с нарушенным порядком: {fixed}")

        full_counts = len(items) == sum(len(t) for t in lines_tokens)
        if args.refine and full_counts:
            total_dur = items[-1][2] + 3.0
            items, changed = refine_items(model, wav, items, lines_tokens, lang, args.work, total_dur)
            print(f"[{name}] refine: изменено строк: {changed}")
        if args.snap_onset and full_counts:
            rms = vocal_rms(wavs["vocals"] if "vocals" in wavs else wav)
            items, moved = snap_with_rms(items, starts, rms)
            print(f"[{name}] snap-onset: начало строки подтянуто к голосу у {moved} строк")

        if "vocals" in wavs and full_counts:
            flags = line_flags(items, lines_tokens, vocal_rms(wavs["vocals"]))
            suspect = [f for f in flags if f[5]]
            print(f"[{name}] подозрительных строк (время могло быть выдумано): {len(suspect)} из {len(flags)}")
            for i, st, en, ratio, rate, reasons in suspect:
                print("   строка %2d  %6.1f–%6.1f  %s  | %s" % (i + 1, st, en, "; ".join(reasons), " ".join(lines_tokens[i])[:40]))
            report[name + "_suspect_lines"] = len(suspect)

        errors, rows = [], []
        pred_lrc = []
        for (g_sec, g_line), idx in zip(gold, starts):
            if idx is None or idx >= len(items):
                continue
            p = items[idx][1]
            if not no_gold:
                errors.append(p - g_sec)
                rows.append((abs(p - g_sec), g_sec, p, g_line))
            q = max(0.0, p - args.lead)
            pred_lrc.append("[%02d:%05.2f] %s" % (int(q // 60), q % 60, g_line))
        if no_gold:
            print(f"[{name}] строки с временем (с учётом --lead {args.lead}):")
            for l in pred_lrc:
                print("   " + l)
        m = line_metrics(errors)
        report[name] = m
        with open(os.path.join(args.work, f"aligned_{name}.lrc"), "w", encoding="utf-8") as f:
            f.write("\n".join(pred_lrc) + "\n")
        print(f"[{name}] метрики: {json.dumps(m, ensure_ascii=False)}")
        print(f"[{name}] худшие строки (ошибка, эталон, модель, текст):")
        for e, g, p, l in sorted(rows, reverse=True)[:6]:
            print(f"   {e:5.2f}s  {g:7.2f}  {p:7.2f}  {l[:50]}")

    if args.asr_anchor:
        key = os.environ.get("GROQ_API_KEY", "")
        if not key:
            sys.exit("для --asr-anchor задайте GROQ_API_KEY (docker run -e GROQ_API_KEY)")
        if "vocals" not in wavs:
            sys.exit("--asr-anchor требует вариант vocals")
        iso = {"Russian": "ru", "English": "en"}.get(lang, "")
        t1 = time.time()
        results, asr = asr_anchor_lines(model, wavs["vocals"], lines_tokens, lang, iso, args.work, key)
        print(f"[asr-anchor] заняло {time.time() - t1:.1f} сек; слов в распознавании: {len(asr)}")
        # Что реально звучит (карта по Whisper): фразы, разделённые паузами > 1.2 сек
        print("[asr-anchor] что слышит Whisper на вокале (фразы по паузам):")
        phrase, last_end = [], None
        for w, st, en in asr + [("", 1e9, 1e9)]:
            if phrase and (last_end is None or st - last_end > 1.2 or not w):
                print("   %6.1f–%6.1f  %s" % (phrase[0][1], phrase[-1][2], " ".join(x[0].strip() for x in phrase)[:80]))
                phrase = []
            if w:
                phrase.append((w, st, en))
                last_end = en
        found = [r for r in results if r["start"] is not None]
        print(f"[asr-anchor] найдено в записи строк: {len(found)} из {len(results)}")
        errs, lrc_lines = [], []
        for r in results:
            line_text = " ".join(lines_tokens[r["line"]])
            if r["start"] is None:
                print("   строка %2d  НЕТ В ЗАПИСИ  (слов найдено %d из %d)  %s" % (r["line"] + 1, r["matched"], r["of"], line_text[:40]))
                continue
            q = max(0.0, r["start"] - args.lead)
            lrc_lines.append("[%02d:%05.2f] %s" % (int(q // 60), q % 60, gold[r["line"]][1]))
            print("   строка %2d  %7.2f–%7.2f  %-18s %s" % (r["line"] + 1, r["start"], r["end"], r["status"], line_text[:40]))
            if not no_gold:
                errs.append(r["start"] - gold[r["line"]][0])
        with open(os.path.join(args.work, "aligned_asr_anchor.lrc"), "w", encoding="utf-8") as f:
            f.write("\n".join(lrc_lines) + "\n")
        if errs:
            m = line_metrics(errs)
            report["asr_anchor"] = m
            print("[asr-anchor] метрики:", json.dumps(m, ensure_ascii=False))

    with open(os.path.join(args.work, "report.json"), "w", encoding="utf-8") as f:
        json.dump(report, f, ensure_ascii=False, indent=2)
    stage("готово; результаты в " + args.work, t0)


if __name__ == "__main__":
    main()
