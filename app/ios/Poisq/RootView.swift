import SwiftUI
import UIKit
import PoisqPlayback

enum AppTab: String, CaseIterable, Codable, Identifiable {
    case home, library, playlists, settings, search
    var id: String { rawValue }

    var title: LocalizedStringKey {
        switch self {
        case .home: "Home"
        case .library: "Library"
        case .playlists: "Playlists"
        case .settings: "Settings"
        case .search: "Search"
        }
    }

    var icon: String {
        switch self {
        case .home: "house.fill"
        case .library: "square.stack.fill"
        case .playlists: "music.note.list"
        case .settings: "gearshape.fill"
        case .search: "magnifyingglass"
        }
    }
}

/// Состав и порядок нижней панели. Главная остаётся обязательной, чтобы настройку всегда можно было открыть снова.
@MainActor @Observable
final class TabBarLayout {
    private(set) var visible: [AppTab]
    private static let key = "tabBarLayout"

    init() {
        let saved = UserDefaults.standard.stringArray(forKey: Self.key)?.compactMap(AppTab.init(rawValue:)) ?? []
        let unique = saved.reduce(into: [AppTab]()) { if !$0.contains($1) { $0.append($1) } }
        visible = unique.isEmpty ? AppTab.allCases : unique
        if !visible.contains(.home) { visible.insert(.home, at: 0) }
    }

    var hidden: [AppTab] { AppTab.allCases.filter { !visible.contains($0) } }
    var isDefault: Bool { visible == AppTab.allCases }

    func move(from: IndexSet, to: Int) {
        visible.move(fromOffsets: from, toOffset: to)
        save()
    }

    func hide(at offsets: IndexSet) {
        visible.remove(atOffsets: IndexSet(offsets.filter { visible[$0] != .home }))
        save()
    }

    func add(_ tab: AppTab) {
        guard !visible.contains(tab) else { return }
        visible.append(tab)
        save()
    }

    func reset() {
        visible = AppTab.allCases
        save()
    }

    private func save() { UserDefaults.standard.set(visible.map(\.rawValue), forKey: Self.key) }
}

struct RootView: View {
    @Environment(AppModel.self) private var model

    var body: some View {
        if model.isSignedIn { mainTabs } else { SignInView(isSheet: false) }
    }

    @State private var tab = AppTab.home
    @State private var tabLayout = TabBarLayout()
    @State private var homePath = NavigationPath()
    @State private var libraryPath = NavigationPath()
    @State private var playlistsPath = NavigationPath()
    @State private var settingsPath = NavigationPath()
    @State private var searchPath = NavigationPath()
    @Environment(\.colorScheme) private var scheme
    @State private var showPlayer = false
    @Namespace private var playerZoom
    @Environment(\.scenePhase) private var scenePhase

    /// Кнопка Play (shuffle) живёт только на главной и только пока ничего не играет.
    /// Когда трек играет, мини-плеер виден на всех вкладках.
    private var showAccessory: Bool {
        guard let player = model.player else { return false }
        return player.isActive || tab == .home
    }

    private var mainTabs: some View {
        TabView(selection: $tab) {
            ForEach(tabLayout.visible) { item in
                if item == .search {
                    Tab(value: item, role: .search) { tabContent(item) }
                } else {
                    Tab(item.title, systemImage: item.icon, value: item) { tabContent(item) }
                }
            }
        }
        .environment(tabLayout)
        .tabBarMinimizeBehavior(.onScrollDown)
        .alert("Download Limit Reached", isPresented: Binding(get: { model.downloadLimitReached }, set: { model.downloadLimitReached = $0 })) {
            Button("OK", role: .cancel) {}
        } message: {
            Text("Some songs were not downloaded. Raise the limit in Settings or remove downloads you no longer need.")
        }
        .onChange(of: scenePhase) { _, phase in
            if phase == .active {
                Task { await model.checkServer() }
            }
            if phase == .background { model.saveSession() }
        }
        .onChange(of: model.pendingAlbum) { _, album in
            guard let album else { return }
            path(for: tab).wrappedValue.append(album)
            model.pendingAlbum = nil
            showPlayer = false
        }
        .onChange(of: model.pendingArtist) { _, artist in
            guard let artist else { return }
            path(for: tab).wrappedValue.append(artist)
            model.pendingArtist = nil
            showPlayer = false
        }
        .onChange(of: model.pendingPlaylist) { _, playlist in
            guard let playlist else { return }
            path(for: tab).wrappedValue.append(playlist)
            model.pendingPlaylist = nil
            showPlayer = false
        }
        .onChange(of: tabLayout.visible) { _, visible in
            if !visible.contains(tab) { tab = visible.first ?? .home }
        }
        .onOpenURL { SystemIntegration.shared.open($0) }
        .onChange(of: model.openPlayerRequest) { if model.player?.isActive == true { showPlayer = true } }       // ссылки из виджетов: poisq://album/…
        .sheet(item: Binding(get: { model.playlistTarget }, set: { model.playlistTarget = $0 })) { AddToPlaylistSheet(target: $0) }
        .sheet(item: Binding(get: { model.shareItems }, set: { model.shareItems = $0 })) { ActivityView(items: $0.items).presentationDetents([.medium, .large]) }
        .fullScreenCover(isPresented: $showPlayer) {
            if let player = model.player {
                FullPlayerView(player: player)
                    .navigationTransition(.zoom(sourceID: "player", in: playerZoom))     // системный zoom: интерактивное закрытие жестом
            }
        }
        .tabViewBottomAccessory(isEnabled: showAccessory) {
            if let player = model.player { PlayerAccessory(player: player, scheme: scheme, onOpen: { showPlayer = true })
                    .matchedTransitionSource(id: "player", in: playerZoom) }
        }
    }

