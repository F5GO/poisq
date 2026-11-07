import SwiftUI
import PoisqCore
import PoisqPlayback

/// Интернет-радио сервера. Раздел виден, только если сервер поддерживает радио (`Capabilities.hasRadio`).
struct RadioView: View {
    @Environment(AppModel.self) private var model
    @State private var stations: [RadioStation] = []
    @State private var loaded = false
    @State private var editing: RadioStation?
    @State private var adding = false
    @State private var failed = false

    var body: some View {
        List {
            ForEach(stations) { station in
                Button { play(station) } label: {
                    HStack(spacing: 12) {
                        Image(systemName: "dot.radiowaves.left.and.right").font(.title3).foregroundStyle(Color.accentColor)
                            .frame(width: 48, height: 48).background(.quaternary, in: .rect(cornerRadius: 8, style: .continuous))
                        VStack(alignment: .leading, spacing: 2) {
                            let isCurrent = model.player?.current?.id == Player.radioPrefix + station.id
                            Text(station.name).lineLimit(1).foregroundStyle(isCurrent ? Color.accentColor : .primary)
                            if let host = URL(string: station.streamUrl)?.host() { Text(host).font(.subheadline).foregroundStyle(.secondary).lineLimit(1) }
                        }
                        Spacer(minLength: 0)
                    }
                    .contentShape(.rect)
                }
                .buttonStyle(.plain)
                .swipeActions {
                    Button("Delete", systemImage: "trash", role: .destructive) { Task { await remove(station) } }
                    Button("Edit", systemImage: "pencil") { editing = station }.tint(.orange)
                }
                .contextMenu { Button("Edit", systemImage: "pencil") { editing = station } }
            }
        }
        .listStyle(.plain)
        .overlay {
            if !loaded { ProgressView() }
            else if stations.isEmpty {
                ContentUnavailableView {
                    Label("No Stations", systemImage: "dot.radiowaves.left.and.right")
                } description: { Text("Add an internet radio stream to listen live.") } actions: {
                    Button("Add Station") { adding = true }
                }
            }
        }
        .navigationTitle("Radio")
        .navigationBarTitleDisplayMode(.inline)
        .toolbar { ToolbarItem(placement: .primaryAction) { Button("Add Station", systemImage: "plus") { adding = true } } }
        .sheet(isPresented: $adding) { StationForm(station: nil) { await load() } }
        .sheet(item: $editing) { s in StationForm(station: s) { await load() } }
        .alert("Couldn't complete the action", isPresented: $failed) { Button("OK", role: .cancel) {} }
        .refreshable { await load() }
        .task(id: model.contentKey) { await load() }
    }

    private func load() async {
        stations = (try? await model.client?.radioStations()) ?? stations
        loaded = true
    }

    private func play(_ s: RadioStation) {
        guard let url = URL(string: s.streamUrl) else { return }
        model.player?.playRadio(id: s.id, name: s.name, streamURL: url)
    }

    private func remove(_ s: RadioStation) async {
        do { try await model.client?.deleteRadioStation(id: s.id); stations.removeAll { $0.id == s.id } } catch { failed = true }
    }
}

/// Добавление и правка станции.
private struct StationForm: View {
    let station: RadioStation?
    let onDone: () async -> Void
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss
    @State private var name = ""
    @State private var stream = ""
    @State private var home = ""
    @State private var failed = false

    var body: some View {
        NavigationStack {
            Form {
                TextField("Name", text: $name)
                TextField("Stream address", text: $stream).textInputAutocapitalization(.never).keyboardType(.URL).autocorrectionDisabled()
                TextField("Website (optional)", text: $home).textInputAutocapitalization(.never).keyboardType(.URL).autocorrectionDisabled()
                if failed { Text("Couldn't save the station").foregroundStyle(.red) }
            }
            .navigationTitle(station == nil ? "Add Station" : "Edit Station")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) { Button("Cancel") { dismiss() } }
                ToolbarItem(placement: .confirmationAction) {
                    Button("Save") { Task { await save() } }
                        .disabled(name.trimmingCharacters(in: .whitespaces).isEmpty || URL(string: stream)?.host() == nil)
                }
            }
            .onAppear { if let s = station { name = s.name; stream = s.streamUrl; home = s.homePageUrl ?? "" } }
        }
        .presentationDetents([.medium])
    }

    private func save() async {
        do {
            if let s = station { try await model.client?.updateRadioStation(id: s.id, name: name, streamUrl: stream, homePageUrl: home) }
            else { try await model.client?.createRadioStation(name: name, streamUrl: stream, homePageUrl: home) }
            await onDone(); dismiss()
        } catch { failed = true }
    }
}
