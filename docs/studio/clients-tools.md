# Экосистема: iOS-клиент, Chrome-расширение и AlignEval

Помимо основного Go-сервиса и `poisq-aligner`, в репозитории находятся три вспомогательных компонента: нативный iOS-клиент, расширение браузера для получения токена Яндекс Музыки и стенд оценки качества выравнивания.

## Нативный iOS-клиент (`app/ios`)

Клиент **Poisq** написан на **Swift 6.0 / SwiftUI (`@Observable`)** под **iOS 26.1** (локализации `en` и `ru`) с единственной внешней зависимостью `AudioStreaming` (`>= 1.4.5`).

### Модули проекта

| Модуль / Таргет | Назначение |
|---|---|
| `PoisqKit/PoisqCore` | Модели (`Song`, `ReplayGain`, `Lyrics`, `Album`, `Artist`, `ArtistInfo`, `Playlist`, `Podcast*`, `RadioStation`), `SubsonicClient` (`v=1.16.1`, `c=poisq`, `f=json`, токен-авторизация и кастомные HTTP-заголовки), автодетектор `Capabilities`, клиентская раскладка `KeyboardLayout`. |
| `PoisqKit/PoisqPlayback` | Протокол `PlaybackEngine`, реализации `AudioStreamingEngine` и `CastEngine`, оркестратор очереди `Player`, `Equalizer`, `NowPlayingBridge` (`MPNowPlayingInfoCenter` / `MPRemoteCommandCenter`), `HomeMix` и `PlayHistoryLog` (до 500 записей). |
| `Poisq` (`app.poisq.ios`) | Интерфейс SwiftUI, настраиваемая главная (`HomeLayout`) и пины (`PinStore`), оффлайн-загрузки, кэши, профили серверов, `CarPlay/`, `Intents/`. |
| `PoisqWidgets` | Расширение WidgetKit без сетевого доступа — читает состояние и обложки из App Group. |
| `Shared` | Общий `SharedStore` в App Group `group.app.poisq.ios`, диплинки `poisq://album|artist|playlist/<id>` и `poisq://player`, базовые `AudioPlaybackIntent`. |

### Ключевые возможности в коде

- **Воспроизведение и DSP**: gapless-очередь, бесконечный shuffle, автоподборка похожих треков (`autoplay` с разведением повторов `Player.spread`), таймер сна с 8-секундным затуханием, `ReplayGain` (`off`/`track`/`album`), DSP-цепочка `gain → AVAudioUnitEQ(16 полос) → Apple PeakLimiter`.
- **Эквалайзер и AutoEq**: 10-полосный графический и до 16 полос параметрический (`peak`, `lowShelf`, `highShelf`), 27 встроенных пресетов, автопереключение по жанру, автоматический `headroom` и **импорт профилей коррекции наушников AutoEq (`ParametricEQ.txt`)**.
- **Синхронизированные тексты**: загрузка через OpenSubsonic `getLyricsBySongId` (fallback `getLyrics`), полноэкранный караоке-режим, перемотка по тапу на строку, перенос длинных слов через `Hyphenator` (`NLLanguageRecognizer` + `CFStringGetHyphenationLocationBeforeIndex`).
- **Оффлайн и кэширование**: фоновые загрузки (`URLSessionConfiguration.background`, лимит места, режим `downloadedOnly` и `autoOffline`), упреждающее кэширование текущего и 2 следующих треков (`StreamCache`, LRU, дефолт 1 ГБ), двухуровневый кэш обложек (`CoverCache`: 250 шт / 100 МБ в памяти + диск).
- **Cast / Music Assistant**: автообнаружение расширения `poisq` в `getOpenSubsonicExtensions` и бесшовное переключение движка между `AudioStreamingEngine` и `CastEngine` с сохранением очереди и позиции.
- **Мультисерверность и Failover**: пароли и кастомные HTTP-заголовки хранятся в **Keychain** (`kSecAttrAccessibleAfterFirstUnlock`); для каждого сервера задаются запасные URL (`alternateURLs`) с автоматическим переключением при обрыве и возвратом на основной адрес.
- **CarPlay, виджеты и Siri**: `CPTabBarTemplate` (5 вкладок) и `CPNowPlayingTemplate`, виджеты `NowPlayingWidget` и `RecentAlbumsWidget`, индексация каталога в Spotlight (`CSSearchableIndex`) и голосовые команды Siri (`PoisqShortcuts`).

---

## Расширение Chrome (`chrome-extension`)

Расширение **Manifest V3** (*Yandex Music Token Grabber*, v1.0.0) автоматизирует получение OAuth-токена Яндекс Музыки для последующей вставки в `poisq Studio → Настройки → Ключи API`.

- **Рабочие домены (`host_permissions`)**: `https://music.yandex.ru/*`, `https://music.yandex.com/*`, `https://oauth.yandex.ru/*`.
- **Принцип работы**:
  1. По нажатию кнопки в popup (`popup/popup.js`) фоновый `service-worker.js` открывает OAuth-авторизацию Яндекса (`services/token-extractor.js`).
  2. Как только отслеживаемая вкладка переходит на доверенный хост (`music.yandex.ru`/`com`, `oauth.yandex.ru`) с параметром `access_token` в `#hash` или `?query`, `service-worker.js` сохраняет токен в `chrome.storage.local` (TTL **14 дней**, `utils/storage.js`), копирует его в буфер обмена, показывает системное уведомление и закрывает вкладку через `1.2s`.
  3. В popup токен не выводится в DOM (хранится только в памяти для повторного копирования и отображается маской `•••`).

---

## Стенд оценки выравнивания (`tools/aligneval`)

Набор инструментов для замеров точности forced alignment на эталонных `.lrc` и сравнения конфигураций моделей:

- **`aligneval.py`** — основной CLI-конвейер (также используется внутри `poisq-aligner`). Поддерживает загрузку эталона из LRCLIB (`--fetch "Исполнитель|Название"`), альтернативный разделитель `audio-separator` (`--separator audio-separator`), выбор набора ASR-вариантов (`--asr-variants`), контекстные проходы Qwen и `MMS_FA` (`--ctx-align`, `--mms-ctx`) и расчёт метрик точности (`median_abs_s`, `p90_abs_s`, `within_0.3s_pct`, `within_0.5s_pct`, `within_1.0s_pct`).
- **`refconsensus.py`** — строит консенсусный эталон `ref_consensus.json` из нескольких пользовательских разметок LRCLIB (выравнивая сдвиги и отбирая строки с `≥ 3` совпадениями и долей согласия `≥ 0.6` в пределах `±0.3s`), чтобы оценивать реальную точность без шума одного разметчика.
- **`batch.sh`** — пакетный прогон контрольного набора из 7 треков через Docker (`LOCAL=0`) или локальное окружение macOS (`LOCAL=1`, с `PYTORCH_ENABLE_MPS_FALLBACK=1`).
