import SwiftUI
import PoisqCore
import PoisqPlayback

/// Станция на главной: нажатие сразу включает поток треков, который не останавливается.
enum HomeStation: Hashable, Identifiable, Sendable {
    case myFlow
    case similar(Song)
    case genre(String)
    case newest

    var id: String {
        switch self {
        case .myFlow: "flow"
        case .similar(let song): "similar-\(song.id)"
        case .genre(let name): "genre-\(name)"
        case .newest: "newest"
        }
    }

    var title: String {
        switch self {
        case .myFlow: String(localized: "My Flow")
        case .similar(let song): String(localized: "Similar to \(song.title)")
        case .genre(let name): name
        case .newest: String(localized: "New in Library")
        }
    }

    var icon: String {
        switch self {
        case .myFlow: "infinity"
        case .similar: "wand.and.stars"
        case .genre: "guitars.fill"
        case .newest: "sparkles"
        }
    }

    /// Цвет дуотона: картинка у станции меняется вместе со списком, а цвет всегда говорит, что это за станция.
    var tint: Color {
        switch self {
        case .myFlow: .indigo
        case .similar: .teal
        case .genre(let name):
            // Стабильный цвет для жанра между запусками (hashValue меняется от запуска к запуску).
            [Color.orange, .green, .purple, .brown][name.unicodeScalars.reduce(0) { $0 + Int($1.value) } % 4]
        case .newest: .pink
        }
    }
}

/// «Мой поток»: любимые порциями вперемешку с похожими на них. Когда любимые кончились — дальше автоподборка плеера.
@MainActor
private final class FlowSource {
    private var remaining: [Song]
    private let client: SubsonicClient
    /// Последний отданный трек: следующая порция не начинается с его альбома.
    private var last: Song?

    init(favorites: [Song], excluding taken: [Song], client: SubsonicClient) {
        let takenIDs = Set(taken.map(\.id))
        remaining = favorites.filter { !takenIDs.contains($0.id) }.shuffled()
        self.client = client
        last = taken.last
    }

    func next() async -> [Song] {
        let favorites = Array(remaining.prefix(6))
        remaining.removeFirst(favorites.count)
        guard let seed = favorites.randomElement() else { return [] }
        let similar = (try? await client.similarSongs(id: seed.id, count: 12)) ?? []
        let part = HomeMix.quickPicks(similar: similar, favorites: favorites, random: [], limit: 18, after: last)
        if let tail = part.last { last = tail }
        return part
    }
}

enum HomeStarter {
    /// Первая порция станции. Готовится заранее, чтобы на карточке была обложка первого трека, а звук начинался сразу.
    nonisolated static func prepare(_ station: HomeStation, client: SubsonicClient, folder: String?, favorites: [Song]) async -> [Song] {
        switch station {
        case .myFlow:
            // Любимые часто лежат целыми альбомами: перемешивания мало, разводим соседей по альбомам и исполнителям.
            if !favorites.isEmpty { return Array(HomeMix.station(favorites.shuffled()).prefix(8)) }
            return HomeMix.station((try? await client.randomSongs(count: 40, musicFolderId: folder)) ?? [])
        case .similar(let song):
            var list = (try? await client.similarSongs(id: song.id, count: 40)) ?? []
            // Похожие сервер находит не всегда (Navidrome без Last.fm отдаёт пусто) — добираем лучшими треками
            // исполнителя и случайными того же жанра, чтобы станция не оставалась пустой.
            if list.count < 10 {
                let artist = song.artist, genre = song.genre
                async let top: [Song] = artist == nil ? [] : ((try? await client.topSongs(artist: artist!, count: 20)) ?? [])
                async let sameGenre: [Song] = genre == nil ? []
                    : ((try? await client.randomSongs(count: 30, genre: genre, musicFolderId: folder)) ?? [])
                list += await top + sameGenre
            }
            var seen: Set<String> = [song.id]
            return HomeMix.station(list.filter { seen.insert($0.id).inserted }.shuffled(), after: song)
        case .genre(let name):
            return HomeMix.station((try? await client.randomSongs(count: 40, genre: name, musicFolderId: folder)) ?? [])
        case .newest:
            let albums = (try? await client.albumList(type: "newest", size: 8, musicFolderId: folder)) ?? []
            let pool = await withTaskGroup(of: [Song].self) { group in
                for album in albums {
                    group.addTask { (try? await client.album(id: album.id).songs) ?? [] }
                }
                var all: [Song] = []
                for await songs in group { all += songs }
                return all
            }
            return Player.spread(pool.shuffled(), after: nil, maxPerAlbum: 3, limit: 60)
        }
    }

