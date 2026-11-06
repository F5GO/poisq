# aligneval — стенд сравнения выравнивания текста по аудио

Измеряет, насколько точно модель выравнивания (Qwen3-ForcedAligner) попадает в начало строк по сравнению
с эталонным человеческим LRC. Варианты: `vocals` (после отделения вокала Demucs) и `mix` (весь микс).
Это инструмент разработчика, к сервису poisq он не подключён.

## Запуск (TrueNAS)

```bash
# 1. один раз: собрать образ (папку tools/aligneval нужно скопировать на сервер)
sudo docker build -t aligneval /tmp/aligneval/script

# 2. запустить на треке (эталон — .lrc рядом с треком)
MUSIC="/mnt/tank/BIGDATA/Media/Music"
TRACK="Исполнитель/Альбом/01 - Название"
mkdir -p /tmp/aligneval/cache /tmp/aligneval/work-1
sudo docker run --rm -v "$MUSIC":/music:ro -v /tmp/aligneval/work-1:/work -v /tmp/aligneval/cache:/root/.cache \
  aligneval "/music/$TRACK.flac" "/music/$TRACK.lrc"
```

Результаты в рабочей папке: `report.json`, `aligned_vocals.lrc`, `aligned_mix.lrc`.
Ограничения: обрабатываются первые 295 секунд трека (лимит модели 300 сек на вызов), эталонный LRC должен быть
для той же записи.

## Запуск на Mac (Apple Silicon), без Docker

Быстрее NAS: Demucs на GPU Apple (MPS) делает трек за ~30 с, выравнивание Qwen на CPU ~30 с.

```bash
python3.10 -m venv ~/aligneval-mac/venv        # нужен Python 3.10+ (например brew install python@3.10)
~/aligneval-mac/venv/bin/pip install numpy torch torchaudio demucs soundfile requests qwen-asr
# На macOS 26+ (бета) бинарные модули новых scipy не грузятся (ошибка dlopen __thread_bss): ставим scipy 1.13.1
~/aligneval-mac/venv/bin/pip install "scipy==1.13.1"
~/aligneval-mac/venv/bin/pip uninstall -y scikit-learn   # transformers его не требует, он тянет scipy.sparse
```

Ключ Groq кладётся в `~/.config/aligneval/groq.env` строкой `GROQ_API_KEY=...` (права 600).

```bash
# один трек
PYTORCH_ENABLE_MPS_FALLBACK=1 ~/aligneval-mac/venv/bin/python aligneval.py "/Volumes/BIGDATA/Media/Music/Исполнитель/Альбом/01 - Название.flac" \
  --fetch "Исполнитель|Название" --work ~/aligneval-mac/work-1 --variants vocals --asr-anchor --only-anchor \
  --demucs-device mps --asr-variants vocals,mix,chunks,turbo,fast --fuse mode --ctx-align
# пакет из 7 треков (сводка в конце; ONLY и SEP_ARGS работают так же)
LOCAL=1 ONLY="2 4 7" SEP_ARGS="--demucs-device mps --asr-variants vocals,mix,chunks,turbo,fast --fuse mode --ctx-align" bash batch.sh
```
