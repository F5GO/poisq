import Foundation
import Network

/// Состояние сети устройства: есть ли соединение и не мобильная ли это (платная) сеть.
@MainActor @Observable
final class NetworkMonitor {
    private(set) var isConnected = true
    /// Мобильная сеть или режим экономии трафика — тяжёлые фоновые закачки лучше не начинать.
    private(set) var isExpensive = false
    /// Вызывается при любом изменении состояния сети.
    var onChange: (() -> Void)?

    private let monitor = NWPathMonitor()

    init() {
        monitor.pathUpdateHandler = { [weak self] path in
            let connected = path.status == .satisfied
            let expensive = path.isExpensive || path.isConstrained
            Task { @MainActor in
                guard let self else { return }
                let changed = self.isConnected != connected || self.isExpensive != expensive
                self.isConnected = connected
                self.isExpensive = expensive
                if changed { self.onChange?() }
            }
        }
        monitor.start(queue: DispatchQueue(label: "app.poisq.network"))
    }
}
