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
// При определении пола игнорирует упоминания сторонних продюсеров/коллабораторов (например, «в альбоме продюсера Biicla»).
func InferArtistGrammarProfile(artistName string, evidence []SynthesisEvidence) ArtistGrammarProfile {
	var allBuilder strings.Builder
	var primaryBuilder strings.Builder
	lowerArtist := strings.ToLower(strings.TrimSpace(artistName))

	for idx, item := range evidence {
		txt := strings.TrimSpace(item.Text)
		if txt == "" {
			continue
		}
		if allBuilder.Len() > 0 {
			allBuilder.WriteByte(' ')
		}
		allBuilder.WriteString(txt)

		// Первичные предложения о самом артисте (а не о сторонних продюсерах/коллабораторах в фитах)
		for _, clause := range splitProfileClauses(txt) {
			lowerClause := strings.ToLower(strings.TrimSpace(clause))
			if lowerClause == "" {
				continue
			}
			if isCollaboratorSubClause(lowerClause, lowerArtist) {
				continue
			}
			if idx == 0 || lowerArtist == "" || strings.Contains(lowerClause, lowerArtist) || startsWithSubjectPronounOrRole(lowerClause) {
				if primaryBuilder.Len() > 0 {
					primaryBuilder.WriteByte(' ')
				}
				primaryBuilder.WriteString(lowerClause)
			}
		}
	}

	fullText := strings.ToLower(allBuilder.String())
	if fullText == "" {
		return ArtistGrammarProfile{}
	}
	primaryText := primaryBuilder.String()
	if primaryText == "" {
		primaryText = fullText
	}

	// Явные маркеры, которые Collector ставит для целевого артиста
	if strings.Contains(fullText, "female solo artist") || strings.Contains(fullText, "female singer") || strings.Contains(fullText, "female rapper") || strings.Contains(fullText, "female musician") || strings.Contains(fullText, "певица") || strings.Contains(fullText, "исполнительница") || strings.Contains(fullText, "автор-исполнительница") {
		return ArtistGrammarProfile{Kind: ArtistEntitySolo, Gender: ArtistGenderFemale}
	}
	if strings.Contains(fullText, "musical duo") || strings.Contains(fullText, "electronic duo") || strings.Contains(fullText, "hip-hop duo") || strings.Contains(fullText, "pop duo") || strings.Contains(fullText, "музыкальный дуэт") || strings.Contains(fullText, "электронный дуэт") {
		return ArtistGrammarProfile{Kind: ArtistEntityDuo}
	}

	// Нейтрализуем конструкции «участник группы», «солистка группы», «member of the band»,
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
	cleanedForGroup := primaryText
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

	primaryTokens := tokenizeProfileWords(primaryText)
	primaryCounts := make(map[string]int, len(primaryTokens))
	for _, tok := range primaryTokens {
		primaryCounts[tok]++
	}

	femalePersonalScore := countWordHits(primaryCounts,
		"певица", "певицы", "певице", "певицу", "певицей",
		"исполнительница", "исполнительницы", "исполнительнице", "исполнительницу",
		"вокалистка", "солистка", "участница", "актриса", "автор-исполнительница",
		"родилась", "выросла", "урождённая", "урожденная", "известная",
		"she", "her", "hers", "herself", "frontwoman", "actress", "songstress", "female",
	) * 3
	femalePersonalScore += countFemaleGivenNames(primaryCounts) * 4

	// В мужские маркеры включаем только однозначно мужские формы (без слов общего рода
	// вроде «продюсер», «диджей», «музыкант», которые могут относиться к женщине или к коллаборатору).
	malePersonalScore := countWordHits(primaryCounts,
		"певец", "певца", "певцу", "певцом",
		"рэпер", "рэпера", "рэперу", "рэпером",
		"вокалист", "солист", "фронтмен",
		"родился", "вырос", "известный",
		"he", "his", "him", "himself", "frontman", "male",
	)*3 + soloPhraseHits*2
	malePersonalScore += countMaleGivenNames(primaryCounts) * 4

	if femalePersonalScore > 0 {
		femaleVerbHits := countWordHits(primaryCounts,
			"выпустила", "записала", "начала", "основала", "получила", "стала", "представила", "выступила", "она", "её", "ее",
		)
		femalePersonalScore += femaleVerbHits * 2
	}
	if malePersonalScore > 0 {
		maleVerbHits := countWordHits(primaryCounts,
			"выпустил", "записал", "начал", "основал", "получил", "стал", "представил", "выступил", "он",
		)
		malePersonalScore += maleVerbHits * 2
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
	if strings.Contains(primaryText, "solo artist") || strings.Contains(primaryText, "сольный") || strings.Contains(primaryText, "сольная") {
		return ArtistGrammarProfile{Kind: ArtistEntitySolo, Gender: ArtistGenderUnknown}
	}
	return ArtistGrammarProfile{}
}

func splitProfileClauses(text string) []string {
	return strings.FieldsFunc(text, func(r rune) bool {
		return r == '.' || r == ';' || r == '\n'
	})
}

func isCollaboratorSubClause(lowerClause, lowerArtist string) bool {
	for _, marker := range []string{
		"в рамках сотрудничества с", "совместно с продюсером", "в альбоме продюсера",
		"принял участие в альбоме", "приняла участие в альбоме", "featured on", "produced by",
	} {
		if strings.Contains(lowerClause, marker) {
			return true
		}
	}
	return false
}

func startsWithSubjectPronounOrRole(lowerClause string) bool {
	for _, prefix := range []string{
		"он ", "она ", "они ", "he ", "she ", "they ", "певица ", "певец ", "рэпер ",
		"исполнительница ", "исполнитель ", "группа ", "дуэт ", "коллектив ", "родилась ", "родился ",
	} {
		if strings.HasPrefix(lowerClause, prefix) {
			return true
		}
	}
	return false
}

func countFemaleGivenNames(counts map[string]int) int {
	return countWordHits(counts,
		"анна", "марина", "ева", "ольга", "елена", "наталья", "юлия", "дарья",
		"полина", "ксения", "светлана", "татьяна", "екатерина", "алина", "диана",
		"кристина", "валерия", "вера", "надежда", "софия", "анастасия", "виктория",
		"яна", "ирина", "мария", "александра", "аника",
	)
}

func countMaleGivenNames(counts map[string]int) int {
	return countWordHits(counts,
		"илья", "юрий", "алексей", "александр", "дмитрий", "максим", "сергей",
		"андрей", "михаил", "иван", "артём", "артем", "никита", "владимир",
		"павел", "евгений", "денис", "роман", "кирилл", "антон", "игорь", "олег",
		"константин", "вячеслав", "тимур", "глеб", "василий", "фёдор", "федор",
	)
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
			return " Artist grammar profile: solo female artist. Express gender naturally through Russian feminine nouns and past-tense verbs (певица, исполнительница, автор-исполнительница, родилась, выпустила, записала, начала, основала, приняла участие, она, её). NEVER write awkward literal translations like «женская певица», «женская сольная исполнительница», or «solo-artist», NEVER use masculine forms (певец, родился, выпустил, он), and NEVER call her a group/band."
		case ArtistGenderMale:
			return " Artist grammar profile: solo male artist. Express gender naturally through Russian masculine nouns and past-tense verbs (рэпер, певец, электронный музыкант, саунд-продюсер, диджей, исполнитель, родился, выпустил, записал, начал, основал, принял участие, он, его). NEVER write awkward literal translations like «мужской сольный исполнитель», «мужской исполнитель», «мужской музыкант», or «solo-artist», NEVER use feminine forms (певица, родилась, выпустила, она), and NEVER call him a group/band."
		default:
			return " Artist grammar profile: solo artist. Determine whether this solo artist is female («певица, исполнительница, родилась, выпустила, она») or male («рэпер, певец, музыкант, продюсер, родился, выпустил, он») from their real name, pronouns, and context, keep Russian grammatical gender consistent throughout, and NEVER write awkward phrases like «мужской сольный исполнитель», «женская исполнительница», or «solo-artist»."
		}
	default:
		return " Determine whether the artist is a solo female artist («певица, исполнительница, родилась, выпустила, она»), solo male artist («рэпер, певец, музыкант, родился, выпустил, он»), musical duo («дуэт»), or group/band («группа, коллектив») and keep Russian grammatical gender and number 100% consistent. NEVER write awkward literal translations like «мужской сольный исполнитель», «женская исполнительница», or «solo-artist»."
	}
}

