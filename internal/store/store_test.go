package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestSpecAndCardLifecycle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "xkb.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()

	sp, err := st.CreateSpec(ctx, &Spec{ProjectPath: "/tmp/proj", Title: "spec", Body: "# body"})
	if err != nil {
		t.Fatal(err)
	}
	if sp.ID == "" || sp.Status != "active" {
		t.Fatalf("unexpected spec: %+v", sp)
	}
	if _, err := st.GetSpec(ctx, sp.ID); err != nil {
		t.Fatal(err)
	}

	c1, err := st.CreateCard(ctx, &Card{
		SpecID: sp.ID, ProjectPath: "/tmp/proj", Title: "first", Priority: "P1",
		Touches: []string{"internal/a/"}, DependsOn: []string{"xkb-000"},
		VerifyCmd: "go test ./...", Goal: "g", OutOfScope: "o", Acceptance: "a",
		SecuritySensitive: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	c2, err := st.CreateCard(ctx, &Card{ProjectPath: "/tmp/proj", Title: "second"})
	if err != nil {
		t.Fatal(err)
	}
	if c1.ID != "xkb-001" || c2.ID != "xkb-002" {
		t.Fatalf("ids: %s %s", c1.ID, c2.ID)
	}
	if !c1.SecuritySensitive || len(c1.Touches) != 1 || c1.DependsOn[0] != "xkb-000" {
		t.Fatalf("roundtrip mismatch: %+v", c1)
	}

	c1.Status = StatusTodo
	c1.Priority = "P0"
	c1.RetryCount = 1
	waitSince := time.Now().UTC().Truncate(time.Second)
	c1.WaitReason = "等待依赖完成: xkb-000"
	c1.WaitSince = &waitSince
	if err := st.UpdateCard(ctx, c1); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetCard(ctx, c1.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusTodo || got.Priority != "P0" || got.RetryCount != 1 {
		t.Fatalf("update mismatch: %+v", got)
	}
	if got.WaitReason != c1.WaitReason || got.WaitSince == nil || !got.WaitSince.Equal(waitSince) {
		t.Fatalf("wait fields mismatch: %+v", got)
	}

	cards, err := st.ListCards(ctx, CardFilter{Statuses: []string{StatusTodo}})
	if err != nil {
		t.Fatal(err)
	}
	if len(cards) != 1 || cards[0].ID != c1.ID {
		t.Fatalf("filter mismatch: %+v", cards)
	}
	cards, err = st.ListCards(ctx, CardFilter{SpecID: sp.ID})
	if err != nil || len(cards) != 1 {
		t.Fatalf("spec filter: %v %+v", err, cards)
	}

	claimed, err := st.ClaimTodo(ctx, c1.ID)
	if err != nil || !claimed {
		t.Fatalf("first claim: %v %v", claimed, err)
	}
	claimed, err = st.ClaimTodo(ctx, c1.ID)
	if err != nil || claimed {
		t.Fatalf("second claim must fail: %v %v", claimed, err)
	}
	claimed, err = st.ClaimTodo(ctx, c2.ID)
	if err != nil || claimed {
		t.Fatalf("backlog card must not be claimable: %v %v", claimed, err)
	}

	if err := st.RecordReviewRun(ctx, &ReviewRun{
		CardID: c1.ID, Round: 1, Stage: "gate_cmd", Reviewer: "gate",
		BaseSHA: "base", HeadSHA: "head", Verdict: "PASS", FindingsJSON: "[]",
	}); err != nil {
		t.Fatal(err)
	}
	runs, err := st.ListReviewRuns(ctx, c1.ID)
	if err != nil || len(runs) != 1 {
		t.Fatalf("reviews: %v %+v", err, runs)
	}

	st.Close()
	st2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	if _, err := st2.GetCard(ctx, c1.ID); err != nil {
		t.Fatalf("persist: %v", err)
	}
}

func TestMigrationAddsWaitColumns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "xkb.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, col := range []string{"wait_reason", "wait_since"} {
		if _, err := st.db.Exec("ALTER TABLE cards DROP COLUMN " + col); err != nil {
			t.Fatalf("drop %s: %v", col, err)
		}
	}
	st.Close()

	st2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	for _, col := range []string{"wait_reason", "wait_since"} {
		ok, err := st2.hasColumn("cards", col)
		if err != nil || !ok {
			t.Fatalf("%s not migrated: ok=%v err=%v", col, ok, err)
		}
	}
}

func TestDeleteCardRemovesReviewRuns(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "xkb.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	c, err := st.CreateCard(ctx, &Card{ProjectPath: "/tmp/proj", Title: "c"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.RecordReviewRun(ctx, &ReviewRun{CardID: c.ID, Round: 1, Stage: "gate_cmd", Reviewer: "gate", Verdict: "PASS"}); err != nil {
		t.Fatal(err)
	}
	ok, err := st.DeleteCard(ctx, c.ID)
	if err != nil || !ok {
		t.Fatalf("delete: %v %v", ok, err)
	}
	if _, err := st.GetCard(ctx, c.ID); err == nil {
		t.Fatal("card still present")
	}
	if runs, err := st.ListReviewRuns(ctx, c.ID); err != nil || len(runs) != 0 {
		t.Fatalf("review runs: %v %+v", err, runs)
	}
	if ok, err := st.DeleteCard(ctx, c.ID); err != nil || ok {
		t.Fatalf("second delete: %v %v", ok, err)
	}
}
