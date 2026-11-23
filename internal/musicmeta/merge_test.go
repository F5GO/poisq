package musicmeta

import (
	"reflect"
	"testing"
	"time"
)

func TestMergeHonorsLocksPriorityMissingConfidenceAndExplicitFalse(t *testing.T) {
	old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	newer := old.Add(time.Hour)
	current := Document{TrackID: "t", Title: "Manual", Album: "Local", Compilation: true, Genres: []string{"Rock"}, FieldStates: map[Field]FieldState{
		FieldTitle:       {Source: SourceManual, Confidence: 1, Locked: true, UpdatedAt: old},
		FieldAlbum:       {Source: SourceAudioFile, Confidence: 1, UpdatedAt: old},
		FieldCompilation: {Source: SourceLegacy, Confidence: 1, UpdatedAt: old},
		FieldGenres:      {Source: SourceAudioFile, Confidence: 1, UpdatedAt: old},
	}}
	remote := Document{TrackID: "t", Title: "Remote", Album: "Remote Album", Compilation: false, Label: "Label", Genres: []string{"Pop"}, FieldStates: map[Field]FieldState{
		FieldTitle:       {Source: SourceMusicBrainz, Provider: "mb", Confidence: 1, UpdatedAt: newer},
		FieldAlbum:       {Source: SourceMusicBrainz, Provider: "mb", Confidence: 1, UpdatedAt: newer},
		FieldCompilation: {Source: SourceMusicBrainz, Provider: "mb", Confidence: 1, UpdatedAt: newer},
		FieldLabel:       {Source: SourceMusicBrainz, Provider: "mb", Confidence: .4, UpdatedAt: newer},
		FieldGenres:      {Source: SourceDerived, Provider: "derived", Confidence: 1, UpdatedAt: newer},
	}}
	got, err := Merge(current, []Document{remote}, DefaultMergePolicy())
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "Manual" || got.Album != "Local" || got.Compilation || got.Label != "" || !reflect.DeepEqual(got.Genres, []string{"Rock"}) {
		t.Fatalf("unexpected merge: %#v", got)
	}
	if got.FieldStates[FieldCompilation].Source != SourceMusicBrainz {
		t.Fatal("explicit false did not retain winning provenance")
	}
}

func TestMergeNewerAndNeverModes(t *testing.T) {
	baseTime := time.Now().UTC()
	current := Document{TrackID: "t", Album: "Current", Title: "Current", FieldStates: map[Field]FieldState{
		FieldAlbum: {Source: SourceAudioFile, Confidence: 1, UpdatedAt: baseTime},
		FieldTitle: {Source: SourceAudioFile, Confidence: 1, UpdatedAt: baseTime},
	}}
	policy := MergePolicy{Rules: []ProviderRule{
		{Source: SourceAudioFile, Priority: 10, RefreshMode: RefreshPriority, Enabled: true},
		{Source: SourceNFO, Priority: 1, RefreshMode: RefreshNewer, Enabled: true},
		{Source: SourceMusicBrainz, Priority: 0, RefreshMode: RefreshNever, Enabled: true},
	}}
	older := Document{TrackID: "t", Album: "Older", FieldStates: map[Field]FieldState{FieldAlbum: {Source: SourceNFO, Confidence: 1, UpdatedAt: baseTime.Add(-time.Minute)}}}
	newer := Document{TrackID: "t", Album: "Newer", FieldStates: map[Field]FieldState{FieldAlbum: {Source: SourceNFO, Confidence: 1, UpdatedAt: baseTime.Add(time.Minute)}}}
	never := Document{TrackID: "t", Title: "Never", FieldStates: map[Field]FieldState{FieldTitle: {Source: SourceMusicBrainz, Confidence: 1, UpdatedAt: baseTime.Add(time.Hour)}}}
	got, err := Merge(current, []Document{older, never, newer}, policy)
	if err != nil {
		t.Fatal(err)
	}
	if got.Album != "Newer" || got.Title != "Current" {
		t.Fatalf("unexpected newer/never merge: %#v", got)
	}
}

func TestMergeIsIndependentOfCandidateOrder(t *testing.T) {
	policy := MergePolicy{Rules: []ProviderRule{{Source: SourceMusicBrainz, Priority: 1, RefreshMode: RefreshPriority, Enabled: true}}}
	base := Document{TrackID: "t", FieldStates: map[Field]FieldState{}}
	at := time.Now().UTC()
	a := Document{TrackID: "t", Title: "B", FieldStates: map[Field]FieldState{FieldTitle: {Source: SourceMusicBrainz, Provider: "z", Confidence: .8, UpdatedAt: at}}}
	b := Document{TrackID: "t", Title: "A", FieldStates: map[Field]FieldState{FieldTitle: {Source: SourceMusicBrainz, Provider: "a", Confidence: .8, UpdatedAt: at}}}
	one, err := Merge(base, []Document{a, b}, policy)
	if err != nil {
		t.Fatal(err)
	}
	two, err := Merge(base, []Document{b, a}, policy)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(one, two) || one.Title != "A" {
		t.Fatalf("order-dependent result: %#v %#v", one, two)
	}
}

func TestValidateMergePolicyRejectsInvalidAndDuplicateRules(t *testing.T) {
	if _, err := ValidateMergePolicy(MergePolicy{}); err == nil {
		t.Fatal("empty policy accepted")
	}
	duplicate := MergePolicy{Rules: []ProviderRule{
		{Source: SourceAudioFile, RefreshMode: RefreshPriority, Enabled: true},
		{Source: SourceAudioFile, RefreshMode: RefreshPriority, Enabled: true},
	}}
	if _, err := ValidateMergePolicy(duplicate); err == nil {
		t.Fatal("duplicate policy accepted")
	}
}
