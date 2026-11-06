# poisq-aligner: акустическое выравнивание текста по аудио

Сервис ставит **точное время каждой строке текста песни** (тайминг для `.lrc`) и сообщает, **насколько можно
верить каждой строке**. Работает рядом с poisq: воркер poisq отправляет сюда аудиофайл и текст, получает готовый
`.lrc` и уверенность по строкам (Studio подсвечивает сомнительные строки в редакторе).

## Как это работает

```
аудио ─► Demucs (вокал) ─► ансамбль Whisper (6 версий, Groq) ─► якоря «что и где спето»
      ─► Qwen3-ForcedAligner в окне «строка + соседи» (контекст) ─► проверка по Whisper
      ─► независимый CTC-выравниватель MMS (арбитраж при расхождении) ─► время + уверенность
```

Ключевые находки (проверено на 7 песнях, 307 строк, эталон — консенсус нескольких разметок LRCLIB):

| | до | после |
|---|---|---|
| медиана ошибки | 0.155 с | **0.08–0.11 с** |
| строк точнее 0.3 с | 69% | **80–88%** |
| строк с ошибкой > 0.5 с | 16% | **7–9%** |

Уверенность по строкам: «высокая» у ~72% строк (из них 96% точнее 0.3 с), «низкая» у ~18% строк, но в них 81%
всех крупных ошибок — именно эти строки стоит проверить в редакторе. Подробности и расчёты: `tools/aligneval/`.

## Быстрый старт (docker compose)

В `docker-compose.yml` сервис описан в профиле `aligner`:

```bash
docker compose --profile aligner up -d
```

и в сервисе `poisq` включите:

```yaml
ALIGNER_URL: "http://aligner:4540"
```

Сервис монтирует медиатеку **только на чтение по тому же пути, что и poisq** (`/music`). Если пути различаются,
задайте у poisq `ALIGNER_MUSIC_PATH` (путь к медиатеке внутри контейнера выравнивания).

Нужен ключ **Groq** (во вкладке «Ключи API» Studio): Whisper работает через Groq Cloud. Ключ берётся из пула poisq
и передаётся сервису на время задачи; сервис его не хранит.

Первый запуск скачивает модели (~4 ГБ: Demucs, Qwen3-ForcedAligner, MMS_FA) в том `/cache`, дальше они берутся оттуда.

## Что ожидать

| Этап | NAS (Xeon E5 без AVX2) | Mac M1 Max |
|---|---|---|
| Demucs (отделение вокала) | 6–7 мин | 30 с (`ALIGNER_DEMUCS_DEVICE=mps`) |
| Whisper ×6 (Groq) | ~30 с | ~30 с |
| Выравнивание Qwen + MMS | 3–6 мин | 2–3 мин |
| **Итого на трек** | **~12–15 мин** (фоновая очередь) | **~3–4 мин** |

Задачи выполняются по одной. Стем вокала и ответы Groq кэшируются (`/cache/work`), повторный запуск по тому же
файлу идёт быстрее. Лимит бесплатного Groq (≈7200 аудио-секунд в час на ключ) позволяет примерно 3 трека в час на
ключ: при исчерпании poisq откладывает задачу на 20 минут. Несколько ключей Groq в пуле распределяют нагрузку.

## HTTP API

| Метод | Путь | Описание |
|---|---|---|
| GET | `/healthz` | `{"ok": true, "queue": N, "running": id\|null}` (без токена) |
| POST | `/v1/jobs` | `{"audio_path", "lines": [...], "lang", "track_id", "groq_key", "fresh"}` → `202 {"id"}` |
| GET | `/v1/jobs/<id>` | `status`: queued / running / done / error / cancelled, `progress`, `result` |
| DELETE | `/v1/jobs/<id>` | отмена |

Результат: `lines[]` (`index`, `text`, `start`, `end`, `lrc_time`, `confidence` high/medium/low, `reason`,
`estimated`), готовый `lrc` и `summary`. Коды ошибок: `quota`, `invalid_key`, `cpu_unsupported`, `bad_request`,
`timeout`, `disk`, `failed`. Строки, которых в записи не нашли, остаются в `.lrc` с оценённым временем и пометкой
`low` (`estimated: true`).

Безопасность: `audio_path` обязан лежать внутри `MUSIC_ROOT` (после раскрытия ссылок), при заданном
`ALIGNER_TOKEN` нужен заголовок `Authorization: Bearer <token>` (poisq отправляет его из своего `ALIGNER_TOKEN`).

## Настройки (переменные окружения сервиса)

| Переменная | По умолчанию | Описание |
|---|---|---|
| `MUSIC_ROOT` | `/music` | корень медиатеки (разрешённые пути) |
| `ALIGNER_CACHE_DIR` | `/cache` | модели, стемы, результаты |
| `ALIGNER_PORT` | `4540` | порт |
| `ALIGNER_TOKEN` | пусто | токен доступа |
| `ALIGNER_THREADS` | 0 (все ядра) | потоки PyTorch; на общем NAS задайте меньше числа ядер |
| `ALIGNER_LEAD` | `0.5` | на сколько секунд раньше писать время строки в `.lrc` |
| `ALIGNER_MAX_SEC` | `1500` | сколько секунд трека обрабатывать |
| `ALIGNER_JOB_TIMEOUT` | `5400` | лимит времени задачи, с |
| `ALIGNER_KEEP_WORKDIRS` | `30` | сколько рабочих папок треков хранить |
| `ALIGNER_ASR_VARIANTS` | `vocals,prompt,mix,chunks,turbo,fast` | версии распознавания Whisper |
| `ALIGNER_DEMUCS_DEVICE` | пусто | `mps` на Apple Silicon |

## Запуск без Docker (разработка)

```bash
python3 -m venv venv && venv/bin/pip install numpy torch torchaudio demucs soundfile requests qwen-asr
MUSIC_ROOT=/путь/к/музыке ALIGNER_CACHE_DIR=./cache venv/bin/python aligner/server.py
cd aligner && python -m unittest discover -s tests   # тесты сервиса
```

Конвейер (`tools/aligneval/aligneval.py`) используется и сервисом, и как исследовательский стенд
(`tools/aligneval/README.md`: метрики, консенсусный эталон из LRCLIB, запуск на Mac).

## Если что-то не работает

- **`cpu_unsupported`** — процессор без нужных инструкций; сообщите модель, будет подобран другой вариант.
- **poisq пишет «сервис выравнивания недоступен»** — проверьте `ALIGNER_URL`, `docker compose --profile aligner ps`
  и `curl http://aligner:4540/healthz` из контейнера poisq. Пока сервис недоступен, poisq использует прежний
  облачный режим (Groq Whisper + Gemini).
- **Задача «ожидает лимит Groq»** — исчерпана квота аудио-секунд; добавьте ещё ключи Groq или подождите.