    /// Запускает станцию: готовый список, если он есть, иначе собирает сейчас. false — источник пуст, музыка не менялась.
    @MainActor
    static func start(_ station: HomeStation, app: AppModel, favorites: [Song], prepared: [Song]?) async -> Bool {
        guard let player = app.player, let client = app.client else { return false }
        let folder = app.selectedLibraryID
        var songs = prepared ?? []
        if songs.isEmpty { songs = await prepare(station, client: client, folder: folder, favorites: favorites) }
        guard !songs.isEmpty else { return false }
        switch station {
        case .myFlow where !favorites.isEmpty:
            let flow = FlowSource(favorites: favorites, excluding: songs, client: client)
            player.play(songs, continuation: { await flow.next() })
        case .genre(let name):
            player.play(songs, continuation: { [weak player] in
                let more = (try? await client.randomSongs(count: 40, genre: name, musicFolderId: folder)) ?? []
                return HomeMix.station(more, after: player?.queue.last)
            })
        default:
            // Пустое продолжение: плеер сам переходит к автоподборке или случайным трекам.
            player.play(songs, continuation: { [] })
        }
        return true
    }
}

/// Строка трека как в «Быстром выборе»: крупная обложка, название и исполнитель, справа меню.
struct QuickPickRow: View {
    @Environment(AppModel.self) private var model
    let song: Song
    let play: () -> Void

    var body: some View {
        let isCurrent = model.player?.current?.id == song.id
        HStack(spacing: 8) {
            Button(action: play) {
                HStack(spacing: 12) {
                    CoverArtView(id: song.coverArt, size: 52, radius: 6)
                    VStack(alignment: .leading, spacing: 3) {
                        Text(song.title).font(.body).lineLimit(1)
                            .foregroundStyle(isCurrent ? AnyShapeStyle(.tint) : AnyShapeStyle(.primary))
                        Text(song.displayArtist ?? "").font(.subheadline).foregroundStyle(.secondary).lineLimit(1)
                    }
                    Spacer(minLength: 0)
                }
                .contentShape(.rect)
            }
            .buttonStyle(.plain)
            if isCurrent {
                Image(systemName: "waveform").foregroundStyle(.tint)
                    .symbolEffect(.variableColor.iterative, isActive: model.player?.isPlaying == true)
            }
        }
        .frame(minHeight: 76)
        .songMenu(song)
    }
}

/// Колонки по три элемента для горизонтальных страниц.
func pagesOfThree<T>(_ items: [T]) -> [[(offset: Int, element: T)]] {
    let all = Array(items.enumerated())
    return stride(from: 0, to: all.count, by: 3).map { Array(all[$0..<min($0 + 3, all.count)]) }
}

/// Заголовок блока главной: название, стрелка, если блок открывается целиком, и подпись.
struct HomeSectionHeader: View {
    let title: LocalizedStringKey
    var subtitle: LocalizedStringKey?
    var open: (() -> Void)?

    var body: some View {
        let label = VStack(alignment: .leading, spacing: 2) {
            HStack(spacing: 4) {
                Text(title).font(.title2.bold())
                if open != nil {
                    Image(systemName: "chevron.right").font(.title3.weight(.semibold)).foregroundStyle(.secondary)
                }
            }
            if let subtitle { Text(subtitle).font(.subheadline).foregroundStyle(.secondary) }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .contentShape(.rect)
        Group {
            if let open {
                Button(action: open) { label }.buttonStyle(.plain)
            } else {
                label
            }
        }
        .padding(.horizontal, 20)
    }
}

/// Горизонтальные страницы по три строки: следующая страница выглядывает справа — видно, что список листается.
struct ThreeRowPages<Item, Row: View>: View {
    let items: [Item]
    @ViewBuilder let row: (Item, Int) -> Row

