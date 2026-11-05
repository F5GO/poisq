#!/usr/bin/env bash
# Пакетный прогон aligneval по нескольким трекам (запускать на сервере: sudo bash batch.sh).
# Для каждого трека своя рабочая папка (кэш Demucs сохраняется), текст берётся из LRCLIB.
# Результат: /tmp/aligneval/batch/NN/{run.log,aligned_asr_anchor.lrc} и сводка в конце.
MUSIC="${MUSIC:-/mnt/tank/BIGDATA/Media/Music}"
OUT=/tmp/aligneval/batch
mkdir -p "$OUT" /tmp/aligneval/cache

# путь относительно каталога музыки | исполнитель | название
TRACKS=(
"Король и Шут/Король и Шут - Будь как дома, Путник/07 - Лесник.flac|Король и Шут|Лесник"
"Linkin Park - 2025 - From Zero (Deluxe) (24bit-48kHz)/02 The Emptiness Machine.flac|Linkin Park|The Emptiness Machine"
"Grebz/Grebz - Юрфак.flac|Grebz|Юрфак"
"Nickelback/Nickelback - The Best of Nickelback, Vol. 1/02 - How You Remind Me.flac|Nickelback|How You Remind Me"
"Radiohead/Radiohead - Pablo Honey/02 - Creep.flac|Radiohead|Creep"
"Валентин Стрыкало/Валентин Стрыкало - Смирись и расслабься/10 - Наше лето.flac|Валентин Стрыкало|Наше лето"
"Gorillaz/Gorillaz - Demon Days/06 - Feel Good Inc.mp3|Gorillaz|Feel Good Inc."
)

n=0
for entry in "${TRACKS[@]}"; do
  n=$((n+1)); d=$(printf "%s/%02d" "$OUT" "$n"); mkdir -p "$d"
  IFS='|' read -r rel artist title <<<"$entry"
  echo "=== [$n/${#TRACKS[@]}] $artist — $title ($(date +%H:%M:%S))"
  docker run --rm --env-file /tmp/aligneval/groq.env -v "$MUSIC":/music:ro -v "$d":/work \
    -v /tmp/aligneval/cache:/root/.cache aligneval "/music/$rel" --fetch "$artist|$title" \
    --variants vocals --asr-anchor --only-anchor > "$d/run.log" 2>&1 \
    || echo "   ОШИБКА, см. $d/run.log"
done

echo; echo "################ СВОДКА ################"
for d in "$OUT"/*/; do
  echo "--- $d"
  grep -E "^\[lrclib\]|найдено в записи|метрики|для сравнения|эталон —|НЕТ В ЗАПИСИ|голоса нет|заполнено|Traceback|Error|error" "$d/run.log" | cut -c1-420
  tail -n 3 "$d/run.log" | cut -c1-300
done
