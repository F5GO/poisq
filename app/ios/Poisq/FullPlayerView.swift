import SwiftUI
import AVKit
import PoisqCore
import PoisqPlayback

private enum PlayerSheet: String, Identifiable {
    case info
    var id: String { rawValue }
}

/// Полноэкранный плеер: обложка, прогресс, управление. Открывается из мини-плеера.
struct FullPlayerView: View {
    let player: Player
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss
    @State private var sheet: PlayerSheet?
    @State private var scrollY: CGFloat = 0
    /// Самое сильное оттягивание вниз за текущее касание: по нему решаем, сворачивать ли плеер.
    @State private var pull: CGFloat = 0
    @State private var editing = false
    @State private var pagerID: String?
    @State private var showEqualizer = false
    @State private var showCast = false
    @State private var lyricsModel = LyricsModel()
    @State private var lyricsOnCover = false
    @State private var lyricsFullScreen = false
    @AppStorage("lyricsBlur") private var lyricsBlur = true

    var body: some View {
        GeometryReader { geo in
            let side = max(min(geo.size.width - 32, geo.size.height - 400, 520), 220)
            let pageHeight = max(geo.size.height - 30, 520)       // первый «экран» плеера; ниже по прокрутке идёт очередь
            ScrollViewReader { proxy in
                List {
                    playerPage(side: side, height: pageHeight, proxy: proxy).id("page")
                        .listRowInsets(EdgeInsets()).listRowSeparator(.hidden).listRowBackground(Color.clear)
                    Color.clear.frame(height: 1).id("queue")
                        .listRowInsets(EdgeInsets()).listRowSeparator(.hidden).listRowBackground(Color.clear)
                    QueueSections(player: player, editing: $editing)
                }
                .listStyle(.plain)
                .scrollContentBackground(.hidden)
                .environment(\.editMode, .constant(editing ? .active : .inactive))
                .onScrollGeometryChange(for: CGFloat.self) { $0.contentOffset.y + $0.contentInsets.top } action: { _, new in scrollY = new; pull = min(pull, new) }
                // Потянули вниз от самого верха и отпустили — плеер сворачивается.
                .onScrollPhaseChange { old, new in
                    if new == .interacting { pull = 0 }
                    if old == .interacting, pull < -45 { dismiss() }
                }
                // Системный эффект края прокрутки вместо своей подложки-материала.
                .safeAreaBar(edge: .top, spacing: 0) { topBar }
                // Свёрнутый плеер лежит поверх списка, а не внутри него: пока список крутится по инерции,
                // касание сразу попадает в кнопку, а не гасит прокрутку.
                .contentMargins(.bottom, scrollY > pageHeight * 0.55 ? 76 : 0, for: .scrollContent)
                .overlay(alignment: .bottom) {
                    if scrollY > pageHeight * 0.55 { miniBar(proxy: proxy).transition(.move(edge: .bottom).combined(with: .opacity)) }
                }
                .animation(.smooth, value: scrollY > pageHeight * 0.55)
            }
        }
        .background { PlayerBackdrop(coverID: player.current?.coverArt) }
        // Соседние обложки грузим заранее, чтобы при пролистывании они уже были на месте.
        .task(id: [player.previousSong?.coverArt, player.upcoming.first?.coverArt]) {
            for id in [player.previousSong?.coverArt, player.upcoming.first?.coverArt] {
                if let id { _ = await model.coverImage(id, pixels: 900) }
            }
        }
        .sheet(isPresented: $showEqualizer) {
            NavigationStack { EqualizerView().toolbar { ToolbarItem(placement: .confirmationAction) { Button("Done", systemImage: "checkmark") { showEqualizer = false } } } }
                .presentationDetents([.large])
        }
        .sheet(isPresented: $showCast) { CastPickerView() }
        .sheet(item: $sheet) { which in
            switch which {
            case .info: if let song = player.current { TrackInfoView(song: song, source: player.source(of: song)) }
            }
        }
    }

