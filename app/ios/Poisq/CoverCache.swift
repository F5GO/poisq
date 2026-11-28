// CoverCache — двухуровневый кеш обложек (память + диск) с синхронным чтением из памяти и предзагрузкой при запуске.
import UIKit
import CryptoKit
import os

/// Кеш обложек: память + диск. Ключ не зависит от токена в URL, поэтому кеш переживает перезапуск.
final class CoverCache: @unchecked Sendable {
    struct Result: Sendable {
        let image: UIImage?
        let fromCache: Bool
    }

    private struct State {
        var headers: [String: String] = [:]
        var inflight: [String: Task<Result, Never>] = [:]
        var recentKeys: [String] = []
        var flushScheduled = false
    }

    private static let recentDefaultsKey = "coverCache.recentKeys"
    private static let maxRecentKeys = 36

    private let memory = NSCache<NSString, UIImage>()
    private let lock = OSAllocatedUnfairLock(initialState: State())
    private let dir: URL = {
        let d = FileManager.default.urls(for: .cachesDirectory, in: .userDomainMask)[0].appending(path: "covers", directoryHint: .isDirectory)
        try? FileManager.default.createDirectory(at: d, withIntermediateDirectories: true)
        return d
    }()

    init() {
        memory.countLimit = 250
        memory.totalCostLimit = 100 << 20     // обложки хранятся распакованными: 900 px ≈ 3 МБ, без лимита по объёму 200 штук — до 650 МБ
        let savedRecent = UserDefaults.standard.stringArray(forKey: Self.recentDefaultsKey) ?? []
        if !savedRecent.isEmpty {
            lock.withLock { $0.recentKeys = savedRecent }
            prewarm(keys: savedRecent)
        }
    }

    /// Свои HTTP-заголовки активного сервера.
    func setHeaders(_ h: [String: String]) {
        lock.withLock { $0.headers = h }
    }

    /// Мгновенное чтение из памяти без переключения потоков (для первого кадра SwiftUI).
    func cachedImage(key: String) -> UIImage? {
        memory.object(forKey: key as NSString)
    }

    func image(key: String, url: @Sendable @escaping () throws -> URL) async -> UIImage? {
        await imageResult(key: key, url: url).image
    }

    func imageResult(key: String, url: @Sendable @escaping () throws -> URL) async -> Result {
        let headers = lock.withLock { $0.headers }
        return await imageResult(key: key) {
            var req = URLRequest(url: try url())
            headers.forEach { req.setValue($1, forHTTPHeaderField: $0) }
            let (data, resp) = try await URLSession.shared.data(for: req)
            guard (resp as? HTTPURLResponse)?.statusCode == 200 else { throw URLError(.badServerResponse) }
            return data
        }
    }

    /// Обложка из произвольного источника (у защищённого профиля — через шифрованный канал).
    func imageResult(key: String, fetch: @Sendable @escaping () async throws -> Data) async -> Result {
        if let hit = memory.object(forKey: key as NSString) {
            recordRecent(key)
            return Result(image: hit, fromCache: true)
        }

        let (task, isOwner) = lock.withLock { state -> (Task<Result, Never>, Bool) in
            if let existing = state.inflight[key] { return (existing, false) }
            let file = dir.appending(path: Self.fileName(key))
            let created = Task<Result, Never>(priority: .userInitiated) {
                // Декодируем здесь, вне главного потока: иначе каждая новая обложка распаковывается при отрисовке и греет CPU при прокрутке.
                if let data = try? Data(contentsOf: file, options: .mappedIfSafe), let img = UIImage(data: data) {
                    let prepared = await img.byPreparingForDisplay() ?? img
                    return Result(image: prepared, fromCache: true)
                }
                guard let data = try? await fetch(), let img = UIImage(data: data) else {
                    return Result(image: nil, fromCache: false)
                }
                try? data.write(to: file, options: .atomic)
                let prepared = await img.byPreparingForDisplay() ?? img
                return Result(image: prepared, fromCache: false)
            }
            state.inflight[key] = created
            return (created, true)
        }

        let result = await task.value
        if isOwner {
            lock.withLock { $0.inflight[key] = nil }
        }
        if let img = result.image {
            memory.setObject(img, forKey: key as NSString, cost: Self.cost(of: img))
            recordRecent(key)
        }
        return result
    }

    /// Фоновый прогрев обложек с диска в память (используется при холодном запуске).
    func prewarm(keys: [String]) {
        guard !keys.isEmpty else { return }
        let dir = self.dir
        let memory = self.memory
        Task.detached(priority: .userInitiated) {
            for key in keys {
                if memory.object(forKey: key as NSString) != nil { continue }
                let file = dir.appending(path: Self.fileName(key))
                guard let data = try? Data(contentsOf: file, options: .mappedIfSafe),
                      let img = UIImage(data: data) else { continue }
                let prepared = await img.byPreparingForDisplay() ?? img
                memory.setObject(prepared, forKey: key as NSString, cost: Self.cost(of: prepared))
            }
        }
    }

    func clear() {
        memory.removeAllObjects()
        lock.withLock { state in
            state.inflight.values.forEach { $0.cancel() }
            state.inflight.removeAll()
            state.recentKeys.removeAll()
            state.flushScheduled = false
        }
        UserDefaults.standard.removeObject(forKey: Self.recentDefaultsKey)
        try? FileManager.default.removeItem(at: dir)
        try? FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
    }

    private func recordRecent(_ key: String) {
        let shouldSchedule = lock.withLock { state -> Bool in
            if state.recentKeys.first == key { return false }
            state.recentKeys.removeAll { $0 == key }
            state.recentKeys.insert(key, at: 0)
            if state.recentKeys.count > Self.maxRecentKeys {
                state.recentKeys.removeLast(state.recentKeys.count - Self.maxRecentKeys)
            }
            if state.flushScheduled { return false }
            state.flushScheduled = true
            return true
        }
        guard shouldSchedule else { return }
        Task.detached(priority: .background) { [weak self] in
            try? await Task.sleep(for: .seconds(2))
            guard let self else { return }
            let snapshot = self.lock.withLock { state -> [String] in
                state.flushScheduled = false
                return state.recentKeys
            }
            UserDefaults.standard.set(snapshot, forKey: Self.recentDefaultsKey)
        }
    }

    private static func cost(of img: UIImage) -> Int {
        Int(img.size.width * img.size.height * img.scale * img.scale * 4)
    }

    private static func fileName(_ key: String) -> String {
        SHA256.hash(data: Data(key.utf8)).map { String(format: "%02x", $0) }.joined()
    }
}
