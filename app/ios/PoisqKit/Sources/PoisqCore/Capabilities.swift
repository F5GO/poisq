import Foundation

/// Что умеет сервер. Интерфейс спрашивает «есть ли X», а не «какой это сервер».
/// Без poisq всё работает на стандартном Subsonic.
public struct Capabilities: Sendable, Equatable {
    public var isOpenSubsonic = false
    /// Версия расширения `poisq`, объявленная через getOpenSubsonicExtensions. nil — сервер не poisq.
    public var poisqVersion: Int?
    /// Сервер отвечает на `getPodcasts` (иначе раздела «Подкасты» нет).
    public var hasPodcasts = false
    /// Сервер отвечает на `getInternetRadioStations`.
    public var hasRadio = false
    /// Прокси poisq транслирует на колонки (Music Assistant настроен в Studio, есть хотя бы одна колонка).
    public var hasCast = false

    public init(isOpenSubsonic: Bool = false, poisqVersion: Int? = nil) {
        self.isOpenSubsonic = isOpenSubsonic
        self.poisqVersion = poisqVersion
    }

    public static let baseline = Capabilities()
    public var hasPoisq: Bool { poisqVersion != nil }
}