    /// Первый экран: обложка, название, прогресс, управление и нижний ряд. Тянем вверх — он уезжает, открывается очередь.
    private func playerPage(side: CGFloat, height: CGFloat, proxy: ScrollViewProxy) -> some View {
        // Свободное место делится между промежутками поровну, чтобы композиция «дышала»,
        // а не оставалась пустота между кнопками управления и нижним рядом.
        VStack(spacing: 0) {
            coverPager(side: side).padding(.top, 22)
            Spacer(minLength: 14)
            titleBlock.padding(.horizontal, 24)
            Spacer(minLength: 14)
            VStack(spacing: 8) {
                if player.isRadio { LiveIndicator() } else { PlayerScrubber(player: player) }
                if !player.isRadio, let song = player.current { QualityLine(song: song, source: player.source(of: song), quality: model.qualityInUse(for: song)) }
            }
            .padding(.horizontal, 24)
            Spacer(minLength: 18)
            Group {
                if player.isRadio { RadioControls(player: player) }
                else if player.isSpoken { SpokenControls(player: player) }
                else { controls }
            }
            .padding(.horizontal, 24)
            .padding(.bottom, 16)                      // кнопки управления чуть выше нижнего ряда
            Spacer(minLength: 18)
            bottomBar(proxy: proxy)
        }
        .frame(height: height)
    }

    /// Появляется, когда основной экран уехал вверх: обложка, название, сердечко и пауза.
    private func miniBar(proxy: ScrollViewProxy) -> some View {
        HStack(spacing: 12) {
            // Нажали на свёрнутый плеер — разворачиваем: прокрутка вверх.
            Button { withAnimation(.smooth) { proxy.scrollTo("page", anchor: .top) } } label: {
                HStack(spacing: 12) {
                    CoverArtView(id: player.current?.coverArt, size: 40, radius: 8)
                    VStack(alignment: .leading, spacing: 1) {
                        Text(player.current?.title ?? "").font(.subheadline.weight(.semibold)).lineLimit(1)
                        Text(player.current?.artist ?? "").font(.caption).foregroundStyle(.secondary).lineLimit(1)
                    }
                    Spacer(minLength: 0)
                }
                .contentShape(.rect)
            }
            .buttonStyle(.plain)
            .accessibilityLabel("Now Playing")
            favoriteButton
            Button { player.togglePlayPause() } label: {
                Image(systemName: player.isPlaying ? "pause.fill" : "play.fill").font(.title3).frame(width: 44, height: 44)
                    .contentTransition(.symbolEffect(.replace))
            }
            .accessibilityLabel(player.isPlaying ? "Pause" : "Play")
        }
        .foregroundStyle(.primary)
        .buttonStyle(.borderless)
        .padding(.horizontal, 16).padding(.vertical, 6)
        // Тот же Liquid Glass, что у системного мини-плеера над панелью вкладок.
        .glassEffect(.regular.interactive(), in: .capsule)
        .padding(.horizontal, 12).padding(.bottom, 6)
    }

    /// Шапка: стрелка вниз слева, альбом по центру, AirPlay и меню справа. Потянуть вниз — свернуть.
    private var topBar: some View {
        ZStack {
            VStack(spacing: 1) {
                // Альбом — переход на его страницу.
                if let song = player.current, song.albumId != nil, !player.isRadio, !player.isSpoken {
                    Button { Task { await model.showAlbum(of: song) } } label: {
                        Text(song.album ?? "").font(.footnote.weight(.semibold)).foregroundStyle(.secondary).lineLimit(1)
                    }
                    .buttonStyle(.plain)
                    .accessibilityHint("Show Album")
                } else {
                    Text(player.current?.album ?? "").font(.footnote.weight(.semibold)).foregroundStyle(.secondary).lineLimit(1)
                }
                if let zone = model.castZone {
                    Label(zone.name, systemImage: "hifispeaker.fill")
                        .font(.caption2).foregroundStyle(.tint).lineLimit(1)
                        .accessibilityLabel(Text("Playing on \(zone.name)"))
                }
                if let end = player.sleepEndsAt {
                    TimelineView(.periodic(from: .now, by: 1)) { ctx in
                        let left = max(Int(end.timeIntervalSince(ctx.date)), 0)
                        Label(String(format: "%d:%02d", left / 60, left % 60), systemImage: "moon.zzz.fill")
                            .font(.caption2.monospacedDigit()).foregroundStyle(.tint)
                    }
                }
            }
            .padding(.horizontal, 110)
            HStack(spacing: 0) {
                Button { dismiss() } label: {
                    Image(systemName: "chevron.down").font(.system(size: 20, weight: .semibold))
                        .foregroundStyle(.primary).frame(width: 44, height: 44)
                }
                .accessibilityLabel("Close")
                Spacer()
                // AirPlay и меню — на одной стеклянной подложке.
                HStack(spacing: 0) {
                    if model.capabilities.hasCast || model.castZone != nil {
                        Button { showCast = true } label: {
                            Image(systemName: model.castZone == nil ? "hifispeaker" : "hifispeaker.fill")
                                .font(.system(size: 18, weight: .medium))
                                .frame(width: 44, height: 44)
                        }
                        .tint(model.castZone == nil ? Color.primary : Color.accentColor)     // как у AirPlay: акцент — только когда играет на колонке
                        .accessibilityLabel("Speakers")
                    } else {
                        RoutePicker().frame(width: 44, height: 44).accessibilityLabel("AirPlay")
                    }
                    moreMenu
                }
            }
        }
        .padding(.horizontal, 12)
        .buttonStyle(.borderless)
        .contentShape(.rect)
    }