// CleanAwkwardGrammarPhrases убирает неестественные кальки вроде «мужской сольный исполнитель» из русского текста.
func CleanAwkwardGrammarPhrases(text string) string {
	replacements := []struct {
		old string
		new string
	}{
		{"мужской сольный исполнитель-рэпер", "рэпер и сольный исполнитель"},
		{"мужской сольный исполнитель‑рэпер", "рэпер и сольный исполнитель"},
		{"Мужской сольный исполнитель-рэпер", "Рэпер и сольный исполнитель"},
		{"Мужской сольный исполнитель‑рэпер", "Рэпер и сольный исполнитель"},
		{"мужской сольный исполнитель и ", ""},
		{"Мужской сольный исполнитель и ", ""},
		{"мужской сольный исполнитель, ", "сольный исполнитель, "},
		{"мужской сольный исполнитель ", "сольный исполнитель "},
		{"мужской сольный исполнитель", "сольный исполнитель"},
		{"Мужской сольный исполнитель", "Сольный исполнитель"},
		{"мужской исполнитель и ", ""},
		{"мужской исполнитель", "исполнитель"},
		{"женская сольная исполнительница и ", "певица и "},
		{"женская сольная исполнительница", "певица и исполнительница"},
		{"женская исполнительница", "исполнительница"},
		{"женская певица", "певица"},
	}
	out := text
	for _, pair := range replacements {
		if strings.Contains(out, pair.old) {
			out = strings.ReplaceAll(out, pair.old, pair.new)
		}
	}
	return normalizeBiographySpaces(out)
}

