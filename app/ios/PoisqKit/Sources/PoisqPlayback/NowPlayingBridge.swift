#if os(iOS)
import Foundation
import AVFoundation
import MediaPlayer
import UIKit
import PoisqCore

/// Связывает Player с системой: аудиосессия, экран блокировки, пульт, наушники, CarPlay-кнопки.
@MainActor
final class NowPlayingBridge {
    private weak var player: Player?
    var artworkProvider: ((Song) async -> UIImage?)?
    private var artworkTask: Task<Void, Never>?

    init(player: Player) {
        self.player = player
        registerCommands()
        // Категорию ставим сразу, без активации (чужой звук не прерывается): окно AirPlay показывает устройства
        // по категории сессии, а его могут открыть до первого воспроизведения.
        // longFormAudio — как в «Музыке»: без него приложению на AVAudioEngine (AudioStreaming) система не предлагает AirPlay 2.
        try? AVAudioSession.sharedInstance().setCategory(.playback, mode: .default, policy: .longFormAudio)
    }

    /// Без категории .playback звук глохнет при блокировке экрана и выключенном «беззвучном» режиме не работает.
    /// Активируем каждый раз: пока звук идёт на колонку, телефон молчит, и iOS может выключить сессию, а окну AirPlay нужна активная.
    func activateSession() {
        do {
            try AVAudioSession.sharedInstance().setCategory(.playback, mode: .default, policy: .longFormAudio)
            try AVAudioSession.sharedInstance().setActive(true)
        } catch {
            // Воспроизведение всё равно попробуем начать; без сессии звук может пропасть в фоне.
        }
    }

    func update() {
        guard let player, let song = player.current else {
            MPNowPlayingInfoCenter.default().nowPlayingInfo = nil
            MPNowPlayingInfoCenter.default().playbackState = .stopped
            return
        }
        var info: [String: Any] = [
            MPMediaItemPropertyTitle: song.title,
            MPMediaItemPropertyPlaybackDuration: player.duration > 0 ? player.duration : Double(song.duration ?? 0),
            MPNowPlayingInfoPropertyElapsedPlaybackTime: player.progress,
            MPNowPlayingInfoPropertyPlaybackRate: player.isPlaying ? 1.0 : 0.0,
        ]
        if let artist = song.artist { info[MPMediaItemPropertyArtist] = artist }
        if let album = song.album { info[MPMediaItemPropertyAlbumTitle] = album }
        // Сохраняем уже загруженную обложку при обновлении времени.
        if let art = MPNowPlayingInfoCenter.default().nowPlayingInfo?[MPMediaItemPropertyArtwork] { info[MPMediaItemPropertyArtwork] = art }
        MPNowPlayingInfoCenter.default().nowPlayingInfo = info
        MPNowPlayingInfoCenter.default().playbackState = player.isPlaying ? .playing : .paused
    }

    func loadArtwork(for song: Song) {
        artworkTask?.cancel()
        MPNowPlayingInfoCenter.default().nowPlayingInfo?[MPMediaItemPropertyArtwork] = nil
        guard let provider = artworkProvider else { return }
        artworkTask = Task { [weak self] in
            guard let image = await provider(song), !Task.isCancelled, let self, self.player?.current == song else { return }
            let artwork = Self.makeArtwork(image)
            MPNowPlayingInfoCenter.default().nowPlayingInfo?[MPMediaItemPropertyArtwork] = artwork
        }
    }

    /// Обработчик обложки система вызывает на фоновой очереди. Если создать замыкание внутри MainActor-класса,
    /// оно унаследует изоляцию и приложение упадёт на проверке очереди, поэтому создаём его вне изоляции.
    nonisolated private static func makeArtwork(_ image: UIImage) -> MPMediaItemArtwork {
        MPMediaItemArtwork(boundsSize: image.size) { _ in image }
    }

    private func registerCommands() {
        let c = MPRemoteCommandCenter.shared()
        c.playCommand.addTarget { [weak self] _ in self.run { $0.resume() } }
        c.pauseCommand.addTarget { [weak self] _ in self.run { $0.pause() } }
        c.togglePlayPauseCommand.addTarget { [weak self] _ in self.run { $0.togglePlayPause() } }
        c.nextTrackCommand.addTarget { [weak self] _ in self.run { $0.next() } }
        c.previousTrackCommand.addTarget { [weak self] _ in self.run { $0.previous() } }
        c.changePlaybackPositionCommand.addTarget { [weak self] event in
            guard let e = event as? MPChangePlaybackPositionCommandEvent else { return .commandFailed }
            return self.run { $0.seek(to: e.positionTime) }
        }
    }
}

private extension Optional where Wrapped == NowPlayingBridge {
    /// Команды приходят не с главного потока — переносим на MainActor.
    func run(_ action: @MainActor @escaping (Player) -> Void) -> MPRemoteCommandHandlerStatus {
        guard let bridge = self else { return .commandFailed }
        Task { @MainActor in if let p = bridge.playerForCommands { action(p) } }
        return .success
    }
}

extension NowPlayingBridge {
    fileprivate var playerForCommands: Player? { player }
}
#endif
