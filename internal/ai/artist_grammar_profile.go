// Package ai — модуль определения грамматического профиля артиста (тип: сольный исполнитель,
// дуэт или группа; пол: мужской или женский) по собранным доказательствам (evidence).
// Используется на этапе синтеза и редактирования биографии (Edit), чтобы гарантировать
// правильный грамматический род и число в русском тексте биографии.
package ai

import (
	"strings"
	"unicode"
)

type ArtistEntityKind string

const (
	ArtistEntityUnknown ArtistEntityKind = ""
	ArtistEntitySolo    ArtistEntityKind = "solo"
	ArtistEntityDuo     ArtistEntityKind = "duo"
	ArtistEntityGroup   ArtistEntityKind = "group"
)

type ArtistGender string

const (
	ArtistGenderUnknown ArtistGender = ""
	ArtistGenderMale    ArtistGender = "male"
	ArtistGenderFemale  ArtistGender = "female"
)

type ArtistGrammarProfile struct {
	Kind   ArtistEntityKind
	Gender ArtistGender
}

// InferArtistGrammarProfile анализирует имя артиста и карточки фактов (на русском и английском языках),
// определяя, является ли артист сольным исполнителем, дуэтом или группой, а для сольного исполнителя — его пол.
func InferArtistGrammarProfile(artistName string, evidence []SynthesisEvidence) ArtistGrammarProfile {
	var combined strings.Builder
	for _, item := range evidence {
		if combined.Len() > 0 {
			combined.WriteByte(' ')
		}
		combined.WriteString(item.Text)
	}
	text := strings.ToLower(combined.String())
	if text == "" {
		return ArtistGrammarProfile{}
	}

	tokens := tokenizeProfileWords(text)
	tokenCounts := make(map[string]int, len(tokens))
	for _, tok := range tokens {
		tokenCounts[tok]++
	}

	// Сначала нейтрализуем конструкции «участник группы», «солистка группы», «member of the band»,
	// которые указывают на сольного артиста, состоящего в группе, а не на саму группу.
	soloMemberPhrases := []string{
		"участник группы", "участница группы", "участник коллектива", "участница коллектива",
		"бывший участник", "бывшая участница", "солист группы", "солистка группы",
		"вокалист группы", "вокалистка группы", "фронтмен группы", "лидер группы",
		"основатель группы", "основательница группы", "сооснователь группы",
		"member of the band", "member of the group", "member of band", "member of group",
		"former member of", "frontman of", "frontwoman of", "lead singer of", "vocalist of",
	}
	soloPhraseHits := 0
	cleanedForGroup := text
	for _, phrase := range soloMemberPhrases {
		if count := strings.Count(cleanedForGroup, phrase); count > 0 {
			soloPhraseHits += count
			cleanedForGroup = strings.ReplaceAll(cleanedForGroup, phrase, " ")
		}
	}
	groupTokens := tokenizeProfileWords(cleanedForGroup)
	groupCounts := make(map[string]int, len(groupTokens))
	for _, tok := range groupTokens {
		groupCounts[tok]++
	}

	duoScore := countWordHits(groupCounts,
		"дуэт", "дуэта", "дуэту", "дуэтом", "duo",
	) * 3
	if strings.Contains(cleanedForGroup, "electronic duo") || strings.Contains(cleanedForGroup, "musical duo") || strings.Contains(cleanedForGroup, "музыкальный дуэт") || strings.Contains(cleanedForGroup, "электронный дуэт") {
		duoScore += 4
	}

	groupScore := countWordHits(groupCounts,
		"группа", "группы", "группе", "группу", "группой",
		"коллектив", "коллектива", "коллективу", "коллективом",
		"бэнд", "трио", "квартет", "ансамбль",
		"основана", "образована", "сформирована", "распалась",
		"выпустили", "записали", "основали", "представили", "выступили", "начали",
		"band", "group", "collective", "trio", "quartet", "members", "their", "they",
	)
	for _, phrase := range []string{
		"музыкальная группа", "рок-группа", "поп-группа", "хип-хоп-группа", "рэп-группа",
		"электронная группа", "инди-группа", "музыкальный коллектив",
		"rock band", "pop band", "hip hop group", "rap group", "music group",
		"formed in", "consists of", "band members",
	} {
		if strings.Contains(cleanedForGroup, phrase) {
			groupScore += 3
		}
	}

	femalePersonalScore := countWordHits(tokenCounts,
		"певица", "певицы", "певице", "певицу", "певицей",
		"исполнительница", "исполнительницы", "исполнительнице", "исполнительницу",
		"вокалистка", "солистка", "участница", "актриса", "автор-исполнительница",
		"родилась", "выросла", "урождённая", "урожденная", "известная",
		"she", "her", "hers", "herself", "frontwoman", "actress", "songstress",
	) * 3
	for _, phrase := range []string{
		"сольная певица", "российская певица", "украинская певица", "американская певица",
		"поп-певица", "сольная исполнительница", "female singer", "female rapper", "female artist",
	} {
		if strings.Contains(text, phrase) {
			femalePersonalScore += 4
		}
	}

	malePersonalScore := countWordHits(tokenCounts,
		"певец", "певца", "певцу", "певцом",
		"рэпер", "рэпера", "рэперу", "рэпером",
		"вокалист", "солист", "фронтмен", "участник",
		"диджей", "диджея", "битмейкер", "продюсер", "продюсера", "саунд-продюсер",
		"родился", "вырос", "известный",
		"выпустил", "записал", "начал", "основал", "получил", "стал", "представил", "выступил",
		"он", "его", "ему", "им",
		"he", "his", "him", "himself", "frontman", "rapper",
	)*2 + soloPhraseHits*3
	for _, phrase := range []string{
		"сольный исполнитель", "хип-хоп-исполнитель", "рэп-исполнитель", "электронный музыкант",
		"диджей и продюсер", "dj and producer", "male singer",
	} {
		if strings.Contains(text, phrase) {
			malePersonalScore += 4
		}
	}

	// Если есть личные женские маркеры, учитываем также женские глаголы прошедшего времени
	if femalePersonalScore > 0 {
		femaleVerbHits := countWordHits(tokenCounts,
			"выпустила", "записала", "начала", "основала", "получила", "стала", "представила", "выступила", "она", "её", "ее",
		)
		femalePersonalScore += femaleVerbHits * 2
	}

	soloScore := max(femalePersonalScore, malePersonalScore)

	if duoScore >= 3 && duoScore >= soloScore {
		return ArtistGrammarProfile{Kind: ArtistEntityDuo}
	}
	if groupScore >= 4 && groupScore > soloScore {
		return ArtistGrammarProfile{Kind: ArtistEntityGroup}
	}
	if femalePersonalScore >= 3 && femalePersonalScore > malePersonalScore {
		return ArtistGrammarProfile{Kind: ArtistEntitySolo, Gender: ArtistGenderFemale}
	}
	if malePersonalScore >= 3 && malePersonalScore > femalePersonalScore {
		return ArtistGrammarProfile{Kind: ArtistEntitySolo, Gender: ArtistGenderMale}
	}
	if groupScore >= 2 && soloScore == 0 {
		return ArtistGrammarProfile{Kind: ArtistEntityGroup}
	}
	return ArtistGrammarProfile{}
}

