import SwiftUI
import PoisqCore
import PoisqPlayback

/// Очередь под основным экраном плеера: «Далее» и автоподборка. Строки списка, поэтому правка и перенос работают как раньше.
struct QueueSections: View {
    let player: Player
    @Binding var editing: Bool

    var body: some View {
        Group {
            Section {
                modes
                    .listRowInsets(EdgeInsets(top: 4, leading: 16, bottom: 4, trailing: 16))
                    .listRowSeparator(.hidden).listRowBackground(Color.clear)
                    .moveDisabled(true).deleteDisabled(true)
            }
            if !player.manualUpcoming.isEmpty {
                Section {
                    headerRow {
                        HStack {
                            sectionTitle("Up Next", systemImage: "list.bullet")
                        }
                    }
                    ForEach(player.manualUpcoming) { song in row(song) }
                        .onDelete { offsets in player.removeUpcoming(at: IndexSet(offsets)) }
                        .onMove { player.moveUpcoming(from: $0, to: $1) }
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
                    ForEach(player.autoUpcoming) { song in row(song) }
                        .onDelete { offsets in
                            let base = player.manualUpcoming.count
                            player.removeUpcoming(at: IndexSet(offsets.map { $0 + base }))
                        }
                    if player.autoUpcoming.isEmpty {
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
        Button {
            if let i = player.upcoming.firstIndex(of: song) { player.jump(toUpcoming: i) }
        } label: {
            HStack(spacing: 12) {
                CoverArtView(id: song.coverArt, size: 48, radius: 8)
                VStack(alignment: .leading, spacing: 2) {
                    Text(song.title).lineLimit(1)
                    Text(song.displayArtist ?? "").font(.subheadline).foregroundStyle(.secondary).lineLimit(1)
                }
                Spacer()
                if player.source(of: song) == .streaming {
                    Image(systemName: "circle.dashed").font(.footnote).foregroundStyle(.tertiary)   // ещё не на устройстве
                }
            }
            .contentShape(.rect)
        }
        .buttonStyle(.plain)
        .songMenu(song, button: false)
        .listRowBackground(Color.clear)
    }
}