    /// Обложки: предыдущая, текущая, следующая — системная горизонтальная прокрутка с постраничной остановкой.
    /// Физика, инерция и отскок — штатные. Трек меняем, когда прокрутка остановилась на соседней обложке.
    /// Ячейки привязаны к id трека: после смены трека обложки занимают новое место, не перезагружаясь (иначе мерцание).
    private func coverPager(side: CGFloat) -> some View {
        let gap: CGFloat = 28
        var built: [(id: String, song: Song, slot: Int)] = []
        // Один и тот же трек может быть и предыдущим, и следующим (повтор, дубли в очереди): id ячеек обязаны быть уникальны,
        // иначе SwiftUI путает обложки.
        let currentSong = player.current
        if let p = player.previousSong { built.append((p.id == currentSong?.id ? p.id + "#prev" : p.id, p, -1)) }
        if let c = currentSong { built.append((c.id, c, 0)) }
        if let n = player.upcoming.first {
            var nid = n.id
            if nid == currentSong?.id || nid == player.previousSong?.id { nid += "#next" }
            built.append((nid, n, 1))
        }
        let currentID = player.current?.id
        let cells = built
        return GeometryReader { geo in
            ScrollViewReader { pagerProxy in
            ScrollView(.horizontal) {
                HStack(spacing: gap) {
                    ForEach(cells, id: \.id) { cell in
                        ZStack(alignment: .topTrailing) {
                            CoverArtView(id: cell.song.coverArt, size: side, radius: 24)
                                .blur(radius: cell.slot == 0 && lyricsOnCover ? 28 : 0)
                                .overlay { Color.black.opacity(cell.slot == 0 && lyricsOnCover ? 0.55 : 0).clipShape(.rect(cornerRadius: 24)) }
                            if cell.slot == 0, lyricsOnCover { lyricsOverlay }
                        }
                        .frame(width: side, height: side)
                        .clipShape(.rect(cornerRadius: 24, style: .continuous))
                        .shadow(color: .black.opacity(cell.slot == 0 && player.isPlaying ? 0.3 : 0.12), radius: 24, y: 12)
                        .scaleEffect(player.isPlaying || lyricsOnCover ? 1 : 0.88)       // на паузе обложка отступает, как в Apple Music (у каждой своя, иначе лента смещается)
                        .animation(.bouncy, value: player.isPlaying)
                        // Уходящая обложка уменьшается, приходящая растёт — по ходу пальца.
                        .scrollTransition(.interactive, axis: .horizontal) { content, phase in
                            content.scaleEffect(1 - 0.12 * min(abs(phase.value), 1))
                        }
                        .id(cell.id)
                    }
                }
                .scrollTargetLayout()
            }
            .contentMargins(.horizontal, max((geo.size.width - side) / 2, 0), for: .scrollContent)
            .scrollTargetBehavior(.viewAligned)
            .scrollPosition(id: $pagerID)
            .scrollIndicators(.hidden)
            .scrollClipDisabled()                          // тень обложки не обрезается
            .scrollDisabled(lyricsOnCover)                 // с текстом на обложке жесты отдаём тексту, трек не листается случайно
            // Остановились на соседней обложке — переключаем трек.
            .onScrollPhaseChange { _, new in
                guard new == .idle, let id = pagerID, let cell = cells.first(where: { $0.id == id }), cell.slot != 0 else { return }
                if cell.slot > 0 { player.next() } else { player.playPrevious() }
            }
            // Трек сменился не жестом (кнопка, автопереход) — обложка встаёт на него без анимации.
            // Набор ячеек изменился (трек сменился, очередь догрузилась, соседний трек подменился) — обложка встаёт на текущий трек.
            // Так лента никогда не остаётся на ячейке, которой уже нет или которая показывает не тот трек.
            // Присвоения pagerID мало: смещение остаётся прежним, а ячейки уже переехали, и лента показывает соседний трек.
            // Поэтому ещё и прокручиваем к текущей ячейке явно, без анимации (картинка та же — шва не видно).
            .onChange(of: cells.map(\.id)) { _, _ in
                guard let current = cells.first(where: { $0.slot == 0 }) else { return }
                var t = Transaction(); t.disablesAnimations = true
                withTransaction(t) {
                    pagerID = current.id
                    pagerProxy.scrollTo(current.id, anchor: .center)
                }
            }
            .onAppear { pagerID = currentID }
            }
        }
        .frame(height: side)
        .animation(.smooth, value: lyricsOnCover)
        .task(id: LyricsKey(songID: player.current?.id, on: lyricsOnCover || lyricsFullScreen)) {
            guard lyricsOnCover || lyricsFullScreen else { return }
            await lyricsModel.load(song: player.current, client: model.client)
        }
        .fullScreenCover(isPresented: $lyricsFullScreen) {
            LyricsFullScreen(player: player, model: lyricsModel, onRefresh: {
                Task { await lyricsModel.load(song: player.current, client: model.client, force: true) }
            }, blurInactive: $lyricsBlur)
        }
    }

