import Foundation
import Testing
@testable import PoisqCore

@Suite struct SecureWorkingCertificateTests {
    private func fixture() throws -> (SecurePairingVector, SecureServerPairing, Data) {
        let vector = try SecurePairingVector.load()
        let pairing = try #require(SecureServerPairing(code: vector.pairing.code))
        return (vector, pairing, Data(hex: vector.certificate.hex))
    }

    @Test func readsGoCertificate() throws {
        let (vector, pairing, raw) = try fixture()
        let cert = try #require(SecureWorkingCertificate(data: raw, pairing: pairing))
        #expect(cert.epoch == vector.certificate.epoch)
        #expect(cert.notBefore == Date(timeIntervalSince1970: TimeInterval(vector.certificate.notBefore)))
        #expect(cert.notAfter == Date(timeIntervalSince1970: TimeInterval(vector.certificate.notAfter)))
        #expect(cert.workingKey == Data(hex: vector.certificate.workingKey))
        #expect(cert.nextRootDigest == Data(hex: vector.certificate.nextRootDigest))
        #expect(cert.rawValue == raw)
    }

    @Test func rejectsTamperedCertificates() throws {
        let (_, pairing, raw) = try fixture()
        for index in raw.indices {
            var bad = raw
            bad[index] ^= 0x01
            #expect(SecureWorkingCertificate(data: bad, pairing: pairing) == nil, "Accepted byte \(index) flipped")
        }
        #expect(SecureWorkingCertificate(data: raw.dropLast(), pairing: pairing) == nil)
        #expect(SecureWorkingCertificate(data: raw + Data([0]), pairing: pairing) == nil)
    }

    @Test func rejectsAnotherInstallationOrRoot() throws {
        let (vector, _, raw) = try fixture()
        var fields = vector.pairing.code.split(separator: ":").map(String.init)
        fields[2] = "ffeeddccbbaa99887766554433221100"
        let otherInstallation = try #require(SecureServerPairing(code: fields.joined(separator: ":")))
        #expect(SecureWorkingCertificate(data: raw, pairing: otherInstallation) == nil)

        fields = vector.pairing.code.split(separator: ":").map(String.init)
        fields[3] = "PUAXw-hDiVqStwqnTRt-vJyYLM8uxJaMwM1V8Sr0Zgw" // RFC 8032 test 2 public key
        let otherRoot = try #require(SecureServerPairing(code: fields.joined(separator: ":")))
        #expect(SecureWorkingCertificate(data: raw, pairing: otherRoot) == nil)
    }

    @Test func checksDatesAndEpochRollback() throws {
        let (_, pairing, raw) = try fixture()
        let cert = try #require(SecureWorkingCertificate(data: raw, pairing: pairing))
        let skew = SecureWorkingCertificate.clockSkew
        #expect(!cert.isValid(at: cert.notBefore.addingTimeInterval(-skew - 1)))
        #expect(cert.isValid(at: cert.notBefore.addingTimeInterval(-skew)))
        #expect(cert.isValid(at: cert.notAfter.addingTimeInterval(skew - 1)))
        #expect(!cert.isValid(at: cert.notAfter.addingTimeInterval(skew)))

        let now = cert.notBefore.addingTimeInterval(3600)
        #expect(cert.isAcceptable(afterAcceptedEpoch: nil, at: now))
        #expect(cert.isAcceptable(afterAcceptedEpoch: cert.epoch, at: now))
        #expect(!cert.isAcceptable(afterAcceptedEpoch: cert.epoch + 1, at: now))
    }
}
