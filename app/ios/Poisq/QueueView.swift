import SwiftUI
import PoisqCore
import PoisqPlayback

/// Отдельный экран очереди, открываемый с карточки «Продолжить» на главной.
struct QueueView: View {
    let player: Player
    @State private var editing = false

    var body: some View {
        List {
            QueueSections(player: player, editing: $editing)
        }
        .listStyle(.plain)
        .environment(\.editMode, .constant(editing ? .active : .inactive))
        .navigationTitle("Queue")
        .navigationBarTitleDisplayMode(.inline)
    }
}

/// Очередь под основным экраном плеера: «Далее» и автоподборка. Строки списка, поэтому правка и перенос работают как раньше.
struct QueueSections: View {
    let player: Player
    @Binding var editing: Bool
    /// Снимок очереди на момент открытия. После выбора трека сохраняем расположение строк,
    /// чтобы пользователь видел выбранный трек там, где нажал его.
    @State private var displayedQueue: [Song] = []
    @State private var displayedAutoIDs: Set<String> = []

    private var displayedManual: [Song] {
        displayedQueue.filter { !displayedAutoIDs.contains($0.id) }
    }

    private var displayedAuto: [Song] {
        displayedQueue.filter { displayedAutoIDs.contains($0.id) }
    }

    var body: some View {
        Group {
            Section {
                modes
                    .listRowInsets(EdgeInsets(top: 4, leading: 16, bottom: 4, trailing: 16))
                    .listRowSeparator(.hidden).listRowBackground(Color.clear)
                    .moveDisabled(true).deleteDisabled(true)
            }
            if !displayedManual.isEmpty {
                Section {
                    headerRow {
                        HStack {
                            sectionTitle("Queue", systemImage: "list.bullet")
                        }
                    }
                    ForEach(displayedManual) { song in
                        row(song)
                            .deleteDisabled(song == player.current)
                            .moveDisabled(song == player.current)
                    }
                    .onDelete(perform: removeManual)
                    .onMove(perform: moveManual)
                }
            }
            Section {
                headerRow {
                    HStack {
                        VStack(alignment: .leading, spacing: 2) {
                            sectionTitle("Autoplay", systemImage: "infinity")
                            Text(player.autoplay ? "Similar tracks" : "Off").font(.footnote).foregroundStyle(.secondary)
                        }
                        Spacer()
                        if player.autoplay {
                            Button("Refresh", systemImage: "arrow.clockwise") { player.refreshMix() }
                                .labelStyle(.iconOnly).foregroundStyle(.secondary)
                        }
                    }
                }
                if player.autoplay {
                    ForEach(displayedAuto) { song in
                        row(song)
                            .deleteDisabled(song == player.current)
                            .moveDisabled(true)
                    }
                    .onDelete(perform: removeAuto)
                    if displayedAuto.isEmpty {
                        Group {
                            if player.mixLoading {
                                HStack { ProgressView(); Text("Finding similar tracks…").foregroundStyle(.secondary) }
                            } else {
                                Text("Nothing found. Tap refresh to try again.").foregroundStyle(.secondary)
                            }
                        }
                        .listRowBackground(Color.clear)
                    }
                }
            }
        }
        .onAppear { if displayedQueue.isEmpty { refreshDisplayedQueue() } }
        .onChange(of: editing) { _, active in if active { refreshDisplayedQueue() } }
        .onChange(of: player.current?.id) { _, currentID in
            if let currentID, !displayedQueue.contains(where: { $0.id == currentID }) {
                refreshDisplayedQueue()
            }
        }
        .onChange(of: player.queue.map(\.id)) { _, _ in mergeNewQueueItems() }
    }

    /// Режимы очереди: вперемешку, повтор, автоподборка (∞) и правка списка.
    private var modes: some View {
        HStack(spacing: 10) {
            pill("shuffle", on: player.shuffleOn, label: "Shuffle") { player.toggleShuffle() }
            pill(player.repeatMode == .one ? "repeat.1" : "repeat", on: player.repeatMode != .off, label: "Repeat") { player.cycleRepeat() }
            pill("infinity", on: player.autoplay, label: "Autoplay") { player.autoplay.toggle() }
            pill(editing ? "checkmark" : "arrow.up.arrow.down", on: editing, label: "Edit Queue") { withAnimation(.smooth) { editing.toggle() } }
        }
        .sensoryFeedback(.selection, trigger: player.autoplay)
    }

