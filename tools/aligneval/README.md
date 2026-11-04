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
