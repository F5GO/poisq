import SwiftUI
import PoisqCore

struct SettingsView: View {
    @Environment(AppModel.self) private var model
    @State private var addingServer = false
    @State private var editingServer: ServerProfile?
    @State private var renamingFolder: MusicFolder?
    @State private var removingServer: ServerProfile?
    @State private var newName = ""
    @State private var confirmRemoveAll = false

    var body: some View {
        NavigationStack {
            Form {
                serversSection
                if let s = model.activeServer {
                    Section("Current Server") {
                        LabeledContent("Address", value: s.url.host() ?? "—")
                        LabeledContent("User", value: s.username)
                        LabeledContent("Mode", value: model.capabilities.hasPoisq ? "poisq" : "Subsonic")
                    }
                }
                playbackSection
                offlineSection
                downloadsSection
                cacheSection
                if model.libraries.count > 1 { librariesSection }
            }
            .navigationTitle("Settings")
            .sheet(isPresented: $addingServer) { SignInView(isSheet: true) }
            .sheet(item: $editingServer) { ServerEditView(server: $0) }
            .alert("Rename Library", isPresented: presented($renamingFolder)) {
                TextField("Name", text: $newName)
                Button("Save") { if let f = renamingFolder { model.rename(f, to: newName) } }
                Button("Cancel", role: .cancel) {}
            }
            .confirmationDialog("Remove this server?", isPresented: presented($removingServer), titleVisibility: .visible, presenting: removingServer) { s in
                Button("Remove “\(s.name)”", role: .destructive) { model.removeServer(s.id) }
            } message: { _ in
                Text("The saved password will be deleted from this device.")
            }
        }
    }

    private var serversSection: some View {
        Section("Servers") {
            ForEach(model.servers) { server in
                HStack {
                    Button { model.switchServer(to: server.id) } label: {
                        HStack {
                            VStack(alignment: .leading) {
                                Text(server.name).foregroundStyle(.primary)
                                Text(server.url.host() ?? "").font(.caption).foregroundStyle(.secondary)
                            }
                            Spacer()
                            if server.id == model.activeID { Image(systemName: "checkmark").foregroundStyle(.tint) }
                        }
                        .contentShape(.rect)
                    }
                    .buttonStyle(.plain)
                    Button("Edit", systemImage: "info.circle") { editingServer = server }
                        .labelStyle(.iconOnly).buttonStyle(.borderless)
                }
                .swipeActions(edge: .trailing) {
                    Button("Remove", role: .destructive) { removingServer = server }
                    Button("Edit") { editingServer = server }.tint(.orange)
                }
            }
            Button { addingServer = true } label: {
                Label("Add Server…", systemImage: "plus")
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .contentShape(.rect)       // нажимается вся строка, а не только значок
            }
        }
    }

    /// Качество стрима и кеша: кеш подкачивает треки в том же качестве.
    private var playbackSection: some View {
        Section {
            Picker("Quality", selection: Binding(get: { model.playbackQuality }, set: { model.playbackQuality = $0 })) {
                ForEach(StreamQuality.allCases) { Text($0.title).tag($0) }
            }
            NavigationLink { EqualizerView() } label: {
                LabeledContent("Equalizer") { if model.equalizer.isEnabled { Text(LocalizedStringKey(model.equalizer.activeName)) } else { Text("Off") } }
            }
            Picker("Sound Check", selection: Binding(get: { model.replayGainMode }, set: { model.replayGainMode = $0 })) {
                Text("Off").tag(0)
                Text("By Track").tag(1)
                Text("By Album").tag(2)
            }
            Toggle("Report Listening", isOn: Binding(get: { model.scrobbleEnabled }, set: { model.scrobbleEnabled = $0 }))
            Toggle("Continue on Other Devices", isOn: Binding(get: { model.syncQueueEnabled }, set: { model.syncQueueEnabled = $0 }))
        } header: { Text("Playback") } footer: {
            Text("Lower quality uses less data. The cache saves songs in this quality too. Listening is reported to the server (history, play counts) and the queue is saved there so you can continue on another device. Sound Check evens out loudness using ReplayGain data from the server, if it has any.")
        }
    }

