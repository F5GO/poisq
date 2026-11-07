import SwiftUI
import PoisqCore
import PoisqPlayback

/// Исполнитель: популярные песни, описание, альбомы и похожие артисты.
struct ArtistView: View {
    let artist: Artist
    @Environment(AppModel.self) private var model
    @State private var albums: [Album] = []
    @State private var top: [Song] = []
    @State private var info = ArtistInfo()
    @State private var loaded = false
    @State private var showAllTop = false
    @State private var bioExpanded = false
    @State private var working = false

    private let columns = [GridItem(.adaptive(minimum: 150), spacing: 14)]

    var body: some View {
        ScrollView {
            VStack(spacing: 24) {
                header
                if !top.isEmpty { topSongs }
                if let bio = info.biography { biography(bio) }
                if !albums.isEmpty { albumGrid }
                if !info.similar.isEmpty { similar }
            }
            .padding(.vertical, 20)
        }
        .overlay { if !loaded { ProgressView() } }
        .navigationBarTitleDisplayMode(.inline)
        .task(id: artist.id) { await load() }
    }

    private var header: some View {
        VStack(spacing: 14) {
            CoverArtView(id: artist.coverArt ?? albums.first?.coverArt, size: 140, radius: 70)
            Text(artist.name).font(.title.bold()).multilineTextAlignment(.center)
            HStack(spacing: 12) {
                Button { play(shuffled: false) } label: {
                    Label("Play", systemImage: "play.fill").font(.subheadline.weight(.semibold)).lineLimit(1).frame(maxWidth: .infinity)
                }.buttonStyle(.glassProminent)
                Button { play(shuffled: true) } label: {
                    Label("Shuffle", systemImage: "shuffle").font(.subheadline.weight(.semibold)).lineLimit(1).minimumScaleFactor(0.7).frame(maxWidth: .infinity)
                }.buttonStyle(.glass)
            }
            .controlSize(.large)
            .disabled(working || albums.isEmpty)
        }
        .padding(.horizontal, 20)
    }

    private var topSongs: some View {
        VStack(alignment: .leading, spacing: 8) {
            HStack {
                Text("Top Songs").font(.title2.bold())
                Spacer()
                if top.count > 5 { Button(showAllTop ? "Show Less" : "See All") { withAnimation(.smooth) { showAllTop.toggle() } }.font(.subheadline) }
            }
            ForEach(Array((showAllTop ? top : Array(top.prefix(5))).enumerated()), id: \.element.id) { index, song in
                Button { model.player?.play(top, startAt: index) } label: {
                    HStack(spacing: 12) {
                        CoverArtView(id: song.coverArt, size: 44, radius: 8)
                        VStack(alignment: .leading, spacing: 2) {
                            Text(song.title).lineLimit(1)
                            if let album = song.album { Text(album).font(.subheadline).foregroundStyle(.secondary).lineLimit(1) }
                        }
                        Spacer()
                    }
                    .contentShape(.rect)
                }
                .buttonStyle(.plain)
                .songMenu(song)
            }
        }
        .padding(.horizontal, 20)
    }

    private func biography(_ text: String) -> some View {
        VStack(alignment: .leading, spacing: 8) {
            Text("About").font(.title2.bold())
            Text(text).font(.subheadline).foregroundStyle(.secondary)
                .lineLimit(bioExpanded ? nil : 4)
            Button(bioExpanded ? "Less" : "More") { withAnimation(.smooth) { bioExpanded.toggle() } }.font(.subheadline)
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(.horizontal, 20)
    }

    private var albumGrid: some View {
        VStack(alignment: .leading, spacing: 8) {
            Text("Albums").font(.title2.bold())
            LazyVGrid(columns: columns, spacing: 18) {
                ForEach(albums) { album in
                    NavigationLink(value: album) { AlbumCard(album: album).frame(maxWidth: .infinity, alignment: .leading) }
                        .buttonStyle(.plain).albumMenu(album)
                }
            }
        }
        .padding(.horizontal, 20)
    }

    private var similar: some View {
        VStack(alignment: .leading, spacing: 8) {
            Text("Similar Artists").font(.title2.bold()).padding(.horizontal, 20)
            ScrollView(.horizontal, showsIndicators: false) {
                HStack(spacing: 16) {
                    ForEach(info.similar) { a in
                        NavigationLink(value: a) {
                            VStack(spacing: 6) {
                                CoverArtView(id: a.coverArt, size: 90, radius: 45)
                                Text(a.name).font(.caption).lineLimit(2).multilineTextAlignment(.center)
                            }
                            .frame(width: 96)
                        }
                        .buttonStyle(.plain)
                    }
                }
                .padding(.horizontal, 20)
            }
        }
    }

    private func load() async {
        guard let client = model.client else { loaded = true; return }
        async let result = try? client.artist(id: artist.id)
        async let topResult = try? client.topSongs(artist: artist.name)
        async let infoResult = client.artistInfo(id: artist.id)
        albums = (await result)?.albums.sorted { ($0.year ?? 0) > ($1.year ?? 0) } ?? []
        loaded = true
        top = (await topResult) ?? []
        info = await infoResult
    }

    /// Все песни артиста: альбомы подгружаются параллельно, порядок — как в списке альбомов.
    private func play(shuffled: Bool) {
        guard let client = model.client else { return }
        working = true
        Task {
            defer { working = false }
            let ordered = albums.sorted { ($0.year ?? 0) < ($1.year ?? 0) }
            let all: [[Song]] = await withTaskGroup(of: (Int, [Song]).self) { group in
                for (i, a) in ordered.enumerated() { group.addTask { (i, (try? await client.album(id: a.id).songs) ?? []) } }
                var out = [[Song]](repeating: [], count: ordered.count)
                for await (i, songs) in group { out[i] = songs }
                return out
            }
            let songs = all.flatMap { $0 }
            guard !songs.isEmpty else { return }
            if shuffled { model.player?.play(songs, startAt: songs.indices.randomElement() ?? 0, shuffled: true) }
            else { model.player?.play(songs) }
        }
    }
}
