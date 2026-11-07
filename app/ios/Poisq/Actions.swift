import SwiftUI
import PoisqCore
import PoisqPlayback

struct ShareItems: Identifiable {
    let id = UUID()
    let items: [Any]
}

/// Системное окно «Поделиться».
struct ActivityView: UIViewControllerRepresentable {
    let items: [Any]
    func makeUIViewController(context: Context) -> UIActivityViewController { UIActivityViewController(activityItems: items, applicationActivities: nil) }
    func updateUIViewController(_ vc: UIActivityViewController, context: Context) {}
}

struct PlaylistTarget: Identifiable {
    let id = UUID()
    let songs: [Song]
}

/// Подменю «Оценка»: 1–5 звёзд или без оценки. Системный Picker внутри Menu.
struct RatingMenu: View {
    let song: Song
    @Environment(AppModel.self) private var model

    var body: some View {
        if let player = model.player {
            Menu {
                Picker("Rating", selection: Binding(get: { player.rating(of: song) }, set: { r in Task { await player.setRating(r, for: song) } })) {
                    Text("No Rating").tag(0)
                    ForEach(1...5, id: \.self) { n in Text(String(repeating: "★", count: n)).tag(n) }
                }
            } label: {
                let r = player.rating(of: song)
                Label(r == 0 ? String(localized: "Rate") : String(repeating: "★", count: r), systemImage: r == 0 ? "star" : "star.fill")
            }
        }
    }
}

/// Пункты меню песни: очередь, избранное, плейлист, загрузка, переход к альбому. Общие для долгого нажатия и кнопки «⋯».
struct SongMenuItems: View {
    let song: Song
    @Environment(AppModel.self) private var model

    var body: some View {
        Button("Play Next", systemImage: "text.line.first.and.arrowtriangle.forward") { model.player?.playNext([song]) }
        Button("Add to Queue", systemImage: "text.line.last.and.arrowtriangle.forward") { model.player?.addToQueue([song]) }
        Divider()
        Button("Add to Playlist…", systemImage: "text.badge.plus") { model.playlistTarget = PlaylistTarget(songs: [song]) }
        RatingMenu(song: song)
        Button("Share…", systemImage: "square.and.arrow.up") { Task { await model.share(song) } }
        if let player = model.player {
            let fav = player.isFavorite(song)
            Button(fav ? "Remove from Favorites" : "Add to Favorites", systemImage: fav ? "heart.slash" : "heart") {
                Task { await player.toggleFavorite(song) }
            }
        }
        if model.isDownloaded(song) {
            Button("Remove Download", systemImage: "trash", role: .destructive) { model.removeDownloads([song]) }
        } else if !model.offline {
            Button("Download", systemImage: "arrow.down.circle") { model.download([song]) }
        }
        if !model.offline, song.albumId != nil || song.artistId != nil {
            Divider()
            if song.albumId != nil { Button("Show Album", systemImage: "square.stack") { Task { await model.showAlbum(of: song) } } }
            if song.artistId != nil { Button("Show Artist", systemImage: "music.mic") { model.showArtist(of: song) } }
        }
    }
}

/// Меню песни в строке списка: долгое нажатие и кнопка «⋯» справа.
struct SongMenu: ViewModifier {
    let song: Song
    var showsButton = true

    func body(content: Content) -> some View {
        HStack(spacing: 0) {
            content.contextMenu { SongMenuItems(song: song) }
            if showsButton {
                Menu { SongMenuItems(song: song) } label: {
                    Image(systemName: "ellipsis").frame(width: 44, height: 44).contentShape(.rect)
                }
                .buttonStyle(.borderless)
                .tint(.secondary)       // без акцентного цвета: иначе кнопка в списке становится розовой
                .accessibilityLabel("More")
            }
        }
    }
}

/// Контекстное меню альбома: песни подгружаются по требованию.
struct AlbumMenu: ViewModifier {
    let album: Album
    @Environment(AppModel.self) private var model

