import SwiftUI

/// Изменение сохранённого сервера: имя, адрес, логин, пароль.
struct ServerEditView: View {
    let server: ServerProfile
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss
    @State private var name: String
    @State private var address: String
    @State private var username: String
    @State private var password = ""
    @State private var alternates: [String]
    @State private var headers: [HeaderRow]
    @State private var isWorking = false
    @State private var errorText: String?

    init(server: ServerProfile) {
        self.server = server
        _name = State(initialValue: server.name)
        _address = State(initialValue: server.url.absoluteString)
        _username = State(initialValue: server.username)
        _alternates = State(initialValue: server.alternateURLs.map(\.absoluteString))
        _headers = State(initialValue: [HeaderRow](ServerHeaders.load(server.id)))
    }

    var body: some View {
        NavigationStack {
            Form {
                Section("Name") {
                    TextField("Name", text: $name)
                }
                Section {
                    TextField("Server address", text: $address)
                        .textContentType(.URL).keyboardType(.URL)
                    TextField("Username", text: $username).textContentType(.username)
                    SecureField("New password", text: $password).textContentType(.password)
                } header: {
                    Text("Connection")
                } footer: {
                    Text("Leave the password empty to keep the current one. Changes to the connection are checked before saving.")
                }
                ServerAdvancedFields(alternates: $alternates, headers: $headers)
                if let errorText {
                    Section { Label(errorText, systemImage: "exclamationmark.triangle.fill").foregroundStyle(.red) }
                }
            }
            .textInputAutocapitalization(.never).autocorrectionDisabled()
            .navigationTitle("Edit Server")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) { Button("Cancel") { dismiss() } }
                ToolbarItem(placement: .confirmationAction) {
                    if isWorking { ProgressView() } else { Button("Save", action: save).disabled(name.isEmpty || address.isEmpty || username.isEmpty) }
                }
            }
            .onChange(of: address) { errorText = nil }
            .onChange(of: username) { errorText = nil }
            .onChange(of: password) { errorText = nil }
            .interactiveDismissDisabled(isWorking)
        }
    }

    private func save() {
        isWorking = true
        Task {
            defer { isWorking = false }
            do {
                try await model.updateServer(server.id, name: name, address: address, username: username, password: password,
                                             alternates: alternates, headers: headers.dictionary)
                dismiss()
            } catch { errorText = error.localizedDescription }
        }
    }
}
