import Foundation
import PoisqCore

/// Закреплённый на главной альбом или плейлист.
struct Pin: Codable, Identifiable, Hashable {
    var album: Album?
    var playlist: Playlist?
    var id: String { album.map { "a:" + $0.id } ?? playlist.map { "p:" + $0.id } ?? UUID().uuidString }
}

/// Закрепление альбомов и плейлистов на главной. Список свой у каждого сервера, хранится на устройстве.
@MainActor @Observable
final class PinStore {
    private(set) var pins: [Pin] = []
    private var serverID: UUID?

    func load(serverID: UUID?) {
        self.serverID = serverID
        guard let serverID, let data = UserDefaults.standard.data(forKey: key(serverID)),
              let list = try? JSONDecoder().decode([Pin].self, from: data) else { pins = []; return }
        pins = list
    }

    func isPinned(album: Album) -> Bool { pins.contains { $0.album?.id == album.id } }
    func isPinned(playlist: Playlist) -> Bool { pins.contains { $0.playlist?.id == playlist.id } }

    func toggle(album: Album) {
        if let i = pins.firstIndex(where: { $0.album?.id == album.id }) { pins.remove(at: i) } else { pins.append(Pin(album: album)) }
        save()
    }
    func toggle(playlist: Playlist) {
        if let i = pins.firstIndex(where: { $0.playlist?.id == playlist.id }) { pins.remove(at: i) } else { pins.append(Pin(playlist: playlist)) }
        save()
    }

    private func key(_ id: UUID) -> String { "pins." + id.uuidString }
    private func save() {
        guard let serverID else { return }
        UserDefaults.standard.set(try? JSONEncoder().encode(pins), forKey: key(serverID))
    }
}
