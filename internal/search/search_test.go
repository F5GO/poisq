// Package search реализует интеллектуальный поиск, смену раскладки,
// транслитерацию и нечеткое сопоставление поисковых запросов для Navidrome.
//
// Данный файл search_test.go содержит комплексные юнит-тесты:
// - смену ошибочной раскладки клавиатуры (Тшкмфтф -> Nirvana, rbyj -> кино)
// - транслитерацию (офспринг -> The Offspring, splean -> сплин)
// - опечатки в комбинации со сменой раскладки (щеаызкштп -> The Offspring)
// - префиксный двух- и трехбуквенный поиск (оф -> The Offspring, мет -> Metallica, спл -> Сплин)
// - поиск с опечатками по префиксу (короы -> Король и Шут, офспринк -> The Offspring)
package search

import (
	"strings"
	"testing"
)

func setupTestDictionary() *Dictionary {
	dict := NewDictionary()
	items := []DictItem{
		{Text: "Nirvana", EntityType: "artist", PlayCount: 150},
		{Text: "The Offspring", EntityType: "artist", PlayCount: 200},
		{Text: "Кино", EntityType: "artist", PlayCount: 300},
		{Text: "Группа Крови", EntityType: "album", PlayCount: 250},
		{Text: "Сплин", EntityType: "artist", PlayCount: 180},
		{Text: "Metallica", EntityType: "artist", PlayCount: 400},
		{Text: "Smells Like Teen Spirit", EntityType: "track", PlayCount: 500},
		{Text: "Jane Air", EntityType: "artist", PlayCount: 50},
		{Text: "Король и Шут", EntityType: "artist", PlayCount: 500},
		{Text: "Кукла колдуна", EntityType: "track", PlayCount: 350},
		{Text: "Rammstein", EntityType: "artist", PlayCount: 450},
		{Text: "Queen", EntityType: "artist", PlayCount: 400},
		{Text: "Green Day", EntityType: "artist", PlayCount: 450},
		{Text: "Scoop", EntityType: "track", PlayCount: 200},
		{Text: "Depeche Mode", EntityType: "artist", PlayCount: 300},
		{Text: "Linkin Park", EntityType: "artist", PlayCount: 450},
	}
	dict.Rebuild(items)
	return dict
}