    @ViewBuilder private func tabContent(_ item: AppTab) -> some View {
        switch item {
        case .home: HomeView(path: $homePath)
        case .library: LibraryView(path: $libraryPath)
        case .playlists: PlaylistsView(path: $playlistsPath)
        case .settings: SettingsView(path: $settingsPath)
        case .search: SearchView(path: $searchPath)
        }
    }

    private func path(for item: AppTab) -> Binding<NavigationPath> {
        switch item {
        case .home: $homePath
        case .library: $libraryPath
        case .playlists: $playlistsPath
        case .settings: $settingsPath
        case .search: $searchPath
        }
    }
}

/// Системный список настройки: строки можно переставить, удалить и вернуть кнопкой «+».
struct TabBarEditView: View {
    @Environment(TabBarLayout.self) private var layout
    @Environment(\.dismiss) private var dismiss

    var body: some View {
        NavigationStack {
            List {
                Section("Shown") {
                    ForEach(layout.visible) { item in
                        Label(item.title, systemImage: item.icon)
                            .deleteDisabled(item == .home)
                    }
                    .onMove { layout.move(from: $0, to: $1) }
                    .onDelete { layout.hide(at: $0) }
                }
                if !layout.hidden.isEmpty {
                    Section("More Buttons") {
                        ForEach(layout.hidden) { item in
                            Button { withAnimation(.snappy) { layout.add(item) } } label: {
                                Label(item.title, systemImage: "plus.circle.fill")
                            }
                        }
                    }
                }
                if !layout.isDefault {
                    Section { Button("Reset to Default", role: .destructive) { withAnimation { layout.reset() } } }
                }
            }
            .environment(\.editMode, .constant(.active))
            .navigationTitle("Customize Tab Bar")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar { ToolbarItem(placement: .confirmationAction) { Button("Done", systemImage: "checkmark") { dismiss() } } }
        }
        .presentationDetents([.large])
    }
}

struct PlaceholderScreen: View {
    let title: LocalizedStringKey
    var body: some View {
        NavigationStack {
            ContentUnavailableView(title, systemImage: "hammer", description: Text("Coming soon"))
                .navigationTitle(title)
        }
    }
}

/// Пусто → одна кнопка Play (shuffle всей медиатеки). Играет → мини-плеер.
struct PlayerAccessory: View {
    let player: Player
    /// Схема приложения. Внутри аксессуара система сама переключает светлый/тёмный вид по тому, что лежит под ним
    /// (например, яркая обложка), и текст становился белым на светлом стекле. Фиксируем схему и подкладываем материал.
    let scheme: ColorScheme
    let onOpen: () -> Void

    var body: some View {
        Group {
            if player.isActive { MiniPlayer(player: player, onOpen: onOpen) } else { ShufflePlayButton(player: player) }
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .background(.regularMaterial)
        .environment(\.colorScheme, scheme)
        .animation(.smooth, value: player.isActive)
    }
}

struct ShufflePlayButton: View {
    let player: Player

    var body: some View {
        Button { Task { await player.shuffleLibrary() } } label: {
            Group {
                if player.isLoading { ProgressView() } else { Image(systemName: "play.fill").font(.title2) }
            }
            .frame(maxWidth: .infinity, maxHeight: .infinity)
            .contentShape(.rect)
        }
        .buttonStyle(.plain)
        .sensoryFeedback(.impact, trigger: player.isLoading)
        .accessibilityLabel("Shuffle all")
    }
}

struct MiniPlayer: View {
    let player: Player
    let onOpen: () -> Void

    var body: some View {
        HStack(spacing: 12) {
            Button(action: onOpen) {
                HStack(spacing: 12) {
                    CoverArtView(id: player.current?.coverArt, size: 36, radius: 6)
                    VStack(alignment: .leading, spacing: 0) {
                        Text(player.current?.title ?? "").font(.subheadline.weight(.semibold)).lineLimit(1)
                        Text(player.current?.displayArtist ?? "").font(.caption).foregroundStyle(.secondary).lineLimit(1)
                    }
                    Spacer(minLength: 0)
                }
                .contentShape(.rect)
            }
            .buttonStyle(.plain)
            .accessibilityLabel("Now Playing")
            Button { player.togglePlayPause() } label: {
                Image(systemName: player.isPlaying ? "pause.fill" : "play.fill").font(.title3)
                    .contentTransition(.symbolEffect(.replace))
            }
            .buttonStyle(.plain)
            .accessibilityLabel(player.isPlaying ? "Pause" : "Play")
        }
        .padding(.horizontal)
        // Свайп влево — следующий трек, вправо — предыдущий.
        .highPriorityGesture(
            DragGesture(minimumDistance: 24).onEnded { v in
                guard abs(v.translation.width) > abs(v.translation.height) * 1.5, abs(v.translation.width) > 50 else { return }
                if v.translation.width < 0 { player.next() } else { player.previous() }
            }
        )
        .sensoryFeedback(.impact(weight: .light), trigger: player.current?.id)
    }
}