    private struct LyricsKey: Hashable { let songID: String?; let on: Bool }

    /// Текст поверх размытой обложки.
    private var lyricsOverlay: some View {
        ZStack(alignment: .topTrailing) {
            LyricsPane(player: player, model: lyricsModel, fontSize: 31, blurInactive: lyricsBlur, onDark: true)
                .foregroundStyle(.white)
                .padding(.horizontal, 18)
            Button("Expand Lyrics", systemImage: "arrow.up.left.and.arrow.down.right") { lyricsFullScreen = true }
                .labelStyle(.iconOnly)
                .buttonStyle(.glass).buttonBorderShape(.circle).controlSize(.small)
                .padding(10)
        }
        .transition(.opacity)
    }

    private var titleBlock: some View {
        HStack(spacing: 12) {
            VStack(alignment: .leading, spacing: 3) {
                Text(player.current?.title ?? "").font(.title3.bold()).lineLimit(1)
                // Артист — переход на его страницу (для радио и подкастов это просто текст).
                if !player.isRadio, !player.isSpoken, let song = player.current, song.artistId != nil {
                    Button { model.showArtist(of: song) } label: {
                        Text(song.artist ?? "").font(.title3).foregroundStyle(.secondary).lineLimit(1)
                    }
                    .buttonStyle(.plain)
                    .accessibilityHint("Show Artist")
                } else {
                    Text((player.isRadio ? player.streamTitle : player.current?.artist) ?? "").font(.title3).foregroundStyle(.secondary).lineLimit(1)
                }
            }
            Spacer(minLength: 0)
            if !player.isRadio && !player.isSpoken { favoriteButton }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .contentTransition(.opacity)
        .animation(.smooth, value: player.current?.id)
    }

    /// Shuffle и повтор по краям, переключение треков в центре.
    private var controls: some View {
        HStack {
            Button { player.toggleShuffle() } label: { modeIcon("shuffle", active: player.shuffleOn) }
                .accessibilityLabel("Shuffle")
                .accessibilityValue(player.shuffleOn ? "On" : "Off")
            Spacer()
            Button { player.previous() } label: { Image(systemName: "backward.fill").font(.system(size: 32)) }
                .accessibilityLabel("Previous Track")
            Spacer()
            Button { player.togglePlayPause() } label: {
                Image(systemName: player.isPlaying ? "pause.fill" : "play.fill").font(.system(size: 46))
                    .contentTransition(.symbolEffect(.replace))
                    .frame(width: 60)
            }
            .accessibilityLabel(player.isPlaying ? "Pause" : "Play")
            Spacer()
            Button { player.next() } label: { Image(systemName: "forward.fill").font(.system(size: 32)) }
                .disabled(!player.hasNext)
                .accessibilityLabel("Next Track")
            Spacer()
            Button { player.cycleRepeat() } label: {
                modeIcon(player.repeatMode == .one ? "repeat.1" : "repeat", active: player.repeatMode != .off)
            }
            .accessibilityLabel("Repeat")
            .accessibilityValue(repeatDescription)
        }
        .buttonStyle(.borderless)
        .foregroundStyle(.primary)
        .sensoryFeedback(.impact(weight: .light), trigger: player.current?.id)
        .sensoryFeedback(.selection, trigger: player.shuffleOn)
        .sensoryFeedback(.selection, trigger: player.repeatMode)
    }

    private var repeatDescription: LocalizedStringKey {
        switch player.repeatMode { case .off: "Off"; case .all: "All"; case .one: "One" }
    }

    /// Включённый режим подсвечен акцентом и плашкой, выключенный — приглушён.
    private func modeIcon(_ name: String, active: Bool) -> some View {
        Image(systemName: name)
            .font(.system(size: 20, weight: .semibold))
            .contentTransition(.symbolEffect(.replace))
            .foregroundStyle(active ? Color.accentColor : .secondary)
            .frame(width: 44, height: 44)
            .background(active ? Color.accentColor.opacity(0.16) : .clear, in: .circle)
            .animation(.snappy, value: active)
    }

    /// Нижний ряд: очередь (прокрутка к списку), текст по центру, информация.
    private func bottomBar(proxy: ScrollViewProxy) -> some View {
        HStack {
            pill("list.bullet", on: false, label: "Queue") { withAnimation(.smooth) { proxy.scrollTo("queue", anchor: .top) } }
            Spacer()
            pill(lyricsOnCover ? "quote.bubble.fill" : "quote.bubble", on: lyricsOnCover, label: "Lyrics") {
                withAnimation(.smooth) { lyricsOnCover.toggle() }
            }
            .sensoryFeedback(.selection, trigger: lyricsOnCover)
            Spacer()
            pill("info.circle", on: false, label: "Track Info") { sheet = .info }
        }
        .padding(.horizontal, 28)
        .padding(.top, 4)
        .padding(.bottom, 28)
    }

    private func pill(_ icon: String, on: Bool, label: LocalizedStringKey, action: @escaping () -> Void) -> some View {
        Button(action: action) {
            Image(systemName: icon).font(.system(size: 20, weight: .medium))
                .contentTransition(.symbolEffect(.replace))
                .foregroundStyle(on ? Color.accentColor : .primary)       // без подложки: включённый режим — акцентный цвет иконки
                .frame(width: 64, height: 40)
                .contentShape(.rect)
        }
        .buttonStyle(.borderless)
        .accessibilityLabel(label)
        .accessibilityValue(on ? "On" : "Off")
    }

    private var favoriteButton: some View {
        let isFav = player.current.map(player.isFavorite) ?? false
        return Button {
            if let song = player.current { Task { await player.toggleFavorite(song) } }
        } label: {
            Image(systemName: isFav ? "heart.fill" : "heart")
                .font(.system(size: 20))
                .foregroundStyle(isFav ? Color.accentColor : .primary)
                .symbolEffect(.bounce, value: isFav)
                .contentTransition(.symbolEffect(.replace))
                .frame(width: 44, height: 44)
                .contentShape(.circle)
        }
        // В List кнопка по умолчанию нажимается всей строкой; borderless оставляет нажатие только на самой иконке.
        .buttonStyle(.borderless)
        .sensoryFeedback(.impact(weight: .medium), trigger: isFav)
        .accessibilityLabel(isFav ? "Remove from Favorites" : "Add to Favorites")
    }

    /// Таймер сна: через N минут или до конца трека; музыка плавно затихает.
    @ViewBuilder private var sleepMenu: some View {
        Menu("Sleep Timer", systemImage: "moon.zzz") {
            if player.sleepEndsAt != nil {
                Button("Turn Off", systemImage: "xmark.circle", role: .destructive) { player.setSleepTimer(minutes: nil) }
            }
            ForEach([5, 10, 15, 30, 45, 60, 90], id: \.self) { m in
                Button { player.setSleepTimer(minutes: m) } label: { Text("\(m) min") }
            }
            Button("End of Track", systemImage: "music.note") { player.setSleepAtEndOfTrack() }
        }
    }

    private var moreMenu: some View {
        ZStack {
            // Иконка нарисована отдельно от меню: iOS прячет метку меню, пока идёт анимация закрытия, и возвращает её с задержкой.
            Image(systemName: "ellipsis").font(.system(size: 20)).foregroundStyle(.primary)
                .frame(width: 44, height: 44).allowsHitTesting(false)
        Menu {
            if let song = player.current { RatingMenu(song: song) }
            if let song = player.current { Button("Share…", systemImage: "square.and.arrow.up") { Task { await model.share(song) } } }
            if let song = player.current {
                if model.isDownloaded(song) {
                    Button("Remove Download", systemImage: "trash") { model.removeDownloads([song]) }
                } else {
                    Button("Download", systemImage: "arrow.down.circle") { model.download([song]) }
                }
            }
            if let song = player.current, song.albumId != nil {
                Button("Show Album", systemImage: "square.stack") { Task { await model.showAlbum(of: song) } }
            }
            if let song = player.current, song.artistId != nil {
                Button("Show Artist", systemImage: "music.mic") { model.showArtist(of: song) }
            }
            Section {
                Button("Equalizer", systemImage: "slider.vertical.3") { showEqualizer = true }
                sleepMenu
            }
        } label: {
            Color.clear.frame(width: 44, height: 44).contentShape(.rect)
        }
        .buttonStyle(.plain)
        .accessibilityLabel("More")
        }
    }
}

/// «FLAC · 24-bit/96 kHz · 1411 kbps» и статус: онлайн или загружен.
private struct QualityLine: View {
    let song: Song
    let source: TrackSource
    let quality: StreamQuality

