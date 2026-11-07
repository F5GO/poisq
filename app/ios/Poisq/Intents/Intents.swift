import AppIntents
import PoisqCore
import PoisqPlayback

enum PoisqIntentError: Error, CustomLocalizedStringResourceConvertible {
    case notSignedIn, notFound
    var localizedStringResource: LocalizedStringResource {
        switch self {
        case .notSignedIn: "Sign in to poisq first."
        case .notFound: "Couldn't find anything to play."
        }
    }
}

struct PlayPlaylistIntent: AudioPlaybackIntent {
    static let title: LocalizedStringResource = "Play Playlist"
    static let description = IntentDescription("Plays a playlist from your server.")
    @Parameter(title: "Playlist") var playlist: PlaylistEntity

    static var parameterSummary: some ParameterSummary { Summary("Play \(\.$playlist)") }

    @MainActor func perform() async throws -> some IntentResult {
        let model = AppModel.shared
        guard let player = model.player, let client = model.client else { throw PoisqIntentError.notSignedIn }
        guard let songs = try? await client.playlist(id: playlist.id).songs, !songs.isEmpty else { throw PoisqIntentError.notFound }
        player.play(songs)
        return .result()
    }
}

struct PlayRandomSongsIntent: AudioPlaybackIntent {
    static let title: LocalizedStringResource = "Play Random Songs"
    static let description = IntentDescription("Shuffles your whole library.")

    @MainActor func perform() async throws -> some IntentResult {
        guard let player = AppModel.shared.player else { throw PoisqIntentError.notSignedIn }
        await player.shuffleLibrary()
        return .result()
    }
}

struct PauseIntent: AudioPlaybackIntent {
    static let title: LocalizedStringResource = "Pause"
    @MainActor func perform() async throws -> some IntentResult {
        guard AppModel.shared.player != nil else { throw PoisqIntentError.notSignedIn }
        PlaybackCommands.handler?(.pause)
        return .result()
    }
}

struct FavoriteCurrentIntent: AppIntent {
    static let title: LocalizedStringResource = "Add Current Song to Favorites"
    @MainActor func perform() async throws -> some IntentResult {
        guard let player = AppModel.shared.player else { throw PoisqIntentError.notSignedIn }
        guard let song = player.current else { throw PoisqIntentError.notFound }
        if !player.isFavorite(song) { await player.toggleFavorite(song) }
        return .result()
    }
}

// MARK: Открытие результатов Spotlight

struct OpenAlbumIntent: OpenIntent {
    static let title: LocalizedStringResource = "Open Album"
    static let supportedModes: IntentModes = .foreground
    @Parameter(title: "Album") var target: AlbumEntity
    @MainActor func perform() async throws -> some IntentResult {
        SystemIntegration.shared.open(.album(id: target.id, name: target.name)); return .result()
    }
}

struct OpenArtistIntent: OpenIntent {
    static let title: LocalizedStringResource = "Open Artist"
    static let supportedModes: IntentModes = .foreground
    @Parameter(title: "Artist") var target: ArtistEntity
    @MainActor func perform() async throws -> some IntentResult {
        SystemIntegration.shared.open(.artist(id: target.id, name: target.name)); return .result()
    }
}

struct OpenPlaylistIntent: OpenIntent {
    static let title: LocalizedStringResource = "Open Playlist"
    static let supportedModes: IntentModes = .foreground
    @Parameter(title: "Playlist") var target: PlaylistEntity
    @MainActor func perform() async throws -> some IntentResult {
        SystemIntegration.shared.open(.playlist(id: target.id, name: target.name)); return .result()
    }
}

// MARK: Фразы Siri (ru/en — в AppShortcuts.xcstrings)

struct PoisqShortcuts: AppShortcutsProvider {
    static var appShortcuts: [AppShortcut] {
        AppShortcut(intent: PlayPlaylistIntent(),
                    phrases: ["Play playlist \(\.$playlist) in \(.applicationName)", "Play \(\.$playlist) in \(.applicationName)"],
                    shortTitle: "Play Playlist", systemImageName: "music.note.list")
        AppShortcut(intent: PlayRandomSongsIntent(),
                    phrases: ["Play random songs in \(.applicationName)", "Shuffle \(.applicationName)"],
                    shortTitle: "Random Songs", systemImageName: "shuffle")
        AppShortcut(intent: PauseIntent(),
                    phrases: ["Pause \(.applicationName)"],
                    shortTitle: "Pause", systemImageName: "pause.fill")
        AppShortcut(intent: NextTrackIntent(),
                    phrases: ["Next song in \(.applicationName)"],
                    shortTitle: "Next Song", systemImageName: "forward.fill")
        AppShortcut(intent: PreviousTrackIntent(),
                    phrases: ["Previous song in \(.applicationName)"],
                    shortTitle: "Previous Song", systemImageName: "backward.fill")
        AppShortcut(intent: FavoriteCurrentIntent(),
                    phrases: ["Add current song to favorites in \(.applicationName)"],
                    shortTitle: "Favorite Current Song", systemImageName: "heart")
    }
}
