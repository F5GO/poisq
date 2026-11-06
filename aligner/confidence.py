"""Оценка уверенности по строкам и сборка результата выравнивания.

Конвейер (tools/aligneval/aligneval.py) выдаёт для каждой строки несколько независимых оценок времени старта:
ансамбль Whisper (asr), Qwen в одиночном окне (qwen), Qwen с контекстом (ctx), независимый CTC-выравниватель MMS
(ctxm) и прежний итог до контекстного прохода (pre). Расхождение между ними служит оценкой уверенности:
на семи проверочных треках строки, где оценки расходятся более чем на 0.8 с, составляли ~28% и содержали ~61%
всех крупных ошибок (>0.5 с). Этот модуль превращает такие данные в уровень уверенности (high/medium/low),
оценивает время для строк, которых в записи не нашли, и формирует итоговый LRC.

Модуль не зависит от тяжёлых библиотек и покрыт тестами (aligner/tests).
"""
import re
import statistics

MMS_LOW = 0.5         # итог дальше этого (с) от независимого выравнивателя MMS — «low»
MMS_MEDIUM = 0.3      # дальше этого — «medium»
STRONG_SPREAD = 0.8   # разброс сильных оценок (ctx, ctxm, pre) выше этого — не выше «medium»
FALLBACK_LOW = 1.2    # без MMS: разброс всех оценок выше этого — «low», выше FALLBACK_MEDIUM — «medium»
FALLBACK_MEDIUM = 0.6
EST_LINE_SEC = 2.5    # длительность строки без соседей (оценка времени для не найденных строк)


def tokens(line):
    """Те же слова, что использует конвейер (нижний регистр, ё=е, \\w+)."""
    return re.findall(r"\w+", line.lower().replace("ё", "е"))


def kept_lines(lines):
    """[(исходный индекс, текст)] только для строк, в которых есть слова (так их видит конвейер)."""
    return [(i, t.strip()) for i, t in enumerate(lines) if tokens(t)]