// ValidateSummaryGrammarProfile проверяет сгенерированный русский текст биографии на соответствие
// определённому полу и формату артиста (сольный исполнитель / дуэт / группа), а также отсутствие «solo-artist».
func ValidateSummaryGrammarProfile(summary string, profile ArtistGrammarProfile) error {
	lower := strings.ToLower(summary)
	if strings.Contains(lower, "solo-artist") || strings.Contains(lower, "solo artist") {
		return &synthesisStageError{
			reason:   "summary_edit_grammar_mismatch",
			msg:      "biography summary contains untranslated solo-artist term",
			feedback: "replace English «solo-artist» with natural Russian («певица / сольная исполнительница» for female or «сольный исполнитель / музыкант / певец» for male)",
		}
	}
	if profile.Kind == ArtistEntityUnknown {
		return nil
	}
	tokens := tokenizeProfileWords(lower)
	counts := make(map[string]int, len(tokens))
	for _, tok := range tokens {
		counts[tok]++
	}

	switch profile.Kind {
	case ArtistEntitySolo:
		if profile.Gender == ArtistGenderFemale {
			malePersonMarkers := countWordHits(counts,
				"певец", "певца", "певцу", "певцом", "вокалист", "солист", "фронтмен",
				"родился", "вырос", "принял",
			)
			femaleMarkers := countWordHits(counts,
				"певица", "певицы", "певице", "певицу", "певицей",
				"исполнительница", "исполнительницы", "вокалистка", "солистка", "участница", "актриса",
				"родилась", "выросла", "известная", "приняла",
				"выпустила", "записала", "начала", "основала", "получила", "стала", "представила", "выступила", "она",
			)
			maleVerbs := countWordHits(counts,
				"выпустил", "записал", "начал", "основал", "получил", "стал", "представил", "выступил", "он",
			)
			if malePersonMarkers > 0 || (femaleMarkers == 0 && maleVerbs > 0) {
				return &synthesisStageError{
					reason:   "summary_edit_grammar_mismatch",
					msg:      "biography summary grammatical gender mismatch",
					feedback: "artist is a SOLO FEMALE artist; rewrite strictly in Russian feminine grammatical gender (певица, исполнительница, родилась, выпустила, записала, начала, приняла участие, она) without masculine forms",
				}
			}
		} else if profile.Gender == ArtistGenderMale {
			femalePersonMarkers := countWordHits(counts,
				"певица", "певицы", "певице", "певицу", "певицей",
				"исполнительница", "исполнительницы", "вокалистка", "солистка", "актриса",
				"родилась", "выросла", "урождённая", "урожденная", "известная", "приняла",
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
