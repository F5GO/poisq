import Testing
@testable import PoisqCore

@Suite struct KeyboardLayoutTests {
    @Test func latinTypedInsteadOfRussian() { #expect(KeyboardLayout.swapped("rbyj") == "кино") }
    @Test func russianTypedInsteadOfLatin() { #expect(KeyboardLayout.swapped("Тшкмфтф") == "Nirvana") }
    @Test func mixedIsLeftAlone() { #expect(KeyboardLayout.swapped("Кино rock") == nil) }
    @Test func digitsOnlyIsLeftAlone() { #expect(KeyboardLayout.swapped("1984") == nil) }
}