def fmt_lrc_time(sec):
    t = max(0, int(round(sec * 100)))
    return "%02d:%02d.%02d" % (t // 6000, (t % 6000) // 100, t % 100)


def classify(d, repeats=1):
    """(уровень, причина) для строки из lines.json конвейера.

    Главный признак — расстояние между итоговым временем и оценкой независимого выравнивателя MMS: на пяти
    проверочных треках (209 строк, консенсус нескольких разметок) при пороге 0.5 с строк «low» было 18%,
    и в них 81% всех крупных ошибок (>0.5 с); среди «high» (72% строк) ошибки >0.5 с составили 1.3%,
    а 96% строк оказались точнее 0.3 с."""
    if not d.get("found"):
        return "low", "нет в записи: время оценено по соседним строкам"
    if d.get("filled"):
        return "low", "строка не распознана, выровнена вслепую"
    final, mms = d.get("final"), d.get("ctxm")
    level, reasons = "high", []

    def raise_to(new, why):
        nonlocal level
        order = {"high": 0, "medium": 1, "low": 2}
        if order[new] > order[level]:
            level = new
        reasons.append(why)

    if final is not None and mms is not None:
        gap = abs(final - mms)
        if gap > MMS_LOW:
            raise_to("low", "второй, независимый выравниватель расходится на %.1f с" % gap)
        elif gap > MMS_MEDIUM:
            raise_to("medium", "второй выравниватель расходится на %.1f с" % gap)
        if repeats >= 3 and gap > MMS_MEDIUM:
            raise_to("low", "строка повторяется в песне %d раз, а оценки расходятся" % repeats)
        strong = [d[k] for k in ("ctx", "ctxm", "pre") if d.get(k) is not None]
        if len(strong) >= 2 and max(strong) - min(strong) > STRONG_SPREAD:
            raise_to("medium", "оценки времени расходятся на %.1f с" % (max(strong) - min(strong)))
    else:  # MMS недоступен для строки: оцениваем по разбросу всех оценок (менее точный признак)
        est = [d[k] for k in ("asr", "qwen", "ctx", "pre") if d.get(k) is not None]
        spread = (max(est) - min(est)) if len(est) >= 2 else 0.0
        if spread > FALLBACK_LOW:
            raise_to("low", "оценки времени расходятся на %.1f с" % spread)
        elif spread > FALLBACK_MEDIUM:
            raise_to("medium", "оценки времени расходятся на %.1f с" % spread)
    if "MMS" in (d.get("status") or ""):
        raise_to("medium", "время выбрано арбитражем двух выравнивателей")
    if d.get("stretch_fixed"):
        raise_to("medium", "начало строки скорректировано по концу фразы")
    sup, of = d.get("support"), d.get("of")
    if sup is not None and of and sup / of < 0.5:
        raise_to("medium", "строку услышали %d из %d версий распознавания" % (sup, of))
    return level, "; ".join(dict.fromkeys(reasons))


def estimate_missing(times, texts):
    """Заполняет время (start, end) для строк, которых не нашли: равномерно по длине текста между
    ближайшими найденными соседями. times — список [start|None, end|None]; изменяется на месте."""
    n = len(times)
    i = 0
    while i < n:
        if times[i][0] is not None:
            i += 1
            continue
        j = i
        while j + 1 < n and times[j + 1][0] is None:
            j += 1
        weights = [max(1, len(tokens(texts[k]))) for k in range(i, j + 1)]
        total_w = float(sum(weights))
        prev_end = times[i - 1][1] if i > 0 else None
        next_start = times[j + 1][0] if j + 1 < n else None
        if prev_end is not None and next_start is not None and next_start - prev_end > 0.2 * len(weights):
            lo, hi = prev_end + 0.1, next_start - 0.1
        elif next_start is not None:
            hi = next_start - 0.3
            lo = max(0.0, hi - EST_LINE_SEC * len(weights))
        elif prev_end is not None:
            lo = prev_end + 0.3
            hi = lo + EST_LINE_SEC * len(weights)
        else:
            lo, hi = 0.0, EST_LINE_SEC * len(weights)
        span = max(hi - lo, 0.1 * len(weights))
        pos = lo
        for w, k in zip(weights, range(i, j + 1)):
            seg = span * w / total_w
            times[k] = [pos, pos + seg]
            pos += seg
        i = j + 1
    return times


def _median_gap(dump):
    gaps = [abs(d["final"] - d["ctxm"]) for d in dump if d.get("found") and d.get("final") is not None and d.get("ctxm") is not None]
    return round(statistics.median(gaps), 3) if gaps else 0.0


def build_result(lines, dump, lead=0.5):
    """Собирает ответ сервиса.

    lines — исходные строки текста (как прислал клиент); dump — lines.json конвейера для строк с буквами
    (в том же порядке). lead — на сколько секунд раньше стартов писать время в LRC (так строка
    появляется чуть заранее, как в караоке). Возвращает словарь:
      lines  — по каждой строке с буквами: index (исходный), text, start, end, lrc_time, confidence, reason, estimated
      lrc    — готовый текст .lrc
      summary — счётчики уверенности
    """
    kept = kept_lines(lines)
    if len(kept) != len(dump):
        raise ValueError("конвейер вернул %d строк, ожидалось %d" % (len(dump), len(kept)))
    texts = [t for _, t in kept]
    counts = {}
    for d in dump:
        counts[d["text"]] = counts.get(d["text"], 0) + 1

    times = [[d.get("final"), d.get("end")] if d.get("found") else [None, None] for d in dump]
    estimate_missing(times, texts)

    out, prev_start = [], -1.0
    for k, (d, (orig_i, text)) in enumerate(zip(dump, kept)):
        start, end = times[k]
        level, reason = classify(d, counts.get(d["text"], 1))
        estimated = not d.get("found")
        if start < prev_start:  # порядок строк в LRC обязан быть неубывающим
            start = prev_start + 0.01
            end = max(end, start + 0.2)
            if level != "low":
                level, reason = "low", (reason + "; " if reason else "") + "порядок времени нарушен, строка сдвинута"
        prev_start = start
        out.append({
            "index": orig_i, "text": text, "start": round(start, 3), "end": round(end, 3),
            "lrc_time": round(max(0.0, start - lead), 3), "confidence": level, "reason": reason, "estimated": estimated,
        })

    lrc = "\n".join("[%s]%s" % (fmt_lrc_time(x["lrc_time"]), x["text"]) for x in out)
    n = len(out)
    low = sum(1 for x in out if x["confidence"] == "low")
    summary = {
        "lines": n,
        "high": sum(1 for x in out if x["confidence"] == "high"),
        "medium": sum(1 for x in out if x["confidence"] == "medium"),
        "low": low,
        "estimated": sum(1 for x in out if x["estimated"]),
        "low_ratio": round(low / n, 3) if n else 0.0,
        "median_gap_to_mms": _median_gap(dump),
    }
    return {"lines": out, "lrc": lrc, "summary": summary}
