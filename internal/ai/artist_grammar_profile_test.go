// Package ai — модульные тесты определения пола и типа артиста (сольный исполнитель, дуэт, группа)
// и проверки грамматического согласования в русском тексте биографии.
package ai

import (
	"context"
	"strings"
	"testing"
)

func TestInferArtistGrammarProfile(t *testing.T) {
	femaleProfile := InferArtistGrammarProfile("МакSим", []SynthesisEvidence{
		{ID: "1", Origin: "wikipedia", Language: "ru", Text: "МакSим (Марина Абросимова) — российская певица, автор-исполнительница и музыкальный продюсер. Родилась в Казани и выпустила дебютный альбом «Трудный возраст»."},
		{ID: "2", Origin: "en.example", Language: "en", Text: "She released her second studio album and won multiple music awards."},
	})
	if femaleProfile.Kind != ArtistEntitySolo || femaleProfile.Gender != ArtistGenderFemale {
		t.Fatalf("expected solo female profile for МакSим, got %#v", femaleProfile)
	}

	maleBandMemberProfile := InferArtistGrammarProfile("4atty aka Tilla", []SynthesisEvidence{
		{ID: "1", Origin: "genius.com", Language: "ru", Text: "Илья Капустин (4atty aka Tilla) — украинский рэпер, родился в Киеве, участник группы «Грибы» и основатель коллектива «7 мостов»."},
		{ID: "2", Origin: "last.fm", Language: "en", Text: "He started his career in 2005 and released several hip-hop albums."},
	})
	if maleBandMemberProfile.Kind != ArtistEntitySolo || maleBandMemberProfile.Gender != ArtistGenderMale {
		t.Fatalf("expected solo male profile for band member 4atty aka Tilla, got %#v", maleBandMemberProfile)
	}

	groupProfile := InferArtistGrammarProfile("Мертвые Дельфины", []SynthesisEvidence{
		{ID: "1", Origin: "wikipedia", Language: "ru", Text: "«Мертвые Дельфины» — российская альтернативная рок-группа, основана в 1996 году. Участники записали альбом «Одуванчики для канареек»."},
		{ID: "2", Origin: "musicbrainz", Language: "en", Text: "Russian rock band formed in Chechnya, they released multiple studio albums."},
	})
	if groupProfile.Kind != ArtistEntityGroup {
		t.Fatalf("expected group profile for Мертвые Дельфины, got %#v", groupProfile)
	}

	duoProfile := InferArtistGrammarProfile("Talemates", []SynthesisEvidence{
		{ID: "1", Origin: "bandcamp.com", Language: "en", Text: "Talemates is an electronic musical duo producing melodic house tracks."},
		{ID: "2", Origin: "soundcloud.com", Language: "ru", Text: "Электронный дуэт выпустил совместный релиз и ремиксы."},
	})
	if duoProfile.Kind != ArtistEntityDuo {
		t.Fatalf("expected duo profile for Talemates, got %#v", duoProfile)
	}

	// ANIKV: dry RateYourMusic card without pronouns + collaborator card mentioning male producer Biicla
	// must NOT misclassify ANIKV as male; and when real name Анна or певица is present, must classify as female.
	anikvNeutral := InferArtistGrammarProfile("ANIKV", []SynthesisEvidence{
		{ID: "1", Origin: "rateyourmusic.com", Language: "en", Text: "ANIKV is a solo artist from Moscow, born April 29, 1995, genres Alternative R&B and Pop Rap."},
		{ID: "2", Origin: "the-flow.ru", Language: "ru", Text: "В рамках сотрудничества с продюсером Biicla приняла участие в альбоме совместно с Feduk и DJ Stonik1917."},
	})
	if anikvNeutral.Gender == ArtistGenderMale {
		t.Fatalf("expected ANIKV with male collaborator Biicla NOT to be misclassified as male, got %#v", anikvNeutral)
	}

	anikvFemale := InferArtistGrammarProfile("ANIKV", []SynthesisEvidence{
		{ID: "1", Origin: "rateyourmusic.com", Language: "en", Text: "ANIKV (Анна Пурцен) is a solo artist from Moscow, born April 29, 1995, genres Alternative R&B and Pop Rap."},
		{ID: "2", Origin: "the-flow.ru", Language: "ru", Text: "В рамках сотрудничества с продюсером Biicla приняла участие в альбоме совместно с Feduk и DJ Stonik1917."},
	})
	if anikvFemale.Kind != ArtistEntitySolo || anikvFemale.Gender != ArtistGenderFemale {
		t.Fatalf("expected ANIKV with Анна Пурцен to be solo female, got %#v", anikvFemale)
	}

	cleanedNicD := CleanAwkwardGrammarPhrases("Nic D — мужской сольный исполнитель‑рэпер, который начал выпускать треки.")
	if strings.Contains(cleanedNicD, "мужской") {
		t.Fatalf("expected awkward 'мужской' to be removed from Nic D summary, got %q", cleanedNicD)
	}
	cleanedBiicla := CleanAwkwardGrammarPhrases("Biicla — мужской сольный исполнитель и электронный музыкант из России.")
	if cleanedBiicla != "Biicla — электронный музыкант из России." {
		t.Fatalf("expected awkward 'мужской сольный исполнитель и ' to be cleaned for Biicla, got %q", cleanedBiicla)
	}
}

func TestBiographyEditorRetriesWrongGenderAndEntityKind(t *testing.T) {
	wrongMaleForFemale := `{"summary":"МакSим — известный российский певец и музыкант, который родился в Казани и начал сольную карьеру в начале двухтысячных годов. Он выпустил успешный дебютный альбом «Трудный возраст» и записал ряд популярных синглов.","sentences":[{"id":1,"text":"МакSим — известный российский певец и музыкант, который родился в Казани и начал сольную карьеру в начале двухтысячных годов.","source_ids":["web:1"]},{"id":2,"text":"Он выпустил успешный дебютный альбом «Трудный возраст» и записал ряд популярных синглов.","source_ids":["web:2"]}],"used_source_ids":["web:1","web:2"]}`
	correctFemale := `{"summary":"МакSим — российская певица и автор-исполнительница, которая родилась в Казани и начала сольную карьеру в начале двухтысячных годов. Она выпустила успешный дебютный альбом «Трудный возраст» и записала ряд популярных синглов.","sentences":[{"id":1,"text":"МакSим — российская певица и автор-исполнительница, которая родилась в Казани и начала сольную карьеру в начале двухтысячных годов.","source_ids":["web:1"]},{"id":2,"text":"Она выпустила успешный дебютный альбом «Трудный возраст» и записала ряд популярных синглов.","source_ids":["web:2"]}],"used_source_ids":["web:1","web:2"]}`

	gen := &synthesisSequenceGenerator{responses: []string{wrongMaleForFemale, correctFemale}}
	ctx := WithEditorArtistName(context.Background(), "МакSим")
	res, err := NewBiographySynthesisAI(gen).Edit(ctx, []SynthesisEvidence{
		{ID: "web:1", Origin: "one.example", Language: "ru", Text: "МакSим — российская певица, родилась в Казани и начала карьеру в начале 2000-х."},
		{ID: "web:2", Origin: "two.example", Language: "en", Text: "She released her debut album Trudny Vozrast and recorded hit singles."},
	})
	if err != nil || gen.calls != 2 || !strings.Contains(res.Summary, "российская певица") {
		t.Fatalf("expected editor to retry masculine text for female artist, got calls=%d err=%v summary=%q", gen.calls, err, res.Summary)
	}
}