    private func pill(_ icon: String, on: Bool, label: LocalizedStringKey, action: @escaping () -> Void) -> some View {
        Button(action: action) {
            Image(systemName: icon).font(.system(size: 18, weight: .semibold))
                .contentTransition(.symbolEffect(.replace))
                .frame(maxWidth: .infinity, minHeight: 44)
                .foregroundStyle(on ? Color.white : .primary)
                .background(on ? Color.accentColor : Color.primary.opacity(0.07), in: .capsule)
        }
        .buttonStyle(.borderless)
        .accessibilityLabel(label)
        .accessibilityValue(on ? "On" : "Off")
    }

    /// Заголовок блока — обычная строка списка, чтобы не прилипал серой полосой при прокрутке.
    private func headerRow<Content: View>(@ViewBuilder _ content: () -> Content) -> some View {
        content()
            .padding(.top, 8)
            .listRowSeparator(.hidden)
            .listRowBackground(Color.clear)
            .moveDisabled(true).deleteDisabled(true)
    }

    private func sectionTitle(_ title: LocalizedStringKey, systemImage: String) -> some View {
        Label(title, systemImage: systemImage).font(.title3.bold()).foregroundStyle(.primary).textCase(nil)
    }

    private func row(_ song: Song) -> some View {
        HStack(spacing: 8) {
            PlayableSongRow(song: song, highlighted: song == player.current) {
                select(song)
            }
            if song == player.current {
                Image(systemName: player.isPlaying ? "waveform" : "pause.fill")
                    .font(.subheadline.weight(.semibold))
                    .foregroundStyle(.tint)
                    .symbolEffect(.variableColor.iterative, isActive: player.isPlaying)
                    .accessibilityLabel("Now Playing")
            }
        }
        .songMenu(song, button: false)
        .listRowBackground(Color.clear)
    }

    private func select(_ song: Song) {
        guard song != player.current else { return }
        let wasLast = displayedQueue.last == song
        if let index = player.upcoming.firstIndex(of: song) {
            player.jump(toUpcoming: index)
        } else if let index = displayedQueue.firstIndex(of: song) {
            player.play(displayedQueue, startAt: index)
        }
        if wasLast { refreshDisplayedQueue() }
    }

    private func refreshDisplayedQueue() {
        displayedQueue = player.queue
        displayedAutoIDs = player.autoIDs
        if let current = player.current { displayedAutoIDs.remove(current.id) }
    }

    /// Автоподборка может дополнить очередь, пока экран открыт. Добавляем новые строки в конец,
    /// но не удаляем старые и не меняем их позиции.
    private func mergeNewQueueItems() {
        let shown = Set(displayedQueue.map(\.id))
        displayedQueue += player.queue.filter { !shown.contains($0.id) }
        displayedAutoIDs.formUnion(player.autoIDs)
    }

    private func removeManual(at offsets: IndexSet) {
        let songs = offsets.compactMap { displayedManual.indices.contains($0) ? displayedManual[$0] : nil }
            .filter { $0 != player.current }
        let actual = IndexSet(songs.compactMap { player.upcoming.firstIndex(of: $0) })
        if !actual.isEmpty { player.removeUpcoming(at: actual) }
        let ids = Set(songs.map(\.id))
        displayedQueue.removeAll { ids.contains($0.id) }
    }

    private func removeAuto(at offsets: IndexSet) {
        let songs = offsets.compactMap { displayedAuto.indices.contains($0) ? displayedAuto[$0] : nil }
            .filter { $0 != player.current }
        let actual = IndexSet(songs.compactMap { player.upcoming.firstIndex(of: $0) })
        if !actual.isEmpty { player.removeUpcoming(at: actual) }
        let ids = Set(songs.map(\.id))
        displayedQueue.removeAll { ids.contains($0.id) }
        displayedAutoIDs.subtract(ids)
    }

    private func moveManual(from source: IndexSet, to destination: Int) {
        // В режиме редактирования снимок всегда начинается с текущего трека.
        let shift = displayedManual.first == player.current ? 1 : 0
        let upcomingSource = IndexSet(source.compactMap { $0 >= shift ? $0 - shift : nil })
        guard !upcomingSource.isEmpty else { return }
        player.moveUpcoming(from: upcomingSource, to: max(0, destination - shift))
        refreshDisplayedQueue()
    }
}
