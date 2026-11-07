import SwiftUI
import PoisqCore

/// Где играть: этот iPhone или колонка Music Assistant (трансляция через прокси poisq).
/// Как окно выбора устройства в «Музыке»: список устройств с галочкой, у выбранной колонки — громкость.
struct CastPickerView: View {
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss
    @State private var volume: Double = 0
    @State private var loading = true
    @State private var showAirPlay = false

    var body: some View {
        NavigationStack {
            List {
                Section {
                    row(Text("This iPhone"), systemImage: "iphone", selected: model.castZone == nil) { model.cast(to: nil) }
                }
                Section {
                    ForEach(model.castZones) { zone in
                        row(Text(verbatim: zone.name), systemImage: "hifispeaker", selected: model.castZone?.id == zone.id) { model.cast(to: zone) }
                        if model.castZone?.id == zone.id, zone.volume >= 0 {
                            Slider(value: $volume, in: 0...100) { Text("Volume") } minimumValueLabel: {
                                Image(systemName: "speaker.fill")
                            } maximumValueLabel: {
                                Image(systemName: "speaker.wave.3.fill")
                            } onEditingChanged: { editing in
                                if !editing { model.setCastVolume(Int(volume)) }
                            }
                            .foregroundStyle(.secondary)
                        }
                    }
                } header: {
                    Text("Speakers")
                } footer: {
                    if !loading, model.castZones.isEmpty { Text("No speakers available. Speakers are set up in poisq Studio.") }
                }
                // AirPlay и Bluetooth — запасной путь, поэтому свёрнут. Звук по AirPlay идёт с iPhone, так что трансляцию на колонку сначала выключаем.
                Section {
                    DisclosureGroup(isExpanded: $showAirPlay) {
                        HStack {
                            Text("AirPlay or Bluetooth device").foregroundStyle(.secondary)
                            Spacer()
                            RoutePicker { model.cast(to: nil) }.frame(width: 44, height: 44).accessibilityLabel("AirPlay")
                        }
                    } label: {
                        Label("AirPlay & Bluetooth", systemImage: "airplay.audio").foregroundStyle(.primary)
                    }
                    .tint(.primary)
                }
            }
            .navigationTitle("Play On")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar { ToolbarItem(placement: .confirmationAction) { Button("Done", systemImage: "checkmark") { dismiss() } } }
            .overlay { if loading, model.castZones.isEmpty { ProgressView() } }
        }
        .presentationDetents([.medium, .large])
        .task {
            await model.loadCastZones()
            loading = false
        }
        .onChange(of: model.castZone?.id, initial: true) { volume = Double(max(model.castZone?.volume ?? 0, 0)) }
    }

    private func row(_ title: Text, systemImage: String, selected: Bool, action: @escaping () -> Void) -> some View {
        Button(action: action) {
            HStack {
                Label { title } icon: { Image(systemName: systemImage) }.foregroundStyle(.primary)
                Spacer()
                if selected { Image(systemName: "checkmark").foregroundStyle(Color.accentColor).fontWeight(.semibold) }
            }
        }
        .tint(.primary)
        .accessibilityAddTraits(selected ? .isSelected : [])
    }
}
