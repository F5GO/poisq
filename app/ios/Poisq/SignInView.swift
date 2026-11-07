import SwiftUI

struct SignInView: View {
    /// Показан поверх приложения («Add Server») — тогда есть кнопка закрытия.
    let isSheet: Bool
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss
    @State private var serverName = ""
    @State private var address = ""
    @State private var username = ""
    @State private var password = ""
    @State private var alternates: [String] = []
    @State private var headers: [HeaderRow] = []
    @State private var showAdvanced = false
    @State private var isWorking = false
    @State private var errorText: String?
    @FocusState private var focus: Field?
    private enum Field { case name, address, username, password }

    private var canSubmit: Bool { !address.isEmpty && !username.isEmpty && !password.isEmpty && !isWorking }

    var body: some View {
        ScrollView {
            VStack(spacing: 28) {
                header
                VStack(spacing: 0) {
                    field("Name", systemImage: "tag") {
                        TextField("Name (optional)", text: $serverName)
                            .focused($focus, equals: .name).submitLabel(.next)
                    }
                    Divider().padding(.leading, 52)
                    field("Server address", systemImage: "server.rack") {
                        TextField("Server address", text: $address, prompt: Text("music.example.com"))
                            .textContentType(.URL).keyboardType(.URL)
                            .focused($focus, equals: .address).submitLabel(.next)
                    }
                    Divider().padding(.leading, 52)
                    field("Username", systemImage: "person") {
                        TextField("Username", text: $username)
                            .textContentType(.username)
                            .focused($focus, equals: .username).submitLabel(.next)
                    }
                    Divider().padding(.leading, 52)
                    field("Password", systemImage: "key") {
                        SecureField("Password", text: $password)
                            .textContentType(.password)
                            .focused($focus, equals: .password).submitLabel(.go)
                    }
                }
                .textInputAutocapitalization(.never).autocorrectionDisabled()
                .background(.background.secondary, in: .rect(cornerRadius: 20))

                // Свои HTTP-заголовки и запасные адреса — для серверов за Cloudflare Access, Authelia, VPN.
                Button { showAdvanced = true } label: {
                    let n = headers.dictionary.count + alternates.filter { !$0.isEmpty }.count
                    Label(n == 0 ? String(localized: "Advanced") : String(localized: "Advanced (\(n))"), systemImage: "slider.horizontal.3")
                        .frame(maxWidth: .infinity, minHeight: 24)
                }
                .buttonStyle(.glass)
                .controlSize(.large)

                if let errorText {
                    Label(errorText, systemImage: "exclamationmark.triangle.fill")
                        .font(.footnote).foregroundStyle(.red)
                        .transition(.opacity.combined(with: .move(edge: .top)))
                }

                Button(action: submit) {
                    Group {
                        if isWorking { ProgressView() } else { Text("Sign In").fontWeight(.semibold) }
                    }
                    .frame(maxWidth: .infinity, minHeight: 28)
                }
                .buttonStyle(.glassProminent)
                .controlSize(.large)
                .disabled(!canSubmit)
            }
            .padding(.horizontal, 20)
            .padding(.top, 60)
        }
        .scrollDismissesKeyboard(.interactively)
        .sheet(isPresented: $showAdvanced) {
            NavigationStack {
                Form { ServerAdvancedFields(alternates: $alternates, headers: $headers) }
                    .navigationTitle("Advanced")
                    .navigationBarTitleDisplayMode(.inline)
                    .toolbar { ToolbarItem(placement: .confirmationAction) { Button("Done", systemImage: "checkmark") { showAdvanced = false } } }
                    .textInputAutocapitalization(.never).autocorrectionDisabled()
            }
            .presentationDetents([.large])
        }
        .background { PaletteBackdrop() }
        .overlay(alignment: .topTrailing) {
            if isSheet {
                Button("Close", systemImage: "xmark") { dismiss() }
                    .labelStyle(.iconOnly).buttonStyle(.glass).buttonBorderShape(.circle).padding()
            }
        }
        .onSubmit {
            switch focus {
            case .name: focus = .address
            case .address: focus = .username
            case .username: focus = .password
            default: submit()
            }
        }
        .animation(.smooth, value: errorText)
        .sensoryFeedback(.error, trigger: errorText) { _, new in new != nil }
        .onChange(of: address) { errorText = nil }
        .onChange(of: username) { errorText = nil }
        .onChange(of: password) { errorText = nil }
    }

    private var header: some View {
        VStack(spacing: 12) {
            Image(systemName: "music.note.house.fill")
                .font(.system(size: 56)).foregroundStyle(.tint).symbolRenderingMode(.hierarchical)
            Text("poisq").font(.largeTitle.bold())
        }
    }

    private func field<C: View>(_ title: LocalizedStringKey, systemImage: String, @ViewBuilder content: () -> C) -> some View {
        HStack(spacing: 12) {
            Image(systemName: systemImage).foregroundStyle(.secondary).frame(width: 28)
            content()
        }
        .padding(.horizontal, 16).frame(minHeight: 52)
        .accessibilityElement(children: .contain).accessibilityLabel(title)
    }

    private func submit() {
        guard canSubmit else { return }
        focus = nil
        isWorking = true
        Task {
            defer { isWorking = false }
            do { try await model.signIn(name: serverName, address: address, username: username, password: password,
                                       headers: headers.dictionary, alternates: alternates); if isSheet { dismiss() } }
            catch { errorText = error.localizedDescription }
        }
    }
}