    var body: some View {
        ScrollView(.horizontal, showsIndicators: false) {
            LazyHStack(alignment: .top, spacing: 12) {
                let pages = pagesOfThree(items)
                ForEach(pages.indices, id: \.self) { page in
                    VStack(spacing: 0) {
                        ForEach(pages[page], id: \.offset) { item in row(item.element, item.offset) }
                    }
                    .containerRelativeFrame(.horizontal) { width, _ in width - 36 }
                }
            }
            .scrollTargetLayout()
        }
        .contentMargins(.horizontal, 20, for: .scrollContent)
        .scrollTargetBehavior(.viewAligned)
    }
}

/// Треки страницами по три: «Быстрый выбор», недавние, новые. Нажатие играет с этого трека, дальше — весь список и автоподборка.
struct TrackPagesSection: View {
    @Environment(AppModel.self) private var model
    let title: LocalizedStringKey
    var subtitle: LocalizedStringKey?
    let songs: [Song]
    var openAll: (() -> Void)?

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            HomeSectionHeader(title: title, subtitle: subtitle, open: openAll)
            ThreeRowPages(items: songs) { song, index in
                QuickPickRow(song: song) { play(at: index) }
            }
        }
    }

    private func play(at index: Int) {
        guard songs.indices.contains(index) else { return }
        model.player?.play(songs, startAt: index, continuation: { [] })
    }
}

/// Блок треков целиком — по нажатию на заголовок.
struct TrackList: View {
    @Environment(AppModel.self) private var model
    let title: LocalizedStringKey
    let songs: [Song]

    var body: some View {
        List {
            ForEach(Array(songs.enumerated()), id: \.element.id) { index, song in
                QuickPickRow(song: song) { model.player?.play(songs, startAt: index, continuation: { [] }) }
            }
        }
        .listStyle(.plain)
        .navigationTitle(title)
    }
}

/// Недавние альбомы в том же виде, что и треки: страницы по три строки. Строка открывает альбом, кнопка справа — играет.
struct AlbumPagesSection: View {
    @Environment(AppModel.self) private var model
    let title: LocalizedStringKey
    let albums: [Album]
    @State private var loadingID: String?

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            HomeSectionHeader(title: title)
            ThreeRowPages(items: albums) { album, _ in
                HStack(spacing: 8) {
                    NavigationLink(value: album) {
                        HStack(spacing: 12) {
                            CoverArtView(id: album.coverArt, size: 52, radius: 6)
                            VStack(alignment: .leading, spacing: 3) {
                                Text(album.name).font(.body).lineLimit(1)
                                Text(album.artist ?? "").font(.subheadline).foregroundStyle(.secondary).lineLimit(1)
                            }
                            Spacer(minLength: 0)
                        }
                        .contentShape(.rect)
                    }
                    .buttonStyle(.plain)
                    Button { play(album) } label: {
                        Group {
                            if loadingID == album.id { ProgressView().controlSize(.small) } else { Image(systemName: "play.fill") }
                        }
                        .foregroundStyle(.secondary)
                        .frame(width: 44, height: 44)
                        .contentShape(.rect)
                    }
                    .buttonStyle(.plain)
                    .disabled(loadingID != nil)
                    .accessibilityLabel("Play")
                }
                .frame(minHeight: 76)
                .albumMenu(album)
            }
        }
    }

    private func play(_ album: Album) {
        loadingID = album.id
        Task {
            let local = model.downloadedSongs.filter { $0.albumId == album.id }.sorted { ($0.track ?? 0) < ($1.track ?? 0) }
            let songs = model.offline ? local : ((try? await model.client?.album(id: album.id).songs) ?? local)
            if !songs.isEmpty { model.player?.play(songs) }
            loadingID = nil
        }
    }
}

/// Станции: карточка — кнопка запуска, а не переход в список.
struct StationShelf: View {
    @Environment(AppModel.self) private var model
    let stations: [HomeStation]
    let favorites: [Song]
    let prepared: [String: [Song]]
    /// Станция запущена — её список израсходован, главная готовит новый.
    let onStarted: (HomeStation) -> Void
    @State private var loadingID: String?
    @State private var failures = 0

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text("Stations").font(.title2.bold()).padding(.horizontal, 20)
            ScrollView(.horizontal, showsIndicators: false) {
                LazyHStack(spacing: 12) {
                    ForEach(stations) { station in
                        Button { start(station) } label: {
                            StationCard(station: station, coverID: prepared[station.id]?.first?.coverArt,
                                        loading: loadingID == station.id)
                        }
                        .buttonStyle(.plain)
                        .disabled(loadingID != nil)
                        .accessibilityLabel(station.title)
                        .accessibilityHint("Starts playing")
                    }
                }
                .scrollTargetLayout()
            }
            .contentMargins(.horizontal, 20, for: .scrollContent)
            .scrollTargetBehavior(.viewAligned)
        }
        .sensoryFeedback(.error, trigger: failures)
    }

    private func start(_ station: HomeStation) {
        guard loadingID == nil else { return }
        loadingID = station.id
        Task {
            let started = await HomeStarter.start(station, app: model, favorites: favorites, prepared: prepared[station.id])
            loadingID = nil
            if started { onStarted(station) } else { failures += 1 }
        }
    }
}

