package lyrics

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

const songA = `Отпускаю небо улетает наше прошлое лето
с телефонными звонками слезы высыхают на ресницах
как синие звезды нам могли присниться
отпускаю небо улетая наше прошлое лето
вдоль ночных дорог шла босиком жалея
теперь в твоих руках потеряй и сломай
не забывай не забывай знаешь ли ты
тихое прощай мне не говори
я не вернусь я не вернусь никогда
отпускаю небо улетает наше прошлое лето
с телефонными звонками слезы высыхают на ресницах
как синие звезды нам могли присниться`

const songOther = `совсем другая песня про море и корабли которые уходят далеко
в туман где нет ни берега ни неба только ветер и вода
капитан смотрит вперед и не видит огней впереди
матросы поют старые песни о доме и о любви
а чайки кружат над мачтой и кричат на разные голоса
волны бьются о борт и уносят нас дальше и дальше`

func makeLRC(text string, stepSec int) string {
	var sb strings.Builder
	for i, l := range strings.Split(text, "\n") {
		sec := 5 + i*stepSec
		sb.WriteString(fmt.Sprintf("[%02d:%02d.00] %s\n", sec/60, sec%60, l))
	}
	return sb.String()
}

func cand(provider, plain, synced string, score float64) TournamentCandidate {
	kind := KindPlain
	if synced != "" {
		kind = KindLRC
	}
	return TournamentCandidate{
		ProviderID: provider,
		Score:      CandidateScore{Total: score, Accepted: true},
		Lyrics:     &Lyrics{Kind: kind, PlainLyrics: plain, SyncedLyrics: synced, Lines: ParseLRC(synced), Source: provider},
	}
}

func TestConsensusPicksFullLRCFromMajority(t *testing.T) {
	q := SearchQuery{Artist: "МакSим", Title: "Отпускаю", Duration: 90 * time.Second}
	cands := []TournamentCandidate{
		cand("yandex", songA, "", 78),
		cand("genius", songA+"\n[Chorus]\n(ooh)", "", 76),
		cand("lrclib", "", makeLRC(songA, 7), 70),
		cand("netease", songOther, makeLRC(songOther, 10), 99), // чужая песня с «лучшим» баллом
	}
	w, info := PickConsensusWinner(cands, q)
	if w == nil || w.ProviderID != "lrclib" {
		t.Fatalf("ожидался lrclib, получено %+v / %+v", w, info)
	}
	if !info.HasFullLRC || !info.ConsensusOK || info.Agreeing != 3 {
		t.Fatalf("неверная сводка консенсуса: %+v", info)
	}
	if len(info.Dissenters) != 1 || info.Dissenters[0] != "netease" {
		t.Fatalf("netease должен быть в несогласных: %+v", info.Dissenters)
	}
}

func TestConsensusRejectsTruncatedLRC(t *testing.T) {
	q := SearchQuery{Title: "x", Duration: 4 * time.Minute}
	short := makeLRC(strings.Join(strings.Split(songA, "\n")[:9], "\n"), 3) // оборван на ~30 сек из 4 минут
	cands := []TournamentCandidate{
		cand("yandex", songA, "", 78),
		cand("genius", songA, "", 76),
		cand("lrclib", "", short, 70),
	}
	w, info := PickConsensusWinner(cands, q)
	if w == nil || info.HasFullLRC {
		t.Fatalf("оборванный LRC не должен считаться полноценным: %+v", info)
	}
	if w.ProviderID != "yandex" {
		t.Fatalf("при отсутствии LRC ожидался лучший по баллу текст большинства, получено %s", w.ProviderID)
	}
}

func TestConsensusNeedsTwoSources(t *testing.T) {
	q := SearchQuery{Duration: 90 * time.Second}
	cands := []TournamentCandidate{cand("lrclib", "", makeLRC(songA, 7), 90)}
	if w, _ := PickConsensusWinner(cands, q); w != nil {
		t.Fatalf("один источник не образует консенсус")
	}
	cands = append(cands, cand("genius", songOther, "", 70))
	if w, info := PickConsensusWinner(cands, q); w != nil || info.ConsensusOK {
		t.Fatalf("несогласные источники не должны давать консенсус: %+v", info)
	}
}

func TestQueryVariantsForMaksim(t *testing.T) {
	q := SearchQuery{Artist: "МакSим", Title: "Отпускаю (Remastered 2011)"}
	vs := GenerateQueryVariants(q, 6)
	var joined []string
	for _, v := range vs {
		joined = append(joined, v.Artist+"|"+v.Title)
	}
	all := strings.Join(joined, "\n")
	for _, want := range []string{"MakSim|", "|Отпускаю"} {
		if !strings.Contains(all, want) {
			t.Errorf("в вариантах нет %q:\n%s", want, all)
		}
	}
}

func TestPrimaryArtistSplit(t *testing.T) {
	cases := map[string]string{
		"Grebz • 4atty aka Tilla • Sимптом": "Grebz",
		"Linkin Park feat. Someone":         "Linkin Park",
		"Земфира":                           "Земфира",
	}
	for in, want := range cases {
		if got := PrimaryArtist(in); got != want {
			t.Errorf("PrimaryArtist(%q) = %q, want %q", in, got, want)
		}
	}
}