    func body(content: Content) -> some View {
        content.contextMenu {
            Button("Play", systemImage: "play.fill") { run { model.player?.play($0) } }
            let pinned = model.pins.isPinned(album: album)
            Button(pinned ? "Unpin from Home" : "Pin to Home", systemImage: pinned ? "pin.slash" : "pin") { model.pins.toggle(album: album) }
            Button("Shuffle", systemImage: "shuffle") { run { model.player?.play($0, startAt: $0.indices.randomElement() ?? 0, shuffled: true) } }
            Divider()
            Button("Play Next", systemImage: "text.line.first.and.arrowtriangle.forward") { run { model.player?.playNext($0) } }
            Button("Add to Queue", systemImage: "text.line.last.and.arrowtriangle.forward") { run { model.player?.addToQueue($0) } }
            Button("Add to Playlist…", systemImage: "text.badge.plus") { run { model.playlistTarget = PlaylistTarget(songs: $0) } }
            if !model.offline { Button("Download", systemImage: "arrow.down.circle") { run { model.download($0) } } }
        }
    }

    private func run(_ action: @escaping ([Song]) -> Void) {
        Task {
            guard let songs = try? await model.client?.album(id: album.id).songs, !songs.isEmpty else { return }
            action(songs)
        }
    }
}

/// Закрепление плейлиста на главной.
struct PlaylistPinMenu: ViewModifier {
    let playlist: Playlist
    @Environment(AppModel.self) private var model
    func body(content: Content) -> some View {
        content.contextMenu {
            let pinned = model.pins.isPinned(playlist: playlist)
            Button(pinned ? "Unpin from Home" : "Pin to Home", systemImage: pinned ? "pin.slash" : "pin") { model.pins.toggle(playlist: playlist) }
        }
    }
}

extension View {
    func playlistPinMenu(_ playlist: Playlist) -> some View { modifier(PlaylistPinMenu(playlist: playlist)) }
    func songMenu(_ song: Song, button: Bool = true) -> some View { modifier(SongMenu(song: song, showsButton: button)) }
    func albumMenu(_ album: Album) -> some View { modifier(AlbumMenu(album: album)) }
}

/// Выбор плейлиста для добавления песен или создание нового.
struct AddToPlaylistSheet: View {
    let target: PlaylistTarget
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss
    @State private var playlists: [Playlist] = []
    @State private var loaded = false
    @State private var creating = false
    @State private var newName = ""
    @State private var failed = false

    var body: some View {
        NavigationStack {
            List {
                Button { creating = true } label: { Label("New Playlist…", systemImage: "plus") }
                ForEach(playlists) { p in
                    Button { add(to: p) } label: { MediaRow(coverID: p.coverArt, title: p.name, subtitle: p.songCount.map { String(localized: "\($0) songs") }) }
                        .buttonStyle(.plain)
                }
            }
            .listStyle(.plain)
            .overlay { if !loaded { ProgressView() } }
            .navigationTitle("Add to Playlist")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar { ToolbarItem(placement: .cancellationAction) { Button("Cancel", systemImage: "xmark") { dismiss() } } }
            .alert("New Playlist", isPresented: $creating) {
                TextField("Name", text: $newName)
                Button("Cancel", role: .cancel) { newName = "" }
                Button("Create") { create() }
            }
            .alert("Couldn't save the playlist", isPresented: $failed) { Button("OK", role: .cancel) {} }
            .task { playlists = (try? await model.client?.playlists()) ?? []; loaded = true }
        }
        .presentationDetents([.medium, .large])
    }

    private func add(to playlist: Playlist) {
        Task {
            do { try await model.client?.addToPlaylist(id: playlist.id, songIDs: target.songs.map(\.id)); model.playlistsVersion += 1; dismiss() }
            catch { failed = true }
        }
    }

    private func create() {
        let name = newName.trimmingCharacters(in: .whitespaces)
        newName = ""
        guard !name.isEmpty else { return }
        Task {
            do { try await model.client?.createPlaylist(name: name, songIDs: target.songs.map(\.id)); model.playlistsVersion += 1; dismiss() }
            catch { failed = true }
        }
    }
}
