import SwiftUI

/// Строка своего HTTP-заголовка в форме.
struct HeaderRow: Identifiable, Hashable {
    let id = UUID()
    var name = ""
    var value = ""
}

extension Array where Element == HeaderRow {
    /// Заполненные строки как словарь заголовков.
    var dictionary: [String: String] {
        reduce(into: [:]) { out, row in
            let n = row.name.trimmingCharacters(in: .whitespaces)
            if !n.isEmpty { out[n] = row.value.trimmingCharacters(in: .whitespaces) }
        }
    }
    init(_ headers: [String: String]) { self = headers.sorted { $0.key < $1.key }.map { HeaderRow(name: $0.key, value: $0.value) } }
}

/// Запасные адреса сервера и свои HTTP-заголовки. Общие поля для входа и для редактирования сервера.
struct ServerAdvancedFields: View {
    @Binding var alternates: [String]
    @Binding var headers: [HeaderRow]

    var body: some View {
        Section {
            ForEach($alternates.indices, id: \.self) { i in
                TextField("Alternate address", text: $alternates[i], prompt: Text("192.168.1.10:4533"))
                    .textContentType(.URL).keyboardType(.URL)
            }
            .onDelete { alternates.remove(atOffsets: $0) }
            Button("Add Address", systemImage: "plus") { alternates.append("") }
        } header: {
            Text("Alternate Addresses")
        } footer: {
            Text("The same server on another address, for example at home or over VPN. They are tried in order when the main address doesn't respond.")
        }
        Section {
            ForEach($headers) { $row in
                VStack(alignment: .leading, spacing: 6) {
                    TextField("Header name", text: $row.name, prompt: Text("CF-Access-Client-Id"))
                    SecureField("Value", text: $row.value)
                }
            }
            .onDelete { headers.remove(atOffsets: $0) }
            Button("Add Header", systemImage: "plus") { headers.append(HeaderRow()) }
        } header: {
            Text("HTTP Headers")
        } footer: {
            Text("Sent with every request to this server, for Cloudflare Access, Authelia and similar. Values are stored in the Keychain.")
        }
    }
}