    private var sourceTitle: LocalizedStringKey {
        switch source { case .downloaded: "Downloaded"; case .cached: "Cached"; case .streaming: "Online" }
    }
    private var sourceIcon: String {
        switch source { case .downloaded: "arrow.down.circle.fill"; case .cached: "internaldrive"; case .streaming: "icloud" }
    }

    var body: some View {
        HStack(spacing: 6) {
            let text = TrackFormat.summary(song, quality: quality)
            if !text.isEmpty { Text(text) }
            if !text.isEmpty { Text("·").foregroundStyle(.tertiary) }
            Label(sourceTitle, systemImage: sourceIcon)
                .labelStyle(.titleAndIcon)
        }
        .font(.caption.weight(.medium))
        .foregroundStyle(.secondary)
        .lineLimit(1)
        .minimumScaleFactor(0.8)
        .frame(maxWidth: .infinity)
        .contentTransition(.opacity)
        .animation(.smooth, value: song.id)
    }
}

/// Полоса прогресса с перемоткой перетаскиванием и временем по краям.
private struct PlayerScrubber: View {
    let player: Player
    @State private var dragFraction: Double?

    var body: some View {
        TimelineView(.periodic(from: .now, by: 0.25)) { _ in
            let duration = max(player.duration, 1)
            let fraction = dragFraction ?? min(max(player.progress / duration, 0), 1)
            VStack(spacing: 6) {
                // Системный Slider: перемотка применяется, когда палец отпущен.
                Slider(value: Binding(get: { fraction }, set: { dragFraction = $0 }), in: 0...1) { editing in
                    if !editing, let f = dragFraction { player.seek(to: f * duration); dragFraction = nil }
                }
                .sliderThumbVisibility(.hidden)
                HStack {
                    Text(Self.format(fraction * duration))
                    Spacer()
                    Text("-" + Self.format(duration - fraction * duration))
                }
                .font(.caption.monospacedDigit()).foregroundStyle(.secondary)
            }
        }
    }

