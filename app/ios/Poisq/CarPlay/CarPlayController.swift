import CarPlay
import UIKit
import PoisqCore
import PoisqPlayback

/// Дерево шаблонов CarPlay: вкладки каталога + «Сейчас играет». Очередь и воспроизведение — только через `Player`.
@MainActor
final class CarPlayController: NSObject, CPNowPlayingTemplateObserver {
    private let model: AppModel
    private let interface: CPInterfaceController
    private var observing = true

    /// CarPlay ограничивает длину списков; запрашиваем не больше.
    private var limit: Int { max(CPListTemplate.maximumItemCount, 1) }

    init(model: AppModel, interface: CPInterfaceController) {
        self.model = model
        self.interface = interface
    }

    // MARK: Жизненный цикл

    func start() {
        let recent = listTemplate(title: String(localized: "Recent"), systemImage: "clock")
        let playlists = listTemplate(title: String(localized: "Playlists"), systemImage: "music.note.list")
        let artists = listTemplate(title: String(localized: "Artists"), systemImage: "music.mic")
        let albums = listTemplate(title: String(localized: "Albums"), systemImage: "square.stack")
        let favorites = listTemplate(title: String(localized: "Favorites"), systemImage: "heart")
        let tabs = CPTabBarTemplate(templates: [recent, playlists, artists, albums, favorites])
        interface.setRootTemplate(tabs, animated: false, completion: nil)

        let nowPlaying = CPNowPlayingTemplate.shared
        nowPlaying.isUpNextButtonEnabled = true
        nowPlaying.isAlbumArtistButtonEnabled = false
        nowPlaying.add(self)
        refreshNowPlayingButtons()
        trackPlayer()

        load(recent) { [self] c in
            try await c.albumList(type: "recent", size: limit, musicFolderId: model.selectedLibraryID).map(albumItem)
        }
        load(playlists) { [self] c in
            try await c.playlists().prefix(limit).map(playlistItem)
        }
        load(artists) { [self] c in
            try await c.artists(musicFolderId: model.selectedLibraryID).prefix(limit).map(artistItem)
        }
        load(albums) { [self] c in
            try await c.albumList(type: "alphabeticalByName", size: limit, musicFolderId: model.selectedLibraryID).map(albumItem)
        }
        load(favorites) { [self] c in
            let songs = try await c.starred(musicFolderId: model.selectedLibraryID).songs
            return songItems(Array(songs.prefix(limit)), all: songs)
        }
    }

    func stop() {
        observing = false
        CPNowPlayingTemplate.shared.remove(self)
    }

    // MARK: Шаблоны списков

    private func listTemplate(title: String, systemImage: String) -> CPListTemplate {
        let t = CPListTemplate(title: title, sections: [])
        t.tabImage = UIImage(systemName: systemImage) ?? UIImage()
        t.emptyViewSubtitleVariants = [String(localized: "Loading…")]
        return t
    }

    /// Заполняет шаблон результатом запроса; ошибка и пустой ответ дают понятную подпись вместо пустого экрана.
    private func load(_ template: CPListTemplate, items: @escaping (SubsonicClient) async throws -> [CPListItem]) {
        guard let client = model.client else {
            template.emptyViewSubtitleVariants = [String(localized: "Sign in on iPhone")]
            return
        }
        Task { [weak self, weak template] in
            do {
                let result = try await items(client)
                template?.updateSections([CPListSection(items: result)])
                if result.isEmpty { template?.emptyViewSubtitleVariants = [String(localized: "Nothing here yet")] }
            } catch {
                template?.emptyViewSubtitleVariants = [String(localized: "Couldn't load")]
            }
            _ = self
        }
    }

    private func push(title: String, items: @escaping (SubsonicClient) async throws -> [CPListItem]) {
        let t = CPListTemplate(title: title, sections: [])
        t.emptyViewSubtitleVariants = [String(localized: "Loading…")]
        interface.pushTemplate(t, animated: true, completion: nil)
        load(t, items: items)
    }

    // MARK: Элементы

    private func albumItem(_ a: Album) -> CPListItem {
        let item = CPListItem(text: a.name, detailText: a.artist)
        item.accessoryType = .disclosureIndicator
        setCover(a.coverArt, on: item)
        item.handler = { [weak self] _, done in
            self?.push(title: a.name) { c in
                let songs = try await c.album(id: a.id).songs
                return self?.songItems(Array(songs.prefix(CPListTemplate.maximumItemCount)), all: songs) ?? []
            }
            done()
        }
        return item
    }