func TestLayoutSwap(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "Russian to English layout",
			input:    "Тшкмфтф",
			expected: "Nirvana",
		},
		{
			name:     "English to Russian layout",
			input:    "rbyj",
			expected: "кино",
		},
		{
			name:     "Offspring in Russian layout",
			input:    "щааы",
			expected: "offs",
		},
		{
			name:     "Complex typo layout",
			input:    "щеаызкштп",
			expected: "otfspring",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SwapLayout(tt.input)
			if !strings.EqualFold(got, tt.expected) {
				t.Errorf("SwapLayout(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestTransliteration(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
		isRu2En  bool
	}{
		{
			name:     "Music alias офспринг to The Offspring",
			input:    "офспринг",
			expected: "the offspring",
			isRu2En:  true,
		},
		{
			name:     "Cyrillic кино to kino",
			input:    "кино",
			expected: "kino",
			isRu2En:  true,
		},
		{
			name:     "Latin splean to сплин",
			input:    "splean",
			expected: "сплин",
			isRu2En:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got string
			if tt.isRu2En {
				got = TransliterateRuToEn(tt.input)
			} else {
				got = TransliterateEnToRu(tt.input)
			}
			if !strings.EqualFold(got, tt.expected) {
				t.Errorf("Transliterate(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestEngineTypoAndLayout(t *testing.T) {
	dict := setupTestDictionary()
	engine := NewEngine(dict)

	tests := []struct {
		name     string
		query    string
		expected string
	}{
		{
			name:     "Wrong layout Тшкмфтф -> Nirvana",
			query:    "Тшкмфтф",
			expected: "Nirvana",
		},
		{
			name:     "Wrong layout rbyj -> Кино",
			query:    "rbyj",
			expected: "Кино",
		},
		{
			name:     "Russian translit офспринг -> The Offspring",
			query:    "офспринг",
			expected: "The Offspring",
		},
		{
			name:     "Typo in translit офсприг -> The Offspring",
			query:    "офсприг",
			expected: "The Offspring",
		},
		{
			name:     "Typo and wrong layout combined щеаызкштп -> The Offspring",
			query:    "щеаызкштп",
			expected: "The Offspring",
		},
		{
			name:     "Translit Latin splean -> Сплин",
			query:    "splean",
			expected: "Сплин",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := engine.ProcessQuery(tt.query)
			if !strings.EqualFold(res.RewrittenQuery, tt.expected) {
				t.Errorf("ProcessQuery(%q) = %q, want %q (matches: %+v, variants: %+v)",
					tt.query, res.RewrittenQuery, tt.expected, res.Matches, res.Variants)
			}
		})
	}
}

func TestUniversalSearchForArbitraryEntities(t *testing.T) {
	dict := NewDictionary()
	// Сущности, которых НЕТ ни в каких таблицах алиасов:
	items := []DictItem{
		{Text: "Three Days Grace", EntityType: "artist", PlayCount: 100},
		{Text: "Bad Omens", EntityType: "artist", PlayCount: 150},
		{Text: "Loop", EntityType: "track", PlayCount: 50},
		{Text: "Boom Boom Pow", EntityType: "track", PlayCount: 80},
		{Text: "Sweet Dreams", EntityType: "track", PlayCount: 120},
		{Text: "Lost Frequencies", EntityType: "artist", PlayCount: 90},
	}
	dict.Rebuild(items)
	engine := NewEngine(dict)

	tests := []struct {
		name     string
		query    string
		expected string
	}{
		{
			name:     "Arbitrary track Loop as луп (sound u <-> oo)",
			query:    "луп",
			expected: "Loop",
		},
		{
			name:     "Arbitrary track Boom as бум (sound u <-> oo)",
			query:    "бум",
			expected: "Boom Boom Pow",
		},
		{
			name:     "Arbitrary artist Three Days Grace as грейс (word matching parent entity)",
			query:    "грейс",
			expected: "Three Days Grace",
		},
		{
			name:     "Arbitrary artist Bad Omens as бэд (word matching parent entity)",
			query:    "бэд",
			expected: "Bad Omens",
		},
		{
			name:     "Arbitrary track Sweet Dreams as свит (sound ee <-> и)",
			query:    "свит",
			expected: "Sweet Dreams",
		},
		{
			name:     "Arbitrary artist Lost Frequencies as лост",
			query:    "лост",
			expected: "Lost Frequencies",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := engine.ProcessQuery(tt.query)
			if !strings.EqualFold(res.RewrittenQuery, tt.expected) {
				t.Errorf("ProcessQuery(%q) = %q, want %q (matches: %+v, variants: %+v)",
					tt.query, res.RewrittenQuery, tt.expected, res.Matches, res.Variants)
			}
		})
	}
}

func TestGreenDayAndScoop(t *testing.T) {
	dict := setupTestDictionary()
	engine := NewEngine(dict)

	tests := []struct {
		name     string
		query    string
		expected string
	}{
		{
			name:     "Scoop as скуп",
			query:    "скуп",
			expected: "Scoop",
		},
		{
			name:     "Scoop as скууп",
			query:    "скууп",
			expected: "Scoop",
		},
		{
			name:     "Green Day as грин",
			query:    "грин",
			expected: "Green Day",
		},
		{
			name:     "Green Day as грин дэй",
			query:    "грин дэй",
			expected: "Green Day",
		},
		{
			name:     "Green Day as грин дей",
			query:    "грин дей",
			expected: "Green Day",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := engine.ProcessQuery(tt.query)
			if !strings.EqualFold(res.RewrittenQuery, tt.expected) {
				t.Errorf("ProcessQuery(%q) = %q, want %q (matches: %+v, variants: %+v)",
					tt.query, res.RewrittenQuery, tt.expected, res.Matches, res.Variants)
			}
		})
	}
}

func TestKorolAndShutTypo(t *testing.T) {
	dict := setupTestDictionary()
	engine := NewEngine(dict)

	// Пользовательский запрос: "короы" (5 рун, опечатка 'ы' вместо 'л')
	// Должен найти "Король и Шут"
	res := engine.ProcessQuery("короы")
	if !strings.EqualFold(res.RewrittenQuery, "Король и Шут") {
		t.Errorf("ProcessQuery('короы') = %q, want 'Король и Шут' (matches: %+v, variants: %+v)",
			res.RewrittenQuery, res.Matches, res.Variants)
	}
	if !res.NeedsFanout {
		t.Errorf("Expected NeedsFanout=true for 'короы'")
	}
}

func TestCyrillicPrefixForLatinArtists(t *testing.T) {
	dict := setupTestDictionary()
	engine := NewEngine(dict)

	tests := []struct {
		name     string
		query    string
		expected string
	}{
		{
			name:     "2 letters Cyrillic prefix оф -> The Offspring",
			query:    "оф",
			expected: "The Offspring",
		},
		{
			name:     "3 letters Cyrillic prefix офф -> The Offspring",
			query:    "офф",
			expected: "The Offspring",
		},
		{
			name:     "3 letters Cyrillic prefix офс -> The Offspring",
			query:    "офс",
			expected: "The Offspring",
		},
		{
			name:     "3 letters Cyrillic prefix мет -> Metallica",
			query:    "мет",
			expected: "Metallica",
		},
		{
			name:     "3 letters Cyrillic prefix спл -> Сплин",
			query:    "спл",
			expected: "Сплин",
		},
		{
			name:     "3 letters Cyrillic prefix рам -> Rammstein",
			query:    "рам",
			expected: "Rammstein",
		},
		{
			name:     "Cyrillic квин -> Queen",
			query:    "квин",
			expected: "Queen",
		},
		{
			name:     "Cyrillic prefix кви -> Queen",
			query:    "кви",
			expected: "Queen",
		},
		{
			name:     "Cyrillic prefix депеш -> Depeche Mode",
			query:    "депеш",
			expected: "Depeche Mode",
		},
		{
			name:     "Cyrillic prefix линкин -> Linkin Park",
			query:    "линкин",
			expected: "Linkin Park",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := engine.ProcessQuery(tt.query)
			if !strings.EqualFold(res.RewrittenQuery, tt.expected) {
				t.Errorf("ProcessQuery(%q) = %q, want %q (matches: %+v, variants: %+v)",
					tt.query, res.RewrittenQuery, tt.expected, res.Matches, res.Variants)
			}
			if !res.NeedsFanout {
				t.Errorf("Expected NeedsFanout=true for %q", tt.query)
			}
		})
	}
}

func TestCyrillicTyposForLatinArtists(t *testing.T) {
	dict := setupTestDictionary()
	engine := NewEngine(dict)

	tests := []struct {
		name     string
		query    string
		expected string
	}{
		{
			name:     "Cyrillic with typo at end офспринк -> The Offspring",
			query:    "офспринк",
			expected: "The Offspring",
		},
		{
			name:     "Cyrillic with vowel typo металыка -> Metallica",
			query:    "металыка",
			expected: "Metallica",
		},
		{
			name:     "Cyrillic single 'л' and prefix металик -> Metallica",
			query:    "металик",
			expected: "Metallica",
		},
		{
			name:     "Cyrillic vowel typo рамштаин -> Rammstein",
			query:    "рамштаин",
			expected: "Rammstein",
		},
		{
			name:     "Cyrillic typo in Russian band сплен -> Сплин",
			query:    "сплен",
			expected: "Сплин",
		},
		{
			name:     "Cyrillic typo at end депешы -> Depeche Mode",
			query:    "депешы",
			expected: "Depeche Mode",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := engine.ProcessQuery(tt.query)
			if !strings.EqualFold(res.RewrittenQuery, tt.expected) {
				t.Errorf("ProcessQuery(%q) = %q, want %q (matches: %+v, variants: %+v)",
					tt.query, res.RewrittenQuery, tt.expected, res.Matches, res.Variants)
			}
		})
	}
}

func TestCutTheBridge(t *testing.T) {
	dict := NewDictionary()
	items := []DictItem{
		{Text: "Cut the Bridge", EntityType: "track", PlayCount: 100},
		{Text: "Bridge", EntityType: "track", PlayCount: 50},
		{Text: "London Bridge", EntityType: "track", PlayCount: 80},
		{Text: "Under the Bridge", EntityType: "track", PlayCount: 120},
		{Text: "Bad Omens", EntityType: "artist", PlayCount: 200},
	}
	dict.Rebuild(items)
	engine := NewEngine(dict)

	queries := []string{
		"cut the bridge",
		"cut the",
		"cut bridge",
		"bridge",
		"кат зе бридж",
		"кат бридж",
		"бридж",
		"cut the br",
	}
	for _, q := range queries {
		res := engine.ProcessQuery(q)
		expanded := ExpandQuery(q, &res)
		t.Logf("Query: %-15q -> Rewritten: %-18q, Expanded: %v",
			q, res.RewrittenQuery, expanded)
	}
}