    private static func format(_ seconds: Double) -> String {
        let s = max(Int(seconds.rounded()), 0)
        return "\(s / 60):" + String(format: "%02d", s % 60)
    }
}

/// Размытая обложка под полупрозрачным материалом.
struct PlayerBackdrop: View {
    let coverID: String?
    @Environment(AppModel.self) private var model
    @State private var image: UIImage?

    var body: some View {
        ZStack {
            Color(.systemBackground)
            if let image {
                Image(uiImage: image).resizable().scaledToFill()
                    .blur(radius: 70).saturation(1.4).opacity(0.7)
                    .transition(.opacity)
            }
            Rectangle().fill(.ultraThinMaterial)
        }
        .ignoresSafeArea()
        .animation(.smooth, value: image)
        .task(id: coverID) {
            guard let coverID else { return }
            image = await model.coverImage(coverID, pixels: 200)
        }
    }
}

/// Кнопка выбора устройства (AirPlay, Bluetooth).
private struct RoutePicker: UIViewRepresentable {
    func makeUIView(context: Context) -> AVRoutePickerView {
        let v = AVRoutePickerView()
        v.tintColor = .label
        v.activeTintColor = .tintColor
        return v
    }
    func updateUIView(_ uiView: AVRoutePickerView, context: Context) {}
}