    private func playlistItem(_ p: Playlist) -> CPListItem {
        let item = CPListItem(text: p.name, detailText: p.songCount.map { String(localized: "\($0) songs") })
        item.accessoryType = .disclosureIndicator
        setCover(p.coverArt, on: item)
        item.handler = { [weak self] _, done in
            self?.push(title: p.name) { c in
                let songs = try await c.playlist(id: p.id).songs
                return self?.songItems(Array(songs.prefix(CPListTemplate.maximumItemCount)), all: songs) ?? []
            }
            done()
        }
        return item
    }

    private func artistItem(_ a: Artist) -> CPListItem {
        let item = CPListItem(text: a.name, detailText: a.albumCount.map { String(localized: "\($0) albums") })
        item.accessoryType = .disclosureIndicator
        setCover(a.coverArt, on: item)
        item.handler = { [weak self] _, done in
            self?.push(title: a.name) { c in
                try await c.artist(id: a.id).albums.map { self?.albumItem($0) }.compactMap { $0 }
            }
            done()
        }
        return item
    }

    /// Песни: нажатие запускает список как очередь-поток — `all` целиком, начиная с нажатой.
    private func songItems(_ shown: [Song], all: [Song]) -> [CPListItem] {
        shown.enumerated().map { index, song in
            let item = CPListItem(text: song.title, detailText: song.displayArtist)
            item.isExplicitContent = false
            setCover(song.coverArt, on: item)
            item.handler = { [weak self] _, done in
                self?.model.player?.play(all, startAt: index, continuation: { [] })
                self?.showNowPlaying()
                done()
            }
            return item
        }
    }

    private func setCover(_ id: String?, on item: CPListItem) {
        guard let id else { return }
        let size = CPListItem.maximumImageSize
        let pixels = Int(max(size.width, size.height) * 2)
        Task { [weak item, model] in
            guard let image = await model.coverImage(id, pixels: pixels) else { return }
            item?.setImage(image)
        }
    }

    // MARK: Сейчас играет

    private func showNowPlaying() {
        let now = CPNowPlayingTemplate.shared
        if interface.topTemplate !== now { interface.pushTemplate(now, animated: true, completion: nil) }
    }

    /// Следим за плеером (`@Observable`) и обновляем кнопки «Сейчас играет» при смене трека, повтора, перемешивания и избранного.
    private func trackPlayer() {
        guard observing else { return }
        withObservationTracking {
            _ = model.player?.current
            _ = model.player?.shuffleOn
            _ = model.player?.repeatMode
            _ = model.player?.favoriteIDs
        } onChange: { [weak self] in
            Task { @MainActor in
                guard let self, self.observing else { return }
                self.refreshNowPlayingButtons()
                self.trackPlayer()
            }
        }
    }

    private func refreshNowPlayingButtons() {
        let player = model.player
        let shuffle = CPNowPlayingShuffleButton { [weak player] _ in player?.toggleShuffle() }
        let repeatButton = CPNowPlayingRepeatButton { [weak player] _ in player?.cycleRepeat() }
        let isFav = player?.current.map { player?.isFavorite($0) ?? false } ?? false
        let heart = UIImage(systemName: isFav ? "heart.fill" : "heart") ?? UIImage()
        let favorite = CPNowPlayingImageButton(image: heart) { [weak player] _ in
            guard let player, let song = player.current else { return }
            Task { await player.toggleFavorite(song) }
        }
        CPNowPlayingTemplate.shared.updateNowPlayingButtons([shuffle, repeatButton, favorite])
    }

    // MARK: CPNowPlayingTemplateObserver

    /// Кнопка «Далее» (очередь): текущий трек всегда первый, затем предстоящие.
    nonisolated func nowPlayingTemplateUpNextButtonTapped(_ nowPlayingTemplate: CPNowPlayingTemplate) {
        Task { @MainActor [weak self] in
            guard let self, let player = model.player else { return }
            let queue = Array(player.queue.prefix(CPListTemplate.maximumItemCount))
            let items = queue.enumerated().map { index, song -> CPListItem in
                let item = CPListItem(text: song.title, detailText: song.displayArtist)
                item.isPlaying = index == 0
                setCover(song.coverArt, on: item)
                item.handler = { [weak self] _, done in
                    if index > 0 { player.jump(toUpcoming: index - 1) }
                    self?.interface.popTemplate(animated: true, completion: nil)
                    done()
                }
                return item
            }
            let t = CPListTemplate(title: String(localized: "Up Next"), sections: [CPListSection(items: items)])
            t.emptyViewSubtitleVariants = [String(localized: "Nothing here yet")]
            interface.pushTemplate(t, animated: true, completion: nil)
        }
    }
}
