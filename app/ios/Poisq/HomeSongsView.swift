import SwiftUI
import PoisqCore
import PoisqPlayback

/// Порядок списка песен на главной.
enum SongsOrder: String, CaseIterable, Identifiable {
    case random, az, za
    var id: String { rawValue }
    var title: LocalizedStringKey {
        switch self { case .random: "Random"; case .az: "A to Z"; case .za: "Z to A" }
    }
}

/// Список песен и его подгрузка. Отдельный объект, чтобы очередь плеера могла просить у него продолжение.
@MainActor @Observable
final class SongsFeedModel {
    var songs: [Song] = []
    var finished = false
    var failed = false
    /// Параметры, с которыми список загружен: при возврате на экран тот же список не сбрасываем.
    var key = ""
    private var seen: Set<String> = []
    private var loading = false
    /// Сколько песен списка уже отдано в очередь плеера.
    private var queuedUpTo = 0
    private let batch = 60

    func reset() { songs = []; seen = []; finished = false; failed = false; queuedUpTo = 0 }

    /// Случайная партия без повторов. Когда сервер несколько раз подряд не отдаёт ничего нового — песни кончились.
    func loadMore(app: AppModel, order: SongsOrder) async {
        guard !loading, !finished else { return }
        loading = true
        defer { loading = false }
        if app.offline {                                      // без сети — загруженное, один раз
            songs = Self.sorted(app.downloadedSongs, order)
            seen = Set(songs.map(\.id)); finished = true
            return
        }
        if order != .random {                                 // по алфавиту: нужна вся медиатека, порядок сервера не гарантирован
            guard let client = app.client else { return }
            var all: [Song] = []
            do {
                while all.count < 30_000 {
                    let page = try await client.allSongs(offset: all.count, count: 500, musicFolderId: app.selectedLibraryID)
                    all += page
                    if page.count < 500 { break }
                }
                failed = false
            } catch { failed = !Self.isCancelled(error) }
            songs = Self.sorted(all, order)
            finished = true
            return
        }
        var fresh: [Song] = []
        var misses = 0
        while fresh.count < 20, misses < 3 {
            guard let client = app.client else { return }
            do {
                let got = try await client.randomSongs(count: batch, musicFolderId: app.selectedLibraryID)
                let new = got.filter { seen.insert($0.id).inserted }
                fresh += new
                misses = new.isEmpty ? misses + 1 : 0
                failed = false
            } catch {
                // Уход с экрана отменяет задачу — это не ошибка загрузки.
                if Self.isCancelled(error) { break }
                failed = true
                if songs.isEmpty && fresh.isEmpty { finished = true }
                break
            }
        }
        songs += fresh
        if misses >= 3 { finished = true }
    }

    private static func isCancelled(_ error: Error) -> Bool {
        Task.isCancelled || error is CancellationError || (error as? URLError)?.code == .cancelled
    }

    static func sorted(_ list: [Song], _ order: SongsOrder) -> [Song] {
        switch order {
        case .random: list.shuffled()
        case .az: list.sorted { $0.title.localizedStandardCompare($1.title) == .orderedAscending }
        case .za: list.sorted { $0.title.localizedStandardCompare($1.title) == .orderedDescending }
        }
    }

    /// Запуск с песни `index`: в очередь уходит порция списка, дальше плеер просит продолжение у `next`.
    func start(at index: Int, player: Player, app: AppModel, order: SongsOrder) {
        let first = Array(songs[index...].prefix(100))
        queuedUpTo = index + first.count
        player.play(first, startAt: 0, continuation: { [weak self] in
            await self?.next(app: app, order: order) ?? []
        })
    }

    /// Следующая порция списка для очереди: при необходимости подгружаем ещё. Пусто — список кончился.
    private func next(app: AppModel, order: SongsOrder) async -> [Song] {
        if queuedUpTo >= songs.count, !finished { await loadMore(app: app, order: order) }
        guard queuedUpTo < songs.count else { return [] }
        let part = Array(songs[queuedUpTo...].prefix(100))
        queuedUpTo += part.count
        return part
    }
}

/// Режим «Песни» на главной: только список песен. Прокручиваем вниз — подгружаются новые, пока не кончатся.
struct SongsFeed: View {
    @Environment(AppModel.self) private var model
    @AppStorage("homeSongsSort") private var orderRaw = SongsOrder.random.rawValue
    @State private var feed = SongsFeedModel()
    private var order: SongsOrder { SongsOrder(rawValue: orderRaw) ?? .random }

    var body: some View {
        LazyVStack(alignment: .leading, spacing: 0) {
            ForEach(Array(feed.songs.enumerated()), id: \.element.id) { index, song in
                let isCurrent = model.player?.current?.id == song.id
                Button {
                    if let player = model.player { feed.start(at: index, player: player, app: model, order: order) }
                } label: {
                    HStack(spacing: 8) {
                        MediaRow(coverID: song.coverArt, title: song.title, subtitle: song.displayArtist, highlighted: isCurrent)
                        Spacer(minLength: 0)
                        if isCurrent {
                            Image(systemName: "waveform").foregroundStyle(.tint)
                                .symbolEffect(.variableColor.iterative, isActive: model.player?.isPlaying == true)
                        }
                    }
                    .padding(.horizontal, 20).padding(.vertical, 6)
                    .background(isCurrent ? Color.accentColor.opacity(0.10) : .clear)
                    .contentShape(.rect)       // нажимается вся строка, а не только название
                }
                .buttonStyle(.plain)
                .songMenu(song)
                .onAppear { if index >= feed.songs.count - 12 { Task { await feed.loadMore(app: model, order: order) } } }
            }
            footer
        }
        .task(id: model.contentKey + "|\(model.offline)|\(model.selectedLibraryID ?? "")|\(orderRaw)") {
            let key = model.contentKey + "|\(model.offline)|\(model.selectedLibraryID ?? "")|\(orderRaw)"
            if feed.key == key, !feed.songs.isEmpty { return }       // вернулись с другого экрана — список уже загружен
            feed.reset(); feed.key = key
            await feed.loadMore(app: model, order: order)
        }
    }

    @ViewBuilder private var footer: some View {
        if feed.failed && feed.songs.isEmpty {
            ContentUnavailableView("Couldn't load", systemImage: "wifi.exclamationmark")
        } else if feed.finished && !feed.songs.isEmpty {
            Text("That's everything").font(.footnote).foregroundStyle(.secondary)
                .frame(maxWidth: .infinity).padding(.vertical, 24)
        } else if feed.finished {
            ContentUnavailableView("No Songs", systemImage: "music.note")
        } else {
            ProgressView().frame(maxWidth: .infinity).padding(.vertical, 24)
        }
    }
}
