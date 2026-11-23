package musicmeta

import (
	"math"
	"strings"
	"testing"
)

func TestValidateDocumentCanonicalizesCollectionsAndIDs(t *testing.T) {
	doc, err := ValidateDocument(Document{
		TrackID: " t ", Title: " Song ", Genres: []string{"Rock", " rock ", "Pop"},
		Credits: []Credit{{Role: RoleArtist, Name: " A "}, {Role: RoleArtist, Name: "a"}},
		ExternalIDs: ExternalIDs{
			{Type: IDMusicBrainzRecording, Value: "AAAAAAAA-AAAA-AAAA-AAAA-AAAAAAAAAAAA"},
			{Type: IDMusicBrainzReleaseTrack, Value: "BBBBBBBB-BBBB-BBBB-BBBB-BBBBBBBBBBBB"},
		},
		FieldStates: map[Field]FieldState{FieldTitle: {Source: SourceAudioFile, Confidence: .9}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if doc.TrackID != "t" || doc.Title != "Song" || len(doc.Genres) != 2 || len(doc.Credits) != 1 {
		t.Fatalf("unexpected normalization: %#v", doc)
	}
	if doc.ExternalIDs[0].Type == doc.ExternalIDs[1].Type || doc.ExternalIDs[0].Value[0] != 'a' {
		t.Fatalf("typed IDs collapsed: %#v", doc.ExternalIDs)
	}
}

func TestValidateDocumentRejectsUnsafeAndUnboundedValues(t *testing.T) {
	badRating := 101
	bad := []Document{
		{TrackID: ""},
		{TrackID: "t\x00x"},
		{TrackID: "t", Title: strings.Repeat("x", MaxTextBytes+1)},
		{TrackID: "t", ReleaseDate: PartialDate{Value: "2023-02-29"}},
		{TrackID: "t", ExternalIDs: ExternalIDs{{Type: IDMusicBrainzRecording, Value: "not-a-uuid"}}},
		{TrackID: "t", FieldStates: map[Field]FieldState{FieldTitle: {Source: SourceLegacy, Confidence: math.NaN()}}},
		{TrackID: "t", FieldStates: map[Field]FieldState{"unknown": {Source: SourceLegacy, Confidence: 1}}},
		{TrackID: "t", BPM: 1000},
		{TrackID: "t", Rating: &badRating},
		{TrackID: "t", CustomTags: []CustomTag{{Key: "TITLE", Value: "shadow"}}},
		{TrackID: "t", CustomTags: []CustomTag{{Key: "bad:key", Value: "x"}}},
	}
	for i, doc := range bad {
		if _, err := ValidateDocument(doc); err == nil {
			t.Fatalf("case %d unexpectedly valid", i)
		}
	}
}

func TestValidateDocumentCanonicalizesCustomTags(t *testing.T) {
	rating := 0
	doc, err := ValidateDocument(Document{TrackID: "t", BPM: 128, Rating: &rating, CustomTags: []CustomTag{{Key: " Mood ", Value: " calm "}}})
	if err != nil {
		t.Fatal(err)
	}
	if doc.BPM != 128 || doc.Rating == nil || *doc.Rating != 0 || len(doc.CustomTags) != 1 || doc.CustomTags[0].Key != "Mood" || doc.CustomTags[0].Value != "calm" {
		t.Fatalf("unexpected expanded tags: %#v", doc)
	}
}
