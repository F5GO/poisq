import SwiftUI
import PoisqCore
import PoisqPlayback

@main
struct PoisqApp: App {
    @UIApplicationDelegateAdaptor(AppDelegate.self) private var appDelegate
    @State private var model = AppModel.shared

    init() { SystemIntegration.shared.start() }      // виджеты, Live Activity, Spotlight, команды Siri

    var body: some Scene {
        WindowGroup {
            RootView()
                .environment(model)
        }
    }
}