/// Карточка станции: фон — обложка первого трека дуотоном в цвет станции со стеклянной кромкой;
/// иконка типа, название и запуск лежат поверх без собственных подложек.
private struct StationCard: View {
    let station: HomeStation
    let coverID: String?
    let loading: Bool

    var body: some View {
        ZStack {
            Group {
                if let coverID {
                    CoverArtView(id: coverID, size: 150, radius: 0)
                        .grayscale(1)
                        .contrast(1.25)
                        .brightness(0.05)
                        .overlay(Rectangle().fill(station.tint).blendMode(.multiply))
                } else {
                    Rectangle().fill(station.tint.gradient)      // список ещё готовится
                }
            }
            // Лёгкое затемнение снизу под подписью: прозрачное стекло само не держит контраст на светлых обложках.
            LinearGradient(colors: [.clear, .black.opacity(0.35)], startPoint: .center, endPoint: .bottom)
                .allowsHitTesting(false)
            VStack(alignment: .leading, spacing: 0) {
                Image(systemName: station.icon).font(.title3.weight(.semibold))
                Spacer(minLength: 0)
                HStack(alignment: .bottom, spacing: 6) {
                    Text(station.title)
                        .font(.headline)
                        .lineLimit(2)
                        .multilineTextAlignment(.leading)
                    Spacer(minLength: 0)
                    Group {
                        if loading {
                            ProgressView().controlSize(.small)
                        } else {
                            Image(systemName: "play.fill").font(.subheadline.weight(.bold))
                        }
                    }
                    .frame(width: 22, height: 22)
                }
            }
            .padding(14)
            .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .leading)
            .shadow(color: .black.opacity(0.25), radius: 3, y: 1)
        }
        .foregroundStyle(.white)
        .frame(width: 150, height: 150)
        .clipShape(.rect(cornerRadius: 20))
        // Системное стекло всегда размывает то, что под ним, — поверх обложки от неё оставалось пятно
        // (чёткой она была только при нажатии, когда стекло отключается). Поэтому обложка чёткая,
        // а от стекла — светлая кромка с бликом сверху.
        .overlay {
            RoundedRectangle(cornerRadius: 20)
                .strokeBorder(LinearGradient(colors: [.white.opacity(0.45), .white.opacity(0.08), .white.opacity(0.2)],
                                             startPoint: .topLeading, endPoint: .bottomTrailing), lineWidth: 1)
        }
        .contentShape(.rect(cornerRadius: 20))
    }
}

/// Чипы источников над карточкой плеера: каждый сразу включает музыку.
struct SourceChips: View {
    let actions: [HomeQuickAction]
    /// Сессии нет — первый чип («Поток») выделен как главный способ начать слушать.
    let emphasizeFirst: Bool
    let busy: HomeQuickAction?
    let isEnabled: (HomeQuickAction) -> Bool
    let run: (HomeQuickAction) -> Void

    var body: some View {
        ScrollView(.horizontal, showsIndicators: false) {
            HStack(spacing: 8) {
                ForEach(Array(actions.enumerated()), id: \.element) { index, action in
                    chip(action, prominent: emphasizeFirst && index == 0)
                }
            }
            // Анимация только внутри ряда: чип встаёт на своё место, а сам ряд не «прилетает» при загрузке главной.
            .animation(.snappy, value: actions)
        }
        .contentMargins(.horizontal, 20, for: .scrollContent)
        .scrollClipDisabled()
    }

    @ViewBuilder private func chip(_ action: HomeQuickAction, prominent: Bool) -> some View {
        let label = HStack(spacing: 6) {
            if busy == action {
                ProgressView().controlSize(.small)
            } else {
                Image(systemName: action.icon)
            }
            Text(action.title)
        }
        .font(.subheadline.weight(.semibold))
        .frame(height: 36)
        .padding(.horizontal, 4)
        Group {
            if prominent {
                Button { run(action) } label: { label }.buttonStyle(.glassProminent)
            } else {
                Button { run(action) } label: { label }.buttonStyle(.glass)
            }
        }
        .buttonBorderShape(.capsule)
        .disabled(!isEnabled(action) || busy != nil)
    }
}
