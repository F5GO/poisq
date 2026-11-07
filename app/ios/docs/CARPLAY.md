# CarPlay

Файлы: `Poisq/CarPlay/` — `CarPlaySceneDelegate.swift` (сцена + `AppModel.shared`), `CarPlayController.swift` (шаблоны), `Poisq.entitlements`.

## Что сделано
- `CPTabBarTemplate`: Недавнее (`getAlbumList2 recent`), Плейлисты, Артисты, Альбомы (А–Я), Избранное (песни). Альбом/плейлист/артист открывают вложенный список; нажатие на песню запускает весь список через `player.play(_:startAt:continuation:)`.
- `CPNowPlayingTemplate`: кнопки «Далее» (очередь → переход к треку), перемешивание, повтор, избранное. Состояние берётся из `Player` (наблюдение `@Observable`), логики очереди в CarPlay нет.
- Обложки — `AppModel.coverImage` (`CoverCache`). Модель общая для телефона и CarPlay: `AppModel.shared`.

## Как включено сейчас
- `UIApplicationSceneManifest` с ролью CarPlay лежит в `project.yml` (→ Info.plist). Без entitlement система сцену не запускает, остальное приложение не затронуто.
- `CODE_SIGN_ENTITLEMENTS[sdk=iphonesimulator*]` подключает `Poisq.entitlements` **только для симулятора** (там подпись не нужна), на устройстве entitlement не подключён — сборка на устройство не требует профиля с CarPlay.

## Что сделать после получения entitlement `com.apple.developer.carplay-audio`
1. Запросить entitlement для App ID `app.poisq.ios` и пересоздать provisioning profile.
2. В `project.yml` заменить строку `"CODE_SIGN_ENTITLEMENTS[sdk=iphonesimulator*]"` на безусловный `CODE_SIGN_ENTITLEMENTS: Poisq/CarPlay/Poisq.entitlements`.
3. `xcodegen generate`, собрать на устройство, проверить в машине/CarPlay-стенде.

## Проверка в симуляторе
Simulator → I/O → External Displays → CarPlay (окно CarPlay). Нужен вход на сервер в телефонной части.
