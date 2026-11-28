import SwiftUI
import PoisqCore

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

    // Защищённый режим: включается сканированием QR poisq, вход разрешён только по подтверждённому каналу.
    @State private var secure: SecureSetup?
    @State private var showScanner = false
    @State private var keyChangeCode: String?
    @State private var showKeyDetails = false
    private struct SecureSetup {
        let profileID: UUID                    // под этим ID канал хранит токен устройства в Keychain
        let pairing: SecureServerPairing
        let channel: SecureChannel
        let address: String
        var state: State = .checking
        enum State: Equatable { case checking, verified, failed(String) }
    }

    private var canSubmit: Bool {
        guard !username.isEmpty, !password.isEmpty, !isWorking else { return false }
        if let secure { return secure.state == .verified }
        return !address.isEmpty
    }

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

                if let secure { secureStatus(secure) }

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
                Form {
                    Section {
                        Button("Scan poisq QR", systemImage: "qrcode.viewfinder") {
                            showAdvanced = false
                            showScanner = true
                        }
                    } footer: {
                        Text("Encrypts everything between the app and poisq Studio. The QR code is shown in poisq Studio → Settings → Secure Transport.")
                    }
                    if secure == nil { ServerAdvancedFields(alternates: $alternates, headers: $headers) }
                }
                    .navigationTitle("Advanced")
                    .navigationBarTitleDisplayMode(.inline)
                    .toolbar { ToolbarItem(placement: .confirmationAction) { Button("Done", systemImage: "checkmark") { showAdvanced = false } } }
                    .textInputAutocapitalization(.never).autocorrectionDisabled()
            }
            .presentationDetents([.large])
        }
        .sheet(isPresented: $showScanner) { SecureQRScannerSheet(onCode: accept) }
        .confirmationDialog("The key of this server has changed", isPresented: Binding(get: { keyChangeCode != nil }, set: { if !$0 { keyChangeCode = nil } }),
                            titleVisibility: .visible) {
            Button("Use the New Key", role: .destructive) {
                if let code = keyChangeCode, let pairing = SecureServerPairing(code: code) { startSecure(pairing) }
                keyChangeCode = nil
            }
            Button("Cancel", role: .cancel) { keyChangeCode = nil }
        } message: {
            Text("The QR code has a different key than the saved server. This may mean someone is impersonating it. Continue only if you reset Secure Transport in poisq Studio yourself.")
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
        .onChange(of: address) {
            errorText = nil
            // Ручная правка адреса выводит форму из защищённого режима: новый адрес не закреплён QR.
            if let s = secure, address != s.address { secure = nil }
        }
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

    @ViewBuilder private func secureStatus(_ s: SecureSetup) -> some View {
        switch s.state {
        case .checking:
            HStack(spacing: 8) {
                ProgressView()
                Text("Checking the secure connection…").font(.footnote).foregroundStyle(.secondary)
            }
            .frame(maxWidth: .infinity)
        case .verified:
            VStack(spacing: 10) {
                Label("Connection is secure", systemImage: "lock.shield.fill")
                    .font(.subheadline.weight(.semibold))
                    .foregroundStyle(.green)
                Button { showKeyDetails = true } label: {
                    Text(s.pairing.emojiFingerprint.joined(separator: " "))
                        .font(.largeTitle)
                }
                .buttonStyle(.plain)
                .accessibilityLabel(Text("Key emoji"))
                .accessibilityHint(Text("Shows how to check the key"))
                .popover(isPresented: $showKeyDetails) {
                    VStack(alignment: .leading, spacing: 8) {
                        Text("Compare the emoji with poisq Studio. If they differ, don't sign in.")
                        Text("Key \(s.pairing.fingerprint)").font(.caption.monospaced()).foregroundStyle(.secondary)
                    }
                    .font(.footnote)
                    .padding()
                    .frame(idealWidth: 280)
                    .presentationCompactAdaptation(.popover)
                }
            }
            .frame(maxWidth: .infinity)
            .transition(.opacity.combined(with: .scale(scale: 0.96)))
        case .failed(let message):
            VStack(spacing: 8) {
                Label(message, systemImage: "lock.trianglebadge.exclamationmark.fill")
                    .font(.footnote)
                    .foregroundStyle(.red)
                    .multilineTextAlignment(.center)
                Button("Try Again") { verify() }.font(.footnote.weight(.semibold))
            }
            .frame(maxWidth: .infinity)
        }
    }

    /// Текст из сканера или буфера. Некорректный или чужой код оставляет форму без изменений.
    private func accept(_ code: String) {
        guard let pairing = SecureServerPairing(code: code) else {
            errorText = String(localized: "This is not a poisq QR code")
            return
        }
        if let known = model.secureProfile(forHost: pairing.origin.host()), known.secure?.pairing?.rootPublicKey != pairing.rootPublicKey {
            keyChangeCode = code                 // ключ известного сервера сменился — только после явного подтверждения
            return
        }
        startSecure(pairing)
    }

    private func startSecure(_ pairing: SecureServerPairing) {
        let id = UUID()
        let channel = SecureChannel(pairing: pairing, credentials: SecureCredentialKeychain(profileID: id))
        var host = pairing.origin.host() ?? ""
        if let port = pairing.origin.port { host += ":\(port)" }
        secure = SecureSetup(profileID: id, pairing: pairing, channel: channel, address: host)
        address = host
        errorText = nil
        verify()
    }

    /// Контрольный обмен без учётных данных: подтверждает ключ установки и канал именно этого профиля.
    private func verify() {
        guard let s = secure else { return }
        secure?.state = .checking
        Task {
            do {
                try await s.channel.verify()
                if secure?.channel === s.channel { secure?.state = .verified }
            } catch {
                let f = error as? SecureChannel.Failure
                if secure?.channel === s.channel {
                    secure?.state = .failed(f.map(SignInError.describe) ?? String(localized: "Couldn't connect to the server"))
                }
            }
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
        // Обработчик сам проверяет условия: отправка с клавиатуры идёт мимо disabled-кнопки.
        guard canSubmit else { return }
        focus = nil
        isWorking = true
        if let s = secure {
            Task {
                defer { isWorking = false }
                do {
                    try await model.signInSecure(profileID: s.profileID, name: serverName, channel: s.channel, username: username, password: password)
                    if isSheet { dismiss() }
                } catch SignInError.secure(.handshakeFailed) {
                    verify()                     // сессия истекла или потерялась — сначала снова подтверждаем канал
                } catch {
                    errorText = error.localizedDescription
                }
            }
            return
        }
        Task {
            defer { isWorking = false }
            do { try await model.signIn(name: serverName, address: address, username: username, password: password,
                                       headers: headers.dictionary, alternates: alternates); if isSheet { dismiss() } }
            catch { errorText = error.localizedDescription }
        }
    }
}