// FormatGrammarPromptDirective возвращает строгую инструкцию для системного промпта редактора биографий.
func FormatGrammarPromptDirective(profile ArtistGrammarProfile) string {
	switch profile.Kind {
	case ArtistEntityGroup:
		return " Artist grammar profile: MUSICAL GROUP / BAND (музыкальная группа / коллектив). Write strictly about a group/band using plural forms (участники, музыканты, выпустили, записали, основали, они) or feminine agreement with the word «группа» (группа выпустила, была основана). NEVER refer to the artist as a solo singer/musician (певец, певица, сольный исполнитель, родился, родилась)."
	case ArtistEntityDuo:
		return " Artist grammar profile: MUSICAL DUO (музыкальный дуэт). Write strictly about a duo/project (дуэт, участники дуэта, проект, музыканты выпустили, записали). NEVER refer to the artist as a solo singer/musician (певец, певица, сольный исполнитель, родился, родилась)."
	case ArtistEntitySolo:
		switch profile.Gender {
		case ArtistGenderFemale:
			return " Artist grammar profile: SOLO FEMALE ARTIST (сольная исполнительница / певица, женский пол). Use strictly Russian feminine grammatical gender for the artist and all past-tense verbs/participles (певица, исполнительница, родилась, выпустила, записала, начала, основала, получила, стала, она, её). NEVER use masculine gender (певец, исполнитель, родился, выпустил, записал, начал, он) and NEVER call her a musical group/band."
		case ArtistGenderMale:
			return " Artist grammar profile: SOLO MALE ARTIST (сольный исполнитель, мужской пол). Use strictly Russian masculine grammatical gender for the artist and all past-tense verbs/participles (исполнитель, музыкант, рэпер, певец, диджей, продюсер, родился, выпустил, записал, начал, основал, получил, стал, он, его). NEVER use feminine gender (певица, исполнительница, родилась, выпустила, она) and NEVER call him a musical group/band."
		}
	}
	return ""
}

