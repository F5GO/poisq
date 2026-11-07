import Foundation
import AVFoundation
import AudioToolbox
import AudioStreaming

@MainActor
public final class AudioStreamingEngine: NSObject, PlaybackEngine {
    private let player = AudioPlayer()
    public private(set) var state: EngineState = .idle
    public private(set) var currentURL: URL?

    public var onTrackStarted: ((URL) -> Void)?
    public var onStateChanged: ((EngineState) -> Void)?
    public var onError: ((Error) -> Void)?

    public var progress: Double { player.progress }
    public var duration: Double { player.duration }

    /// Эквалайзер (до 16 полос) и пик-лимитер после него: даже при сильных усилениях звук не клиппует.
    private let eq = AVAudioUnitEQ(numberOfBands: 16)
    /// Отдельный узел выравнивания громкости: работает независимо от эквалайзера.
    private let gain = AVAudioUnitEQ(numberOfBands: 1)
    private let limiter = AVAudioUnitEffect(audioComponentDescription: AudioComponentDescription(
        componentType: kAudioUnitType_Effect, componentSubType: kAudioUnitSubType_PeakLimiter,
        componentManufacturer: kAudioUnitManufacturer_Apple, componentFlags: 0, componentFlagsMask: 0))

    public override init() {
        super.init()
        player.delegate = self
        eq.bypass = true
        limiter.bypass = true
        gain.bypass = true
        gain.bands[0].bypass = true
        player.attach(nodes: [gain, eq, limiter])
    }

    public var volume: Float {
        get { player.volume }
        set { player.volume = newValue }
    }

    public func setReplayGain(_ db: Float) {
        gain.globalGain = min(max(db, -24), 24)
        gain.bypass = abs(db) < 0.05
    }

    public func setEqualizer(_ config: EQConfig) {
        eq.bypass = !config.enabled
        limiter.bypass = !config.enabled
        eq.globalGain = min(max(config.preampDb, -24), 24)
        for (i, band) in eq.bands.enumerated() {
            guard i < config.bands.count else { band.bypass = true; continue }
            let b = config.bands[i]
            switch b.kind {
            case .peak: band.filterType = .parametric
            case .lowShelf: band.filterType = .lowShelf
            case .highShelf: band.filterType = .highShelf
            }
            band.frequency = b.frequency
            band.bandwidth = Self.bandwidthOctaves(q: b.q)
            band.gain = min(max(b.gain, -24), 24)
            band.bypass = false
        }
    }

    /// Ширина полосы в октавах по добротности Q: BW = 2·asinh(1/(2Q))/ln 2.
    nonisolated static func bandwidthOctaves(q: Float) -> Float {
        let q = max(q, 0.1)
        return min(max(2 * asinh(1 / (2 * q)) / Float(M_LN2), 0.05), 5)
    }

    public var requestHeaders: [String: String] = [:]
    private func headers(for url: URL) -> [String: String] { url.isFileURL ? [:] : requestHeaders }

    public func play(_ url: URL) {
        update(.loading)
        currentURL = url
        player.play(url: url, headers: headers(for: url))
    }

    public func setRate(_ rate: Float) { player.rate = min(max(rate, 0.5), 2) }
    public func setMetadataHandler(_ handler: ((String) -> Void)?) { metadataHandler = handler }
    private var metadataHandler: ((String) -> Void)?

    public func enqueue(_ url: URL) { player.queue(url: url, headers: headers(for: url)) }
    public func dequeue(_ url: URL) { player.removeFromQueue(url: url) }
    public func skipToNext() { player.playNextInQueue() }
    public func setRepeatOne(_ on: Bool) { player.loopMode = on ? .single(times: nil) : .off }
    public func pause() { player.pause() }
    public func resume() { player.resume() }
    public func seek(to seconds: Double) { player.seek(to: seconds) }

    public func stop() {
        player.stop()
        currentURL = nil
        update(.idle)
    }

    fileprivate func update(_ new: EngineState) {
        guard new != state else { return }
        state = new
        onStateChanged?(new)
    }
}

// Колбэки AudioStreaming приходят не с главного потока — переносим на MainActor.
extension AudioStreamingEngine: AudioPlayerDelegate {
    nonisolated public func audioPlayerDidStartPlaying(player: AudioPlayer, with entryId: AudioEntryId) {
        let id = entryId.id
        Task { @MainActor in
            guard let url = URL(string: id) else { return }
            self.currentURL = url
            self.onTrackStarted?(url)
        }
    }
    nonisolated public func audioPlayerDidFinishBuffering(player: AudioPlayer, with entryId: AudioEntryId) {}
    nonisolated public func audioPlayerStateChanged(player: AudioPlayer, with newState: AudioPlayerState, previous: AudioPlayerState) {
        let mapped: EngineState? = switch newState {
        case .playing: .playing
        case .bufferring, .running, .ready: .loading
        case .paused: .paused
        case .stopped, .disposed, .error: .idle
        }
        guard let mapped else { return }
        Task { @MainActor in self.update(mapped) }
    }
    nonisolated public func audioPlayerDidFinishPlaying(player: AudioPlayer, entryId: AudioEntryId, stopReason: AudioPlayerStopReason, progress: Double, duration: Double) {}
    nonisolated public func audioPlayerUnexpectedError(player: AudioPlayer, error: AudioPlayerError) {
        Task { @MainActor in self.onError?(error) }
    }
    nonisolated public func audioPlayerDidCancel(player: AudioPlayer, queuedItems: [AudioEntryId]) {}
    nonisolated public func audioPlayerDidReadMetadata(player: AudioPlayer, metadata: [String: String]) {
        // ICY: поле StreamTitle обычно «Исполнитель - Название».
        let title = metadata.first { $0.key.lowercased().contains("streamtitle") }?.value
            ?? metadata.first { $0.key.lowercased() == "title" }?.value
        guard let title, !title.isEmpty else { return }
        Task { @MainActor in self.metadataHandler?(title) }
    }
}