    private var offlineSection: some View {
        @Bindable var m = model
        return Section {
            Toggle("Switch to Downloads Automatically", isOn: $m.autoOffline)
        } header: { Text("Offline") } footer: {
            Text("Without a connection to the server the app shows and plays only downloaded music, and returns online by itself.")
        }
    }

    private var cacheSection: some View {
        @Bindable var m = model
        return Section {
            LabeledContent("Size", value: ByteCountFormatter.string(fromByteCount: model.cache.sizeBytes, countStyle: .file))
            Picker("Limit", selection: Binding(get: { model.cache.limitMB }, set: { model.cache.limitMB = $0 })) {
                ForEach([512, 1024, 2048, 5120], id: \.self) { Text(ByteCountFormatter.string(fromByteCount: Int64($0) * 1_048_576, countStyle: .binary)).tag($0) }
            }
            Toggle("Cache on Cellular", isOn: $m.cacheOnCellular)
            if model.cache.sizeBytes > 0 { Button("Clear Cache", role: .destructive) { model.cache.clear() } }
        } header: { Text("Cache") } footer: {
            Text("The current and next songs are saved while you listen, so replays and weak signal don't interrupt playback.")
        }
    }

    private var downloadsSection: some View {
        Section {
            NavigationLink { DownloadedView() } label: {
                LabeledContent("Downloaded", value: "\(model.downloadedSongs.count)")
            }
            LabeledContent("Storage", value: ByteCountFormatter.string(fromByteCount: model.downloads.totalBytes(), countStyle: .file))
            Picker("Limit", selection: Binding(get: { model.downloadLimitGB }, set: { model.downloadLimitGB = $0 })) {
                Text("No Limit").tag(0)
                ForEach([1, 2, 5, 10, 20, 50], id: \.self) { Text(ByteCountFormatter.string(fromByteCount: Int64($0) * 1_073_741_824, countStyle: .binary)).tag($0) }
            }
            if model.downloadLimitGB > 0 {
                ProgressView(value: min(Double(model.downloads.totalBytes()) / (Double(model.downloadLimitGB) * 1_073_741_824), 1))
                    .tint(model.downloads.totalBytes() >= Int64(model.downloadLimitGB) * 1_073_741_824 ? .red : .accentColor)
            }
            Picker("Quality", selection: Binding(get: { model.downloadQuality }, set: { model.downloadQuality = $0 })) {
                ForEach(StreamQuality.allCases) { Text($0.title).tag($0) }
            }
            Toggle("Download on Cellular", isOn: Binding(get: { model.downloadOnCellular }, set: { model.downloadOnCellular = $0 }))
            if model.downloads.totalBytes() > 0 {
                Button("Remove All Downloads", role: .destructive) { confirmRemoveAll = true }
                    .confirmationDialog("Remove all downloads?", isPresented: $confirmRemoveAll, titleVisibility: .visible) {
                        Button("Remove All Downloads", role: .destructive) { model.downloads.removeAll() }
                    } message: { Text("Downloaded songs will be deleted from this device.") }
            }
        } header: { Text("Downloads") } footer: {
            Text("Quality applies to new downloads; songs already downloaded keep theirs.")
        }
    }

    private var librariesSection: some View {
        Section {
            LibraryPicker()
            ForEach(model.libraries) { folder in
                Button("Rename “\(model.name(of: folder))”") {
                    newName = model.name(of: folder); renamingFolder = folder
                }
            }
        } header: {
            Text("Libraries")
        } footer: {
            Text("Names changed here apply only in this app; the server keeps its own.")
        }
    }

    /// Алерты и диалоги показываются, пока значение не nil.
    private func presented<T>(_ item: Binding<T?>) -> Binding<Bool> {
        Binding(get: { item.wrappedValue != nil }, set: { if !$0 { item.wrappedValue = nil } })
    }
}

extension StreamQuality {
    /// Название для выбора в настройках.
    var title: LocalizedStringKey {
        switch self {
        case .original: "Original"
        case .kbps320: "High · 320 kbps"
        case .kbps192: "Standard · 192 kbps"
        case .kbps128: "Low · 128 kbps"
        }
    }
}
