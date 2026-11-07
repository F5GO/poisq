import CarPlay
import UIKit

/// Сцена CarPlay. Подключается через `UIApplicationSceneManifest` в Info.plist (роль CPTemplateApplicationSceneSessionRoleApplication).
/// Без entitlement `com.apple.developer.carplay-audio` система эту сцену просто не запускает — приложение работает как обычно.
/// Что включить после получения entitlement — см. docs/CARPLAY.md.
@MainActor
final class CarPlaySceneDelegate: UIResponder, CPTemplateApplicationSceneDelegate {
    private var controller: CarPlayController?

    func templateApplicationScene(_ templateApplicationScene: CPTemplateApplicationScene,
                                  didConnect interfaceController: CPInterfaceController) {
        // CarPlay может запустить приложение без телефонного UI, поэтому модель общая и живёт вне SwiftUI-сцены.
        let controller = CarPlayController(model: AppModel.shared, interface: interfaceController)
        self.controller = controller
        controller.start()
    }

    func templateApplicationScene(_ templateApplicationScene: CPTemplateApplicationScene,
                                  didDisconnectInterfaceController interfaceController: CPInterfaceController) {
        controller?.stop()
        controller = nil
    }
}

extension AppModel {
    /// Единственный экземпляр: его делят телефонная сцена (SwiftUI) и сцена CarPlay.
    static let shared = AppModel()
}
