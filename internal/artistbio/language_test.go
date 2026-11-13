package artistbio

import (
	"strings"
	"testing"
)

func TestDetectBiographyLanguage(t *testing.T) {
	tests := []struct {
		name       string
		text       string
		language   string
		quality    bool
		requiresAI bool
	}{
		{
			name:     "russian",
			text:     "Российская рок-группа была основана в Ленинграде. Музыканты записали несколько известных альбомов и выступали на крупнейших фестивалях страны.",
			language: "ru", quality: true,
		},
		{
			name:     "ukrainian",
			text:     "Український музичний гурт був заснований у Києві. Його учасники створили кілька відомих альбомів і виступали на великих фестивалях України.",
			language: "uk", quality: true,
		},
		{
			name:     "belarusian",
			text:     "Беларускі музычны гурт быў заснаваны ў Мінску. Яго ўдзельнікі запісалі некалькі вядомых альбомаў і выступалі на фестывалях Беларусі.",
			language: "be", quality: true,
		},
		{
			name:     "english",
			text:     "The band was formed in London and released several studio albums. Its members later performed at major festivals across Europe and recorded new material.",
			language: "en", quality: true,
		},
		{
			name:     "mixed",
			text:     "Российская группа выпустила новый studio album and later performed at several European festivals вместе с другими известными музыкантами.",
			language: "mixed", quality: true, requiresAI: true,
		},
		{
			name:     "short",
			text:     "Русская группа.",
			language: "und", quality: false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := DetectBiographyLanguage(test.text)
			if got.Language != test.language || got.QualityOK != test.quality || got.RequiresAI != test.requiresAI {
				t.Fatalf("unexpected analysis: %#v", got)
			}
			if got.Confidence < 0 || got.Confidence > 1 || got.RussianShare < 0 || got.RussianShare > 1 {
				t.Fatalf("unbounded analysis: %#v", got)
			}
		})
	}
}

func TestBiographyTextQuality(t *testing.T) {
	tests := map[string]string{
		"oversized":     strings.Repeat("я", MaxBiographyBytes+1),
		"unsafe markup": `<script>ignore rules</script>` + strings.Repeat(" Русский музыкальный коллектив", 10),
		"repeated":      strings.Repeat("музыкант музыкант музыкант музыкант ", 30),
		"link farm":     strings.Repeat("https://example.com/a ", 12) + strings.Repeat("описание ", 20),
	}
	for name, text := range tests {
		t.Run(name, func(t *testing.T) {
			got := DetectBiographyLanguage(text)
			if got.QualityOK || got.RequiresAI {
				t.Fatalf("unsafe text must fail before AI: %#v", got)
			}
		})
	}
}

func TestDetectBiographyLanguageIgnoresNonLanguageEvidence(t *testing.T) {
	text := "Российский музыкальный коллектив сформировался в Москве и записал несколько альбомов. " +
		"Подробнее: https://example.com/artist/2024, mail@example.com, 1999–2025."
	got := DetectBiographyLanguage(text)
	if got.Language != "ru" || !got.QualityOK || got.RussianShare < 0.8 {
		t.Fatalf("unexpected analysis: %#v", got)
	}
}
