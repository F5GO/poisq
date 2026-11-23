package db

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestWritebackRepositoryStateMachineCommitsProposalAtomically(t *testing.T) {
	database, err := OpenPoisqDatabase(filepath.Join(t.TempDir(), "poisq.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	staging := NewStagingRepository(database)
	proposalID, err := staging.CreateProposal(StagingChangeRecord{TargetID: "t", ChangeType: "manual", CurrentJSON: `{}`, ProposedJSON: `{}`})
	if err != nil {
		t.Fatal(err)
	}
	repo := NewWritebackRepository(database)
	txID := strings.Repeat("a", 32)
	if err := repo.CreatePrepared(context.Background(), WritebackTransaction{
		TransactionID: txID, ProposalID: proposalID, Kind: "tags", TargetPath: "/music/t.mp3", OriginalJSON: `{"title":"old"}`,
	}); err != nil {
		t.Fatal(err)
	}
	if err := repo.Commit(context.Background(), txID, proposalID, `{}`); err == nil {
		t.Fatal("unverified transaction committed")
	}
	if err := repo.UpdateState(context.Background(), txID, "written", nil); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpdateState(context.Background(), txID, "verified", nil); err != nil {
		t.Fatal(err)
	}
	if err := repo.Commit(context.Background(), txID, proposalID, `{"title":"old"}`); err != nil {
		t.Fatal(err)
	}
	proposal, _ := staging.GetByID(proposalID)
	tx, _ := repo.GetByProposal(context.Background(), proposalID)
	if proposal.Status != "applied" || proposal.OriginalTagsJSON != `{"title":"old"}` || tx.State != "committed" || tx.Reconciled {
		t.Fatalf("proposal=%#v tx=%#v", proposal, tx)
	}
	if err := repo.MarkReconciled(context.Background(), txID); err != nil {
		t.Fatal(err)
	}
	if values, _ := repo.ListCommittedUnreconciled(context.Background()); len(values) != 0 {
		t.Fatalf("reconciled transaction still queued: %#v", values)
	}
}

func TestWritebackRepositoryRequiresBackupForExistingTarget(t *testing.T) {
	database, err := OpenPoisqDatabase(filepath.Join(t.TempDir(), "poisq.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	proposalID, _ := NewStagingRepository(database).CreateProposal(StagingChangeRecord{TargetID: "t", ChangeType: "manual"})
	err = NewWritebackRepository(database).CreatePrepared(context.Background(), WritebackTransaction{
		TransactionID: strings.Repeat("b", 32), ProposalID: proposalID, Kind: "tags", TargetPath: "/music/t.mp3", OriginalExists: true,
	})
	if err == nil {
		t.Fatal("existing target without verified backup accepted")
	}
}
