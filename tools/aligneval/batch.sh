#!/usr/bin/env bash
# Пакетный прогон aligneval по нескольким трекам.
# На сервере (Docker): sudo bash batch.sh.  На Mac без Docker: LOCAL=1 bash batch.sh (см. LOCAL ниже).
# Для каждого трека своя рабочая папка (кэш Demucs сохраняется), текст берётся из LRCLIB.
# Результат: /tmp/aligneval/batch/NN/{run.log,aligned_asr_anchor.lrc} и сводка в конце.
# LOCAL=1 — запуск напрямую через Python из окружения $PY (по умолчанию ~/aligneval-mac/venv), без Docker:
#   MUSIC по умолчанию /Volumes/BIGDATA/Media/Music (SMB), OUT ~/aligneval-mac/batch,
#   ключ Groq читается из файла $GROQ_ENV (по умолчанию ~/.config/aligneval/groq.env, строка GROQ_API_KEY=...)
LOCAL="${LOCAL:-0}"
if [ "$LOCAL" = "1" ]; then
  MUSIC="${MUSIC:-/Volumes/BIGDATA/Media/Music}"
  OUT="${OUT:-$HOME/aligneval-mac/batch}"
  PY="${PY:-$HOME/aligneval-mac/venv/bin/python}"
  GROQ_ENV="${GROQ_ENV:-$HOME/.config/aligneval/groq.env}"
  HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
  export PYTORCH_ENABLE_MPS_FALLBACK=1
  [ -f "$GROQ_ENV" ] && { set -a; . "$GROQ_ENV"; set +a; }
else
  MUSIC="${MUSIC:-/mnt/tank/BIGDATA/Media/Music}"
  OUT="${OUT:-/tmp/aligneval/batch}"
fi
# ONLY="2 4 7" — только эти номера треков из списка ниже
# дополнительные аргументы aligneval (например: SEP_ARGS="--separator audio-separator --sep-model vocals_mel_band_roformer.ckpt")
SEP_ARGS="${SEP_ARGS:-}"
mkdir -p "$OUT"
[ "$LOCAL" = "1" ] || mkdir -p /tmp/aligneval/cache

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
  n=$((n+1))
  # ONLY="2 4 7" — прогнать только перечисленные номера (экономит лимиты Groq)
  if [ -n "${ONLY:-}" ] && ! echo " $ONLY " | grep -q " $n "; then continue; fi
  d=$(printf "%s/%02d" "$OUT" "$n"); mkdir -p "$d"
  IFS='|' read -r rel artist title <<<"$entry"
  echo "=== [$n/${#TRACKS[@]}] $artist — $title ($(date +%H:%M:%S))"
  if [ "$LOCAL" = "1" ]; then
    "$PY" "$HERE/aligneval.py" "$MUSIC/$rel" --work "$d" --fetch "$artist|$title" \
      --variants vocals --asr-anchor --only-anchor $SEP_ARGS > "$d/run.log" 2>&1 \
      || echo "   ОШИБКА, см. $d/run.log"
  else
    docker run --rm --env-file /tmp/aligneval/groq.env -v "$MUSIC":/music:ro -v "$d":/work \
      -v /tmp/aligneval/cache:/root/.cache aligneval "/music/$rel" --fetch "$artist|$title" \
      --variants vocals --asr-anchor --only-anchor $SEP_ARGS > "$d/run.log" 2>&1 \
      || echo "   ОШИБКА, см. $d/run.log"
  fi
done

echo; echo "################ СВОДКА ################"
for d in "$OUT"/*/; do
  echo "--- $d"
  grep -a -E "^\[lrclib\]|найдено в записи|контекстное выравнивание|итоговое время|распознавание «|^\[asr-anchor\] метрики|СРАВНЕНИЕ ВАРИАНТОВ|защита старта|^   (whisper|qwen|hybrid|final|auto|med3|ctx|ctx2|ctx_gated|ctx2_gated|OUT|med4|guard|asr:|fused|fz:)|Traceback|Error:" "$d/run.log" | cut -c1-400
  grep -a -A8 "общий сдвиг эталона" "$d/run.log" | cut -c1-160
done
