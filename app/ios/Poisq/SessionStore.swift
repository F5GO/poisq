import Foundation
import PoisqCore
import PoisqPlayback

/// Файлы приложения в Application Support: сессия плеера и очередь неотправленных прослушиваний.
enum AppFiles {
    static let dir: URL = {
        let d = FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask)[0]
        try? FileManager.default.createDirectory(at: d, withIntermediateDirectories: true)
        return d
    }()
    static func session(_ serverID: UUID) -> URL { dir.appending(path: "session-\(serverID.uuidString).json") }
    static let scrobbles = dir.appending(path: "scrobbles.json")
}

/// Сессия плеера на устройстве: что играло, на каком месте, режимы shuffle и повтора.
enum SessionStore {
    static func save(_ snapshot: PlayerSnapshot, serverID: UUID) {
        try? JSONEncoder().encode(snapshot).write(to: AppFiles.session(serverID), options: .atomic)
    }
    static func load(serverID: UUID) -> PlayerSnapshot? {
        guard let data = try? Data(contentsOf: AppFiles.session(serverID)) else { return nil }
        return try? JSONDecoder().decode(PlayerSnapshot.self, from: data)
    }
    static func delete(serverID: UUID) { try? FileManager.default.removeItem(at: AppFiles.session(serverID)) }
}

/// Очередь прослушиваний, которые ещё не дошли до сервера (например, песни, прослушанные офлайн).
/// Отправка идёт по порядку, с исходным временем начала трека.
@MainActor
final class ScrobbleQueue {
    struct Item: Codable { let serverID: UUID; let songID: String; let time: Date }
    private(set) var items: [Item] = []
    private var flushing = false

    init() {
        if let data = try? Data(contentsOf: AppFiles.scrobbles), let list = try? JSONDecoder().decode([Item].self, from: data) { items = list }
    }

    func add(serverID: UUID, songID: String, time: Date) {
        items.append(Item(serverID: serverID, songID: songID, time: time))
        save()
    }

    func removeAll(serverID: UUID) { items.removeAll { $0.serverID == serverID }; save() }

    /// Отправляет накопленное. Обрыв сети — останавливаемся и пробуем позже; отказ сервера на конкретную песню — пропускаем её.
    func flush(client: SubsonicClient, serverID: UUID) async {
        guard !flushing else { return }
        flushing = true
        defer { flushing = false }
        while let item = items.first(where: { $0.serverID == serverID }) {
            do { try await client.scrobble(songID: item.songID, time: item.time, submission: true) }
            catch is URLError { return }
            catch { /* сервер не принял (песни нет и т. п.) — повторять бессмысленно */ }
            if let i = items.firstIndex(where: { $0.serverID == item.serverID && $0.songID == item.songID && $0.time == item.time }) { items.remove(at: i) }
            save()
        }
    }

    private func save() { try? JSONEncoder().encode(items).write(to: AppFiles.scrobbles, options: .atomic) }
}
