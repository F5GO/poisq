import SwiftUI
import Observation
import PoisqCore
import PoisqPlayback

extension PodcastEpisode {
    /// Выпуск как песня для общего плеера: id — `streamId`, поэтому работает обычный `stream`.
    func asSong(channelTitle: String?) -> Song? {
        guard let streamId else { return nil }
        return Song(id: streamId, title: title, artist: channelTitle, album: channelTitle, coverArt: coverArt,
                    duration: duration, suffix: suffix, size: size, contentType: contentType)
    }
}

/// Локальные данные подкастов: место, где остановились, и отметка «прослушано». Сервер этого не хранит.
@MainActor @Observable
final class PodcastStore {
    static let shared = PodcastStore()
    private(set) var positions: [String: Double]
    private(set) var listened: Set<String>
    @ObservationIgnored private var trackingTask: Task<Void, Never>?

    private init() {
        positions = (UserDefaults.standard.dictionary(forKey: "podcastPositions") as? [String: Double]) ?? [:]
        listened = Set(UserDefaults.standard.stringArray(forKey: "podcastListened") ?? [])
    }

    private func save() {
        UserDefaults.standard.set(positions, forKey: "podcastPositions")
        UserDefaults.standard.set(Array(listened), forKey: "podcastListened")
    }

    func position(of e: PodcastEpisode) -> Double { e.streamId.flatMap { positions[$0] } ?? 0 }
    func isListened(_ e: PodcastEpisode) -> Bool { e.streamId.map(listened.contains) ?? false }

    func setListened(_ value: Bool, _ e: PodcastEpisode) {
        guard let id = e.streamId else { return }
        if value { listened.insert(id); positions[id] = nil } else { listened.remove(id) }
        save()
    }

    /// Запускает выпуск с запомненного места (прослушанный — сначала).
    func play(_ e: PodcastEpisode, channelTitle: String?, player: Player) {
        guard let song = e.asSong(channelTitle: channelTitle) else { return }
        let start = isListened(e) ? 0 : position(of: e)
        if isListened(e) { setListened(false, e) }
        player.playSpoken([song], position: start)
        track(player)
    }

    /// Раз в несколько секунд сохраняет место, пока играет подкаст; в конце выпуска ставит «прослушано».
    private func track(_ player: Player) {
        trackingTask?.cancel()
        trackingTask = Task { [weak self, weak player] in
            while !Task.isCancelled {
                try? await Task.sleep(for: .seconds(4))
                guard let self, let player else { return }
                guard player.isSpoken, let id = player.current?.id, player.isPlaying else { continue }
                let total = player.duration, at = player.progress
                if total > 60, at >= total - 20 {
                    listened.insert(id); positions[id] = nil
                } else if at > 5 {
                    positions[id] = at
                }
                save()
            }
        }
    }
}

/// Доступные скорости речи.
let speechRates: [Float] = [0.75, 1, 1.25, 1.5, 1.75, 2]
func rateLabel(_ r: Float) -> String { String(format: "%g×", r) }

func formatEpisodeDuration(_ seconds: Int) -> String {
    let f = DateComponentsFormatter()
    f.allowedUnits = seconds >= 3600 ? [.hour, .minute] : [.minute]
    f.unitsStyle = .abbreviated
    return f.string(from: TimeInterval(max(seconds, 60))) ?? ""
}
