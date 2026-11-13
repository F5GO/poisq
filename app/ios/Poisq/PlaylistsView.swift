import SwiftUI
import PoisqCore
import PoisqPlayback

struct PlaylistsView: View {
    @Environment(AppModel.self) private var model
    @State private var playlists: [Playlist] = []
    @State private var loaded = false
    @State private var failed = false
    @State private var creating = false
    @State private var newName = ""
    @Binding var path: NavigationPath

    var body: some View {
        NavigationStack(path: $path) {
            List {
                ForEach(playlists) { playlist in
                    NavigationLink(value: playlist) {
                        MediaRow(coverID: playlist.coverArt, title: playlist.name, subtitle: subtitle(playlist))
                    }
                    .swipeActions { Button("Delete", systemImage: "trash", role: .destructive) { delete(playlist) } }
                    .contextMenu {
                        let pinned = model.pins.isPinned(playlist: playlist)
                        Button(pinned ? "Unpin from Home" : "Pin to Home", systemImage: pinned ? "pin.slash" : "pin") { model.pins.toggle(playlist: playlist) }
                        Button("Delete Playlist", systemImage: "trash", role: .destructive) { delete(playlist) }
                    }
                }
            }
            .listStyle(.plain)
            .overlay {
                if !loaded { ProgressView() }
                else if failed { ContentUnavailableView("Couldn't load", systemImage: "wifi.exclamationmark") }
                else if playlists.isEmpty { ContentUnavailableView("No Playlists", systemImage: "music.note.list", description: Text("Playlists from your server will appear here.")) }
            }
            .refreshable { await load() }
            .navigationTitle("Playlists")
            .toolbar {
                if !model.offline {
                    ToolbarItem(placement: .topBarTrailing) { Button("New Playlist", systemImage: "plus") { creating = true } }
                }
            }
            .alert("New Playlist", isPresented: $creating) {
                TextField("Name", text: $newName)
                Button("Cancel", role: .cancel) { newName = "" }
                Button("Create") { create() }
            }
            .task(id: model.contentKey + "|\(model.playlistsVersion)") { await load() }
            .musicDestinations()
        }
    }

    private func subtitle(_ p: Playlist) -> String? {
        p.songCount.map { String(localized: "\($0) songs") }
    }

    private func create() {
        let name = newName.trimmingCharacters(in: .whitespaces)
        newName = ""
        guard !name.isEmpty else { return }
        Task { _ = try? await model.client?.createPlaylist(name: name); await load() }
    }

    private func delete(_ playlist: Playlist) {
        playlists.removeAll { $0.id == playlist.id }
        Task { try? await model.client?.deletePlaylist(id: playlist.id); await load() }
    }

    private func load() async {
        do { playlists = try await model.client?.playlists() ?? []; failed = false }
        catch { failed = playlists.isEmpty }
        loaded = true
    }
}

struct PlaylistView: View {
    let playlist: Playlist
    @Environment(AppModel.self) private var model
    @State private var songs: [Song] = []
    @State private var loaded = false
    @State private var name = ""
    @State private var newName = ""
    @State private var renaming = false
    @State private var confirmDelete = false
    @Environment(\.dismiss) private var dismiss

    /// Чужие плейлисты сервер не даёт менять.
    private var canEdit: Bool { playlist.owner == nil || playlist.owner == model.activeServer?.username }

    var body: some View {
        List {
            VStack(spacing: 14) {
                CoverArtView(id: playlist.coverArt ?? songs.first?.coverArt, size: 220, radius: 16)
                    .shadow(color: .black.opacity(0.18), radius: 18, y: 8)
                Text(name.isEmpty ? playlist.name : name).font(.title2.bold()).multilineTextAlignment(.center)
                if let comment = playlist.comment, !comment.isEmpty { Text(comment).font(.footnote).foregroundStyle(.secondary) }
                HStack(spacing: 12) {
                    Button { model.player?.play(songs) } label: {
                        Label("Play", systemImage: "play.fill").font(.subheadline.weight(.semibold)).lineLimit(1).frame(maxWidth: .infinity)
                    }.buttonStyle(.glassProminent)
                    Button { model.player?.play(songs, startAt: songs.indices.randomElement() ?? 0, shuffled: true) } label: {
                        Label("Shuffle", systemImage: "shuffle").font(.subheadline.weight(.semibold)).lineLimit(1).minimumScaleFactor(0.7).frame(maxWidth: .infinity)
                    }.buttonStyle(.glass)
                    DownloadButton(songs: songs).buttonStyle(.glass).buttonBorderShape(.circle).labelStyle(.iconOnly)
                }
                .controlSize(.large).disabled(songs.isEmpty)
            }
            .frame(maxWidth: .infinity)
            .listRowSeparator(.hidden).listRowBackground(Color.clear)

            ForEach(Array(songs.enumerated()), id: \.offset) { index, song in
                PlayableSongRow(song: song) { model.player?.play(songs, startAt: index) }
                .songMenu(song)
            }
            .onDelete(perform: remove)
            .onMove(perform: move)
        }
        .listStyle(.plain)
        .overlay { if !loaded { ProgressView() } }
        .navigationBarTitleDisplayMode(.inline)
        .toolbar {
            if canEdit {
                ToolbarItem(placement: .topBarTrailing) {
                    Menu("More", systemImage: "ellipsis") {
                        Button("Rename…", systemImage: "pencil") { newName = name; renaming = true }
                        Button("Delete Playlist", systemImage: "trash", role: .destructive) { confirmDelete = true }
                    }
                }
                ToolbarItem(placement: .topBarTrailing) { EditButton() }
            }
        }
        .alert("Rename Playlist", isPresented: $renaming) {
            TextField("Name", text: $newName)
            Button("Cancel", role: .cancel) {}
            Button("Save") { rename() }
        }
        .confirmationDialog("Delete this playlist?", isPresented: $confirmDelete, titleVisibility: .visible) {
            Button("Delete Playlist", role: .destructive) { deletePlaylist() }
        }
        .task(id: playlist.id) {
            songs = (try? await model.client?.playlist(id: playlist.id).songs) ?? []
            loaded = true
        }
    }

    private func remove(_ offsets: IndexSet) {
        let before = songs
        songs.remove(atOffsets: offsets)
        Task {
            do { try await model.client?.removeFromPlaylist(id: playlist.id, indexes: Array(offsets)); model.playlistsVersion += 1 }
            catch { songs = before }
        }
    }

    private func move(_ source: IndexSet, _ destination: Int) {
        let before = songs
        songs.move(fromOffsets: source, toOffset: destination)
        let ids = songs.map(\.id)
        Task {
            do { try await model.client?.replacePlaylistSongs(id: playlist.id, songIDs: ids) }
            catch { songs = before }
        }
    }

    private func rename() {
        let n = newName.trimmingCharacters(in: .whitespaces)
        guard !n.isEmpty else { return }
        Task {
            do { try await model.client?.renamePlaylist(id: playlist.id, name: n); name = n; model.playlistsVersion += 1 } catch {}
        }
    }

    private func deletePlaylist() {
        Task {
            do { try await model.client?.deletePlaylist(id: playlist.id); model.playlistsVersion += 1; dismiss() } catch {}
        }
    }
}
