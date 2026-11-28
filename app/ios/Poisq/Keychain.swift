import Foundation
import PoisqCore
import Security

/// Пароль хранится только в Keychain. Адрес и логин — в UserDefaults.
enum Keychain {
    private static let service = "app.poisq.ios.password"

    static func save(_ password: String, account: String) {
        let base: [String: Any] = [kSecClass as String: kSecClassGenericPassword,
                                   kSecAttrService as String: service,
                                   kSecAttrAccount as String: account]
        SecItemDelete(base as CFDictionary)
        var item = base
        item[kSecValueData as String] = Data(password.utf8)
        item[kSecAttrAccessible as String] = kSecAttrAccessibleAfterFirstUnlock // нужен для фонового воспроизведения
        SecItemAdd(item as CFDictionary, nil)
    }

    static func load(account: String) -> String? {
        let q: [String: Any] = [kSecClass as String: kSecClassGenericPassword,
                                kSecAttrService as String: service,
                                kSecAttrAccount as String: account,
                                kSecReturnData as String: true,
                                kSecMatchLimit as String: kSecMatchLimitOne]
        var out: CFTypeRef?
        guard SecItemCopyMatching(q as CFDictionary, &out) == errSecSuccess, let d = out as? Data else { return nil }
        return String(data: d, encoding: .utf8)
    }

    static func delete(account: String) {
        SecItemDelete([kSecClass as String: kSecClassGenericPassword,
                       kSecAttrService as String: service,
                       kSecAttrAccount as String: account] as CFDictionary)
    }
}

/// Токен устройства защищённого профиля. Доступен после первой разблокировки (фоновое воспроизведение)
/// и только на этом устройстве: в резервные копии и на другие устройства не переносится.
final class SecureCredentialKeychain: SecureCredentialStore, @unchecked Sendable {
    private static let service = "app.poisq.ios.secure-device"
    private let account: String

    init(profileID: UUID) { account = profileID.uuidString }

    private var base: [String: Any] {
        [kSecClass as String: kSecClassGenericPassword,
         kSecAttrService as String: Self.service,
         kSecAttrAccount as String: account]
    }

    func loadCredential() -> SecureDeviceCredential? {
        var q = base
        q[kSecReturnData as String] = true
        q[kSecMatchLimit as String] = kSecMatchLimitOne
        var out: CFTypeRef?
        guard SecItemCopyMatching(q as CFDictionary, &out) == errSecSuccess, let data = out as? Data else { return nil }
        return try? JSONDecoder().decode(SecureDeviceCredential.self, from: data)
    }

    func saveCredential(_ credential: SecureDeviceCredential) throws {
        let data = try JSONEncoder().encode(credential)
        let update = SecItemUpdate(base as CFDictionary, [kSecValueData as String: data] as CFDictionary)
        if update == errSecSuccess { return }
        guard update == errSecItemNotFound else { throw SecureChannel.Failure.serverUnavailable }
        var item = base
        item[kSecValueData as String] = data
        item[kSecAttrAccessible as String] = kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly
        guard SecItemAdd(item as CFDictionary, nil) == errSecSuccess else { throw SecureChannel.Failure.serverUnavailable }
    }

    func deleteCredential() {
        SecItemDelete(base as CFDictionary)
    }
}
