#!/usr/bin/env python3
"""Консенсусный эталон из нескольких записей LRCLIB (разные люди ставили тайминг).

Для каждого трека из lines.json берёт все записи LRCLIB с таймингом и длительностью близкой к основной,
сопоставляет их строки с нашими по тексту (difflib по нормализованным словам) и считает:
  ref_med — медиана времён строки по записям (если записей >= 3),
  ref_n   — сколько записей дали время этой строки,
  ref_agree — доля записей в пределах 0.3 с от медианы (надёжность строки как эталона).
Пишет ref_consensus.json рядом с lines.json: [{line, ref_med, ref_n, ref_agree}].
Использование: refconsensus.py "Исполнитель|Название" путь/к/lines.json [допуск_длительности_с=4]
"""
import difflib
import importlib.util
import json
import os
import re
import statistics
import sys
import urllib.parse
import urllib.request

spec = importlib.util.spec_from_file_location("aligneval", os.path.join(os.path.dirname(os.path.abspath(__file__)), "aligneval.py"))
ae = importlib.util.module_from_spec(spec)
spec.loader.exec_module(ae)  # только определения функций, main() не вызывается


def main():
    artist, title = sys.argv[1].split("|", 1)
    lines_path = sys.argv[2]
    tol = float(sys.argv[3]) if len(sys.argv) > 3 else 4.0
    lines = json.load(open(lines_path, encoding="utf-8"))
    work = os.path.dirname(lines_path)
    primary = ae.parse_lrc(open(os.path.join(work, "lrclib_synced.lrc"), encoding="utf-8").read())
    primary_text = open(os.path.join(work, "lrclib_synced.lrc"), encoding="utf-8").read().strip()

    url = "https://lrclib.net/api/search?" + urllib.parse.urlencode({"artist_name": artist, "track_name": title})
    items = json.loads(urllib.request.urlopen(urllib.request.Request(url, headers={"User-Agent": "poisq-aligneval/1.0"}), timeout=30).read().decode())
    pdur = None
    for i in items:
        if i.get("syncedLyrics") and i["syncedLyrics"].strip() == primary_text:
            pdur = float(i.get("duration") or 0)
            break
    ours = [" ".join(ae.tokens(l["text"])) for l in lines]
    # записи: {номер строки -> время}; основная запись первой
    recs = [{l["line"]: l["ref"] for l in lines if l.get("ref") is not None}]
    for it in items:
        sl = it.get("syncedLyrics")
        if not sl or sl.strip() == primary_text:
            continue
        if pdur and abs(float(it.get("duration") or 0) - pdur) > tol:
            continue
        rec = ae.parse_lrc(sl)
        rec_norm = [" ".join(ae.tokens(t)) for _, t in rec]
        sm = difflib.SequenceMatcher(None, ours, rec_norm, autojunk=False)
        mp = {}
        for tag, i1, i2, j1, j2 in sm.get_opcodes():
            if tag == "equal":
                for k in range(i2 - i1):
                    mp[lines[i1 + k]["line"]] = rec[j1 + k][0]
        if len(mp) >= 0.5 * len(ours):
            recs.append(mp)
    used = len(recs)
    # у каждой записи свой постоянный сдвиг (другая версия трека): убираем его по медиане относительно нашего итога
    fin = {l["line"]: l["final"] for l in lines if l.get("final") is not None}
    norm = []
    for mp in recs:
        diffs = [mp[k] - fin[k] for k in mp if k in fin]
        off = statistics.median(diffs) if diffs else 0.0
        norm.append({k: v - off for k, v in mp.items()})
    out = []
    for l in lines:
        c = [m[l["line"]] for m in norm if l["line"] in m]
        if len(c) >= 3:
            med = statistics.median(c)
            agree = sum(1 for v in c if abs(v - med) <= 0.3) / len(c)
            out.append({"line": l["line"], "ref_med": med, "ref_n": len(c), "ref_agree": agree})
        else:
            out.append({"line": l["line"], "ref_med": None, "ref_n": len(c), "ref_agree": None})
    json.dump(out, open(os.path.join(work, "ref_consensus.json"), "w"), ensure_ascii=False, indent=1)
    good = [o for o in out if o["ref_med"] is not None]
    rel = [o for o in good if o["ref_agree"] >= 0.6]
    print(f"{artist} — {title}: записей использовано {used}; строк с консенсусом {len(good)} из {len(out)}, надёжных (согласие ≥60%) {len(rel)}")


if __name__ == "__main__":
    main()
