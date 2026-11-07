import SwiftUI

/// «Настроить главную»: порядок блоков и их видимость.
struct HomeEditView: View {
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss
    @AppStorage("homeSongsMode") private var songsMode = false
    @AppStorage("homeSongsSort") private var songsSort = SongsOrder.random.rawValue

    var body: some View {
        let layout = model.homeLayout
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
                } footer: {
                    Text("Home shows only an endless list of random songs.")
                }
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
    }
}