// ValidateSummaryGrammarProfile проверяет сгенерированный русский текст биографии на соответствие
// определённому полу и формату артиста (сольный исполнитель / дуэт / группа).
func ValidateSummaryGrammarProfile(summary string, profile ArtistGrammarProfile) error {
	if profile.Kind == ArtistEntityUnknown {
		return nil
	}
	tokens := tokenizeProfileWords(strings.ToLower(summary))
	counts := make(map[string]int, len(tokens))
	for _, tok := range tokens {
		counts[tok]++
	}

	switch profile.Kind {
	case ArtistEntitySolo:
		if profile.Gender == ArtistGenderFemale {
			malePersonMarkers := countWordHits(counts,
				"певец", "певца", "певцу", "певцом", "вокалист", "солист", "фронтмен",
				"родился", "вырос", "известный",
			)
			femaleMarkers := countWordHits(counts,
				"певица", "певицы", "певице", "певицу", "певицей",
				"исполнительница", "исполнительницы", "вокалистка", "солистка", "участница", "актриса",
				"родилась", "выросла", "известная",
				"выпустила", "записала", "начала", "основала", "получила", "стала", "представила", "выступила", "она",
			)
			maleVerbs := countWordHits(counts,
				"выпустил", "записал", "начал", "основал", "получил", "стал", "представил", "выступил", "он",
			)
			if malePersonMarkers > 0 || (femaleMarkers == 0 && maleVerbs > 0) {
				return &synthesisStageError{
					reason:   "summary_edit_grammar_mismatch",
					msg:      "biography summary grammatical gender mismatch",
					feedback: "artist is a SOLO FEMALE artist; rewrite strictly in Russian feminine grammatical gender (певица, исполнительница, родилась, выпустила, записала, начала, она) without masculine forms",
				}
			}
		} else if profile.Gender == ArtistGenderMale {
			femalePersonMarkers := countWordHits(counts,
				"певица", "певицы", "певице", "певицу", "певицей",
				"исполнительница", "исполнительницы", "вокалистка", "солистка", "актриса",
				"родилась", "выросла", "урождённая", "урожденная", "известная",
			)
			if femalePersonMarkers > 0 {
				return &synthesisStageError{
					reason:   "summary_edit_grammar_mismatch",
					msg:      "biography summary grammatical gender mismatch",
					feedback: "artist is a SOLO MALE artist; rewrite strictly in Russian masculine grammatical gender (исполнитель, музыкант, рэпер, певец, родился, выпустил, записал, он) without feminine forms",
				}
			}
		}
	case ArtistEntityGroup, ArtistEntityDuo:
		groupIndicators := countWordHits(counts,
			"группа", "группы", "группе", "группу", "группой",
			"коллектив", "коллектива", "коллективу", "коллективом",
			"дуэт", "дуэта", "дуэту", "дуэтом",
			"трио", "квартет", "бэнд", "ансамбль", "проект", "проекта",
			"участники", "музыканты", "основана", "образована",
			"выпустили", "записали", "основали", "начали", "представили", "выступили", "они",
		)
		soloPersonOnly := countWordHits(counts,
			"родился", "родилась", "певец", "певица",
		)
		if groupIndicators == 0 && soloPersonOnly > 0 {
			return &synthesisStageError{
				reason:   "summary_edit_grammar_mismatch",
				msg:      "biography summary entity type mismatch",
				feedback: "artist is a musical group/duo, NOT a solo singer; describe the artist strictly as a group/collective/duo (группа, коллектив, дуэт, участники выпустили)",
			}
		}
	}
	return nil
}

func tokenizeProfileWords(text string) []string {
	return strings.FieldsFunc(text, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '-'
	})
}

func countWordHits(counts map[string]int, words ...string) int {
	total := 0
	for _, w := range words {
		total += counts[w]
	}
	return total
}
