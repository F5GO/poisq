import SwiftUI

/// «Настроить главную»: порядок блоков и их видимость.
struct HomeEditView: View {
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss
    @AppStorage("homeSongsMode") private var songsMode = false
    @AppStorage("homeSongsSort") private var songsSort = SongsOrder.random.rawValue
    @State private var editingQuickAccess = false
    @State private var editingQuickActions = false

    var body: some View {
        @Bindable var layout = model.homeLayout
        NavigationStack {
            List {
                Section {
                    Toggle("Songs Mode", systemImage: "music.note.list", isOn: $songsMode.animation())
                    if songsMode {
                        Picker("Order", selection: $songsSort) {
                            ForEach(SongsOrder.allCases) { Text($0.title).tag($0.rawValue) }
                        }
                        .pickerStyle(.segmented)
                    }
                } header: {
                    Text("Display Mode")
                } footer: {
                    Text("Home shows only an endless list of random songs.")
                }
                Section("Sections") {
                    ForEach(layout.entries) { entry in
                        HStack(spacing: 14) {
                            Image(systemName: entry.item.icon).frame(width: 26).foregroundStyle(entry.isVisible ? .primary : .tertiary)
                            Text(entry.item.title).foregroundStyle(entry.isVisible ? .primary : .tertiary)
                            Spacer()
                            Button {
                                withAnimation(.snappy) { layout.setVisible(entry.item, !entry.isVisible) }
                            } label: {
                                Image(systemName: entry.isVisible ? "eye.fill" : "eye.slash.fill")
                                    .contentTransition(.symbolEffect(.replace))
                                    .foregroundStyle(entry.isVisible ? Color.primary : Color.secondary)
                            }
                            .buttonStyle(.borderless)
                            .sensoryFeedback(.selection, trigger: entry.isVisible)
                            .accessibilityLabel(entry.isVisible ? "Hide" : "Show")
                        }
                        .deleteDisabled(true)
                    }
                    .onMove { layout.move(from: $0, to: $1) }
                }

                Section("Quick Actions") {
                    Button { editingQuickActions = true } label: {
                        Label("Configure Quick Actions", systemImage: "square.grid.2x2")
                    }
                    .foregroundStyle(.primary)
                }

                Section("Quick Access") {
                    Button { editingQuickAccess = true } label: {
                        Label("Configure Quick Access", systemImage: "rectangle.grid.2x2")
                    }
                    .foregroundStyle(.primary)
                }

                Section("Recently Played") {
                    Stepper(value: $layout.recentlyPlayedLimit, in: 1...6) {
                        LabeledContent("Number of Items", value: "\(layout.recentlyPlayedLimit)")
                    }
                }

                if !layout.isDefault {
                    Section {
                        Button("Reset to Default", role: .destructive) { withAnimation { layout.reset() } }
                    }
                }
            }
            .environment(\.editMode, .constant(.active))      // ручки перетаскивания всегда видны
            .navigationTitle("Customize Home")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .confirmationAction) {
                    Button("Done", systemImage: "checkmark") { dismiss() }
                }
            }
        }
        .presentationDetents([.large])
        .sheet(isPresented: $editingQuickActions) {
            NavigationStack { QuickActionsEditView() }
        }
        .sheet(isPresented: $editingQuickAccess) {
            NavigationStack { QuickAccessEditView() }
        }
    }

}

private struct QuickAccessEditView: View {
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss

    var body: some View {
        @Bindable var layout = model.homeLayout
        Form {
            Section("Content") {
                Toggle("Pinned Items", systemImage: "pin.fill", isOn: $layout.quickAccessShowsPins)
                Toggle("Recently Played Albums", systemImage: "clock", isOn: $layout.quickAccessShowsRecent)
            }
            Section("Number of Cards") {
                Picker("Number of Cards", selection: $layout.quickAccessLimit) {
                    Text("2").tag(2)
                    Text("4").tag(4)
                    Text("6").tag(6)
                }
                .pickerStyle(.segmented)
            }
            if layout.quickAccessShowsPins {
                Section("Pinned Items") {
                    if model.pins.pins.isEmpty {
                        Text("Pin albums and playlists from their menus.")
                            .foregroundStyle(.secondary)
                    } else {
                        ForEach(model.pins.pins) { pin in
                            HStack(spacing: 10) {
                                CoverArtView(id: pin.album?.coverArt ?? pin.playlist?.coverArt, size: 40, radius: 7)
                                VStack(alignment: .leading, spacing: 2) {
                                    Text(pin.album?.name ?? pin.playlist?.name ?? "").lineLimit(1)
                                    Text(pin.album != nil ? String(localized: "Album") : String(localized: "Playlist"))
                                        .font(.caption).foregroundStyle(.secondary)
                                }
                            }
                        }
                        .onMove { model.pins.move(from: $0, to: $1) }
                        .onDelete { model.pins.remove(at: $0) }
                    }
                }
            }
        }
        .environment(\.editMode, .constant(.active))
        .navigationTitle("Quick Access")
        .navigationBarTitleDisplayMode(.inline)
        .toolbar {
            ToolbarItem(placement: .confirmationAction) {
                Button("Done", systemImage: "checkmark") { dismiss() }
            }
        }
    }
}

private struct QuickActionsEditView: View {
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss

    var body: some View {
        let layout = model.homeLayout
        List {
            Section {
                ForEach(layout.quickActionEntries) { entry in
                    HStack(spacing: 14) {
                        Image(systemName: entry.item.icon)
                            .frame(width: 26)
                            .foregroundStyle(entry.isVisible ? .primary : .tertiary)
                        Text(entry.item.title)
                            .foregroundStyle(entry.isVisible ? .primary : .tertiary)
                        Spacer()
                        Button {
                            withAnimation(.snappy) {
                                layout.setQuickActionVisible(entry.item, !entry.isVisible)
                            }
                        } label: {
                            Image(systemName: entry.isVisible ? "eye.fill" : "eye.slash.fill")
                                .contentTransition(.symbolEffect(.replace))
                                .foregroundStyle(entry.isVisible ? Color.primary : Color.secondary)
                        }
                        .buttonStyle(.borderless)
                        .sensoryFeedback(.selection, trigger: entry.isVisible)
                        .accessibilityLabel(entry.isVisible ? "Hide" : "Show")
                    }
                    .deleteDisabled(true)
                }
                .onMove { layout.moveQuickActions(from: $0, to: $1) }
            } footer: {
                Text("Choose which actions appear on Home and arrange their order.")
            }
        }
        .environment(\.editMode, .constant(.active))
        .navigationTitle("Quick Actions")
        .navigationBarTitleDisplayMode(.inline)
        .toolbar {
            ToolbarItem(placement: .confirmationAction) {
                Button("Done", systemImage: "checkmark") { dismiss() }
            }
        }
    }
}
