import Foundation

public enum EngineState: Sendable, Equatable { case idle, loading, playing, paused }

/// Всё, что нужно плееру от аудиодвижка. Движок можно заменить (AudioStreaming → AVQueuePlayer → свой),
/// интерфейс при этом не меняется.
@MainActor
public protocol PlaybackEngine: AnyObject {
    var state: EngineState { get }
    var currentURL: URL? { get }
    var progress: Double { get }
    var duration: Double { get }

    /// Начать с этого URL; следующие ставятся в очередь для gapless.
    func play(_ url: URL)
    func enqueue(_ url: URL)
    /// Убрать ещё не начавший играть трек из очереди.
    func dequeue(_ url: URL)
    /// Перейти к следующему треку очереди.
    func skipToNext()
    /// Зациклить текущий трек средствами движка.
    func setRepeatOne(_ on: Bool)
    func pause()
    func resume()
    func stop()
    func seek(to seconds: Double)
    /// Громкость движка 0…1 (для плавного затухания таймера сна).
    var volume: Float { get set }
    /// Свои HTTP-заголовки для сетевых запросов стрима (не для файлов на устройстве).
    var requestHeaders: [String: String] { get set }
    /// Эквалайзер: полосы и предусилитель применяются на лету.
    func setEqualizer(_ config: EQConfig)
    /// Выравнивание громкости (ReplayGain) в дБ; 0 — без изменений.
    func setReplayGain(_ db: Float)

    /// Скорость воспроизведения (подкасты) с сохранением высоты голоса. 1 — обычная.
    func setRate(_ rate: Float)
    /// Метаданные потока (ICY `StreamTitle` у радио). nil — убрать обработчик.
    func setMetadataHandler(_ handler: ((String) -> Void)?)

    /// Вызывается, когда трек начал играть (в т.ч. при gapless-переходе).
    var onTrackStarted: ((URL) -> Void)? { get set }
    var onStateChanged: ((EngineState) -> Void)? { get set }
    var onError: ((Error) -> Void)? { get set }
}

/// Движки без поддержки скорости и метаданных остаются рабочими.
public extension PlaybackEngine {
    func setRate(_ rate: Float) {}
    func setMetadataHandler(_ handler: ((String) -> Void)?) {}
}
