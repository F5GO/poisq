import SwiftUI
import PoisqCore
import PoisqPlayback

struct AlbumView: View {
    let album: Album
    @Environment(AppModel.self) private var model
    @State private var songs: [Song] = []
    @State private var detailedAlbum: Album?
    @State private var loaded = false
    @State private var failed = false

    var body: some View {
        List {
            header
                .listRowSeparator(.hidden).listRowBackground(Color.clear)
            ForEach(Array(songs.enumerated()), id: \.element.id) { index, song in
                HStack(spacing: 14) {
                    Button { model.player?.play(songs, startAt: index) } label: {
                        Text(song.track.map(String.init) ?? "").font(.subheadline.monospacedDigit())
                            .foregroundStyle(.secondary).frame(width: 24, alignment: .trailing)
                    }
                    .buttonStyle(.plain)
                    VStack(alignment: .leading, spacing: 2) {
                        Button { model.player?.play(songs, startAt: index) } label: {
                            Text(song.title).lineLimit(1)
                        }
                        .buttonStyle(.plain)
                        if song.artist != album.artist || (song.artists?.count ?? 0) > 1 {
                            SongArtistLinks(song: song, font: .caption)
                        }
                    }
                    Spacer()
                    if model.isDownloaded(song) { Image(systemName: "arrow.down.circle.fill").font(.footnote).foregroundStyle(.secondary) }
                    Text(Self.duration(song.duration)).font(.subheadline.monospacedDigit()).foregroundStyle(.secondary)
                }
                .songMenu(song)
            }
            if failed {
                ContentUnavailableView("Couldn't load", systemImage: "wifi.exclamationmark").listRowBackground(Color.clear)
            }
        }
        .listStyle(.plain)
        .navigationBarTitleDisplayMode(.inline)
        .overlay { if !loaded && !failed { ProgressView() } }
        .task(id: album.id) { await load() }
    }

    private var header: some View {
        VStack(spacing: 14) {
            CoverArtView(id: album.coverArt, size: 260, radius: 16)
                .shadow(color: .black.opacity(0.18), radius: 18, y: 8)
            VStack(spacing: 4) {
                Text(album.name).font(.title2.bold()).multilineTextAlignment(.center)
                if albumArtists.count > 1 {
                    Menu {
                        ForEach(albumArtists) { artist in
                            Button(artist.name) { model.pendingArtist = artist }
                        }
                    } label: {
                        artistLabel
                    }
                    .menuStyle(.button)
                    .buttonStyle(.plain)
                    .accessibilityHint("Show Artist")
                } else if let artist = albumArtists.first {
                    Button { model.pendingArtist = artist } label: {
                        artistLabel
                    }
                    .buttonStyle(.plain)
                    .accessibilityHint("Show Artist")
                } else {
                    Text(album.artist ?? "").font(.title3).foregroundStyle(.secondary)
                }
                Text(subtitle).font(.footnote).foregroundStyle(.secondary)
            }
            HStack(spacing: 12) {
                Button { model.player?.play(songs) } label: {
                    HStack(spacing: 8) {
                        Image(systemName: "play.fill")
                        Text("Play")
                    }
                    .font(.headline).lineLimit(1).minimumScaleFactor(0.8).frame(maxWidth: .infinity)
                }
                .buttonStyle(.glassProminent)
                Button { model.player?.play(songs, startAt: songs.indices.randomElement() ?? 0, shuffled: true) } label: {
                    Image(systemName: "shuffle").font(.headline)
                }
                .buttonStyle(.glass).buttonBorderShape(.circle)
                .accessibilityLabel("Shuffle")
                DownloadButton(songs: songs)
                    .buttonStyle(.glass).buttonBorderShape(.circle).labelStyle(.iconOnly)
            }
            .controlSize(.large)
            .padding(.horizontal, 4)
            .disabled(songs.isEmpty)
        }
        .frame(maxWidth: .infinity)
        .padding(.vertical, 8)
    }

    private var artistLabel: some View {
        Text(album.artist ?? ListFormatter.localizedString(byJoining: albumArtists.map(\.name)))
            .font(.title3)
            .foregroundStyle(.tint)
            .multilineTextAlignment(.center)
    }

    /// Сначала используем исполнителей самого альбома. Если сервер их не прислал,
    /// собираем уникальных исполнителей из треков, не сводя их к общему artistId.
    private var albumArtists: [Artist] {
        let supplied = detailedAlbum?.artists ?? album.artists
        if let supplied, !supplied.isEmpty { return supplied }

        var seen = Set<String>()
        let fromSongs = songs.flatMap { song -> [Artist] in
            if let artists = song.artists, !artists.isEmpty { return artists }
            if let id = song.artistId, let name = song.artist { return [Artist(id: id, name: name)] }
            return []
        }
        let unique = fromSongs.filter { seen.insert($0.id).inserted }
        if !unique.isEmpty { return unique }

        if let id = album.artistId, let name = album.artist { return [Artist(id: id, name: name)] }
        return []
    }

    private var subtitle: String {
        var parts: [String] = []
        if let y = album.year { parts.append(String(y)) }
        let n = songs.isEmpty ? (album.songCount ?? 0) : songs.count
        if n > 0 { parts.append(String(localized: "\(n) songs")) }
        return parts.joined(separator: " · ")
    }

    private func load() async {
        let local = model.downloadedSongs.filter { $0.albumId == album.id }.sorted { ($0.track ?? 0) < ($1.track ?? 0) }
        if model.offline { songs = local; loaded = true; return }       // офлайн-режим: только то, что на устройстве
        do {
            if let result = try await model.client?.album(id: album.id) {
                detailedAlbum = result.album
                songs = result.songs
            } else {
                songs = []
            }
            failed = false
        }
        catch { songs = local; failed = local.isEmpty }                       // нет сети — показываем загруженное
        loaded = true
    }

    private static func duration(_ seconds: Int?) -> String {
        guard let s = seconds else { return "" }
        return "\(s / 60):" + String(format: "%02d", s % 60)
    }
}
