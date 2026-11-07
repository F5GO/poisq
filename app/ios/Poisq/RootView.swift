import SwiftUI
import UIKit
import PoisqPlayback

struct RootView: View {
    @Environment(AppModel.self) private var model

    var body: some View {
        if model.isSignedIn { mainTabs } else { SignInView(isSheet: false) }
    }

    private enum AppTab: Hashable { case home, library, playlists, settings, search }
    @State private var tab = AppTab.home
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
            Tab("Home", systemImage: "house.fill", value: AppTab.home) { HomeView() }
            Tab("Library", systemImage: "square.stack.fill", value: AppTab.library) { LibraryView() }
            Tab("Playlists", systemImage: "music.note.list", value: AppTab.playlists) { PlaylistsView() }
            Tab("Settings", systemImage: "gearshape.fill", value: AppTab.settings) { SettingsView() }
            Tab(value: AppTab.search, role: .search) { SearchView() }
        }
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
            if album != nil { tab = .home; showPlayer = false }
        }
        .onChange(of: model.pendingArtist) { _, artist in
            if artist != nil { tab = .home; showPlayer = false }
        }
        .onChange(of: model.pendingPlaylist) { _, playlist in
            if playlist != nil { tab = .playlists; showPlayer = false }
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
