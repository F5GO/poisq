# Трансляция на колонки (Music Assistant)

Подсистема Cast (`internal/cast`, `internal/proxy/cast_auth.go`) позволяет музыкальным клиентам, поддерживающим расширение OpenSubsonic `poisq` (версии `1`), отправлять треки и очереди из Navidrome напрямую на колонки и комнаты, управляемые сервером **Music Assistant**.

## Как это работает

1. В **poisq Studio → Настройки → Music Assistant** задаются URL сервера Music Assistant (обычно `:8095`), долгосрочный Bearer-токен (шифруется и хранится в `api_keys` под провайдером `music_assistant`), файловый источник (`filesystem*`), указывающий на ту же папку медиатеки, что и Navidrome, и список разрешённых колонок. Конфигурация сохраняется в таблице `settings` под ключом `"cast"`.
2. Прокси объявляет клиентам поддержку `{"name":"poisq","versions":[1]}` в `getOpenSubsonicExtensions`.
3. Когда клиент обращается к `/rest/poisqCast*`, прокси (`internal/proxy/cast_auth.go`) проверяет его Subsonic-учётные данные (`u`, `t`, `s`, `p`, `apiKey`) запросом `GET /rest/ping` к Navidrome и кэширует валидную сессию на **5 минут** (до 1000 записей).
4. `cast.Service` переводит ID треков Navidrome в URI Music Assistant: читает корни библиотек из `navidrome.db` (`library`) и путь трека (`media_file`), вычисляет относительный путь и собирает `<provider>://track/<relPath>`.
5. Команды отправляются в Music Assistant по HTTP JSON-RPC (`POST <URL>/api`, заголовок `Authorization: Bearer <token>`, таймаут `10s`, лимит ответа `4 МиБ`).

## Эндпоинты `/rest/poisqCast*`

Все маршруты поддерживают `GET`, `POST` (form-urlencoded) и опциональный суффикс `.view`, а ответы возвращаются в стандартном JSON-конверте OpenSubsonic (`version: "1.16.1"`, `type: "poisq"`, `openSubsonic: true`):

| Маршрут | Параметры | Описание |
|---|---|---|
| `/rest/poisqCastZones` | — | Возвращает доступные и включённые колонки из разрешённого списка (`castZones`: `id`, `name`, `state`, `volume`). |
| `/rest/poisqCastPlay` | `zone`, `id` *(повторяемый, до 2000)*, `offset` *(сек, опц.)* | Очищает очередь колонки, немедленно запускает первый трек с позиции `offset` и фоново догружает остаток очереди. |
| `/rest/poisqCastControl` | `zone`, `action`, `position`, `volume`, `id`… | Управляет воспроизведением, громкостью и очередью выбранной зоны. |
| `/rest/poisqCastStatus` | `zone` | Возвращает текущее состояние очереди и колонки (`castStatus`: `state`, `index`, `position`, `volume`). При `state == "playing"` позиция интерполируется по `ElapsedUpdated`. |

### Действия `action` в `poisqCastControl`

| `action` | Команда Music Assistant | Пояснение |
|---|---|---|
| `play`, `pause`, `stop`, `clear`, `next`, `previous` | `player_queues/<action>` | Базовое управление. `stop` и `clear` отменяют фоновую догрузку очереди. |
| `seek` | `player_queues/get` → `player_queues/play_index` | Перемотка текущего индекса на `position` (в секундах). |
| `index` | `player_queues/play_index` | Переход на трек с номером `position` (при необходимости ждёт, пока фоновый хвост догрузит этот индекс). |
| `remove` | `player_queues/delete_item` | Удаляет элемент очереди по `position` (после завершения фоновой догрузки). |
| `add` | `player_queues/play_media` (`option: "add"`) | Дописывает до 2000 треков (`id`…) в конец очереди. |
| `volume` | `players/cmd/volume_set` | Устанавливает громкость колонки (`volume` ограничивается диапазоном `0..100`). |

## Фоновая догрузка длинных очередей

Чтобы воспроизведение на колонке начиналось мгновенно даже для плейлистов из сотен треков (`internal/cast/service.go`):
- В `Play` синхронно отправляется только **первый трек** (`player_queues/clear` → `play_media` → `play_index`).
- Остальные треки (до `maxCastQueueIDs = 2000`) дописываются отдельной фоновой горутиной `appendTail` чанками по **5 треков** (`tailChunk = 5`, таймаут `2m`).
- Per-zone блокировки и синхронизация с хвостом гарантируют, что новый `Play`, `stop`/`clear`, `add`/`remove` или переход по `index` не вступят в гонку с фоновой догрузкой.

## Коды ошибок Subsonic

| Код | Причина |
|---|---|
| `0` | Cast не настроен (`ErrNotConfigured`) или ошибка связи с Music Assistant. |
| `10` | Отсутствует обязательный параметр (`zone`, `id`, `action`). |
| `40` | Ошибка авторизации Subsonic (`Wrong username or password`). |
| `70` | Неизвестная колонка (`ErrUnknownZone`) или неподдерживаемый метод. |
