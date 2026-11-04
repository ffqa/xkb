// Package store is the SQLite persistence layer for specs and cards.
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// Statuses used by cards and specs.
const (
	StatusBacklog  = "backlog"
	StatusTodo     = "todo"
	StatusRunning  = "running"
	StatusReview   = "review"
	StatusDone     = "done"
	StatusBlocked  = "blocked"
	StatusArchived = "archived"
)

// Card is one atomic task.
type Card struct {
	ID                string     `json:"id"`
	SpecID            string     `json:"specId"`
	ProjectPath       string     `json:"projectPath"`
	XIssueProj        int64      `json:"xissueProj"`
	XIssueIssueID     int64      `json:"xissueIssueId"`
	XIssueRunID       int64      `json:"xissueRunId"`
	Title             string     `json:"title"`
	Priority          string     `json:"priority"`
	Size              string     `json:"size"`
	SecuritySensitive bool       `json:"securitySensitive"`
	Status            string     `json:"status"`
	DependsOn         []string   `json:"dependsOn"`
	Touches           []string   `json:"touches"`
	VerifyCmd         string     `json:"verifyCmd"`
	Goal              string     `json:"goal"`
	OutOfScope        string     `json:"outOfScope"`
	Acceptance        string     `json:"acceptance"`
	ExecAgent         string     `json:"execAgent"`
	ExecModel         string     `json:"execModel"`
	ReviewAgent       string     `json:"reviewAgent"`
	RetryCount        int        `json:"retryCount"`
	LastReviewedSHA   string     `json:"lastReviewedSha"`
	LastError         string     `json:"lastError"`
	WaitReason        string     `json:"waitReason"`
	WaitSince         *time.Time `json:"waitSince,omitempty"`
	CreatedAt         time.Time  `json:"createdAt"`
	StartedAt         *time.Time `json:"startedAt,omitempty"`
	FinishedAt        *time.Time `json:"finishedAt,omitempty"`
	ArchivedAt        *time.Time `json:"archivedAt,omitempty"`
}

// Spec is a requirements document that cards are split from.
type Spec struct {
	ID          string    `json:"id"`
	ProjectPath string    `json:"projectPath"`
	XIssueProj  int64     `json:"xissueProj"`
	Title       string    `json:"title"`
	Body        string    `json:"body"`
	Status      string    `json:"status"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

// ReviewRun is one verifier evidence record.
type ReviewRun struct {
	ID           int64     `json:"id"`
	CardID       string    `json:"cardId"`
	Round        int       `json:"round"`
	Stage        string    `json:"stage"`
	Reviewer     string    `json:"reviewer"`
	BaseSHA      string    `json:"baseSha"`
	HeadSHA      string    `json:"headSha"`
	Verdict      string    `json:"verdict"`
	FindingsJSON string    `json:"findingsJson"`
	CreatedAt    time.Time `json:"createdAt"`
}

// CardFilter narrows ListCards.
type CardFilter struct {
	Statuses    []string
	ProjectPath string
	SpecID      string
}

// Store owns the SQLite handle.
type Store struct {
	db *sql.DB
}

// Open opens (and migrates) the database at path.
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	for _, pragma := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA busy_timeout=5000",
		"PRAGMA foreign_keys=ON",
	} {
		if _, err := db.Exec(pragma); err != nil {
			db.Close()
			return nil, fmt.Errorf("%s: %w", pragma, err)
		}
	}
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close releases the database.
func (s *Store) Close() error { return s.db.Close() }

const schema = `
CREATE TABLE IF NOT EXISTS specs (
    id           TEXT PRIMARY KEY,
    project_path TEXT NOT NULL,
    xissue_proj  INTEGER NOT NULL DEFAULT 0,
    title        TEXT NOT NULL,
    body         TEXT NOT NULL,
    status       TEXT NOT NULL DEFAULT 'active',
    created_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE IF NOT EXISTS cards (
    id                 TEXT PRIMARY KEY,
    spec_id            TEXT NOT NULL DEFAULT '',
    project_path       TEXT NOT NULL,
    xissue_proj        INTEGER NOT NULL DEFAULT 0,
    xissue_issue_id    INTEGER NOT NULL DEFAULT 0,
    xissue_run_id      INTEGER NOT NULL DEFAULT 0,
    title              TEXT NOT NULL,
    priority           TEXT NOT NULL DEFAULT 'P2',
    size               TEXT NOT NULL DEFAULT 'small',
    security_sensitive INTEGER NOT NULL DEFAULT 0,
    status             TEXT NOT NULL DEFAULT 'backlog',
    depends_on_json    TEXT NOT NULL DEFAULT '[]',
    touches_json       TEXT NOT NULL DEFAULT '[]',
    verify_cmd         TEXT NOT NULL DEFAULT '',
    goal               TEXT NOT NULL DEFAULT '',
    out_of_scope       TEXT NOT NULL DEFAULT '',
    acceptance         TEXT NOT NULL DEFAULT '',
    exec_agent         TEXT NOT NULL DEFAULT '',
    exec_model         TEXT NOT NULL DEFAULT '',
    review_agent       TEXT NOT NULL DEFAULT '',
    retry_count        INTEGER NOT NULL DEFAULT 0,
    last_reviewed_sha  TEXT NOT NULL DEFAULT '',
    last_error         TEXT NOT NULL DEFAULT '',
    wait_reason        TEXT NOT NULL DEFAULT '',
    wait_since         DATETIME,
    created_at         DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    started_at         DATETIME,
    finished_at        DATETIME,
    archived_at        DATETIME
);
CREATE INDEX IF NOT EXISTS idx_cards_status ON cards(status);
CREATE INDEX IF NOT EXISTS idx_cards_spec ON cards(spec_id);
CREATE TABLE IF NOT EXISTS review_runs (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    card_id       TEXT NOT NULL,
    round         INTEGER NOT NULL,
    stage         TEXT NOT NULL,
    reviewer      TEXT NOT NULL,
    base_sha      TEXT NOT NULL,
    head_sha      TEXT NOT NULL,
    verdict       TEXT NOT NULL,
    findings_json TEXT NOT NULL,
    created_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_review_runs_card ON review_runs(card_id);
`

func (s *Store) migrate() error {
	if _, err := s.db.Exec(schema); err != nil {
		return err
	}
	// CREATE TABLE IF NOT EXISTS does not add columns to an existing database.
	// Keep runtime upgrades additive so old ~/.xkb/xkb.db files keep working.
	for _, col := range []struct{ name, ddl string }{
		{"wait_reason", "TEXT NOT NULL DEFAULT ''"},
		{"wait_since", "DATETIME"},
	} {
		exists, err := s.hasColumn("cards", col.name)
		if err != nil {
			return err
		}
		if exists {
			continue
		}
		if _, err := s.db.Exec("ALTER TABLE cards ADD COLUMN " + col.name + " " + col.ddl); err != nil {
			return fmt.Errorf("add cards.%s: %w", col.name, err)
		}
	}
	return nil
}

func (s *Store) hasColumn(table, column string) (bool, error) {
	rows, err := s.db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notNull, pk int
		var name, typ string
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &typ, &notNull, &dflt, &pk); err != nil {
			return false, err
		}
		if name == column {
			return true, nil
		}
	}
	return false, rows.Err()
}

func now() time.Time { return time.Now().UTC() }

func ts(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func parseTS(raw sql.NullString) *time.Time {
	if !raw.Valid || raw.String == "" {
		return nil
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, raw.String); err == nil {
			t = t.UTC()
			return &t
		}
	}
	return nil
}

func jsonList(v []string) string {
	if v == nil {
		v = []string{}
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func parseList(raw string) []string {
	out := []string{}
	_ = json.Unmarshal([]byte(raw), &out)
	if out == nil {
		out = []string{}
	}
	return out
}

// CreateSpec inserts a spec, generating the next spec-YYYYMMDD-NN id.
func (s *Store) CreateSpec(ctx context.Context, spec *Spec) (*Spec, error) {
	day := now().Format("20060102")
	prefix := "spec-" + day + "-"
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(CAST(SUBSTR(id, ?) AS INTEGER)), 0) + 1 FROM specs WHERE id LIKE ?`,
		len(prefix)+1, prefix+"%").Scan(&n)
	if err != nil {
		return nil, err
	}
	if spec.ID == "" {
		spec.ID = fmt.Sprintf("%s%02d", prefix, n)
	}
	if spec.CreatedAt.IsZero() {
		spec.CreatedAt = now()
	}
	spec.UpdatedAt = spec.CreatedAt
	if spec.Status == "" {
		spec.Status = "active"
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO specs(id, project_path, xissue_proj, title, body, status, created_at, updated_at)
		 VALUES(?,?,?,?,?,?,?,?)`,
		spec.ID, spec.ProjectPath, spec.XIssueProj, spec.Title, spec.Body, spec.Status,
		ts(spec.CreatedAt), ts(spec.UpdatedAt))
	if err != nil {
		return nil, err
	}
	return s.GetSpec(ctx, spec.ID)
}

const specCols = `id, project_path, xissue_proj, title, body, status, created_at, updated_at`

func scanSpec(sc interface{ Scan(...any) error }) (*Spec, error) {
	var sp Spec
	var created, updated string
	if err := sc.Scan(&sp.ID, &sp.ProjectPath, &sp.XIssueProj, &sp.Title, &sp.Body, &sp.Status, &created, &updated); err != nil {
		return nil, err
	}
	sp.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	sp.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
	return &sp, nil
}

// GetSpec loads one spec.
func (s *Store) GetSpec(ctx context.Context, id string) (*Spec, error) {
	sp, err := scanSpec(s.db.QueryRowContext(ctx, `SELECT `+specCols+` FROM specs WHERE id=?`, id))
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("spec %s not found", id)
	}
	return sp, err
}

// ListSpecs returns all specs, newest first.
func (s *Store) ListSpecs(ctx context.Context) ([]*Spec, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+specCols+` FROM specs ORDER BY created_at DESC, id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Spec{}
	for rows.Next() {
		sp, err := scanSpec(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sp)
	}
	return out, rows.Err()
}

// CreateCard inserts a card, generating the next xkb-NNN id.
func (s *Store) CreateCard(ctx context.Context, card *Card) (*Card, error) {
	if card.ID == "" {
		var n int
		err := s.db.QueryRowContext(ctx,
			`SELECT COALESCE(MAX(CAST(SUBSTR(id, 5) AS INTEGER)), 0) + 1 FROM cards WHERE id LIKE 'xkb-%'`).Scan(&n)
		if err != nil {
			return nil, err
		}
		card.ID = fmt.Sprintf("xkb-%03d", n)
	}
	if card.Status == "" {
		card.Status = StatusBacklog
	}
	if card.Priority == "" {
		card.Priority = "P2"
	}
	if card.Size == "" {
		card.Size = "small"
	}
	if card.CreatedAt.IsZero() {
		card.CreatedAt = now()
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO cards(id, spec_id, project_path, xissue_proj, xissue_issue_id, xissue_run_id,
		 title, priority, size, security_sensitive, status, depends_on_json, touches_json,
		 verify_cmd, goal, out_of_scope, acceptance, exec_agent, exec_model, review_agent,
		 retry_count, last_reviewed_sha, last_error, wait_reason, wait_since,
		 created_at, started_at, finished_at, archived_at)
		 VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		card.ID, card.SpecID, card.ProjectPath, card.XIssueProj, card.XIssueIssueID, card.XIssueRunID,
		card.Title, card.Priority, card.Size, boolInt(card.SecuritySensitive), card.Status,
		jsonList(card.DependsOn), jsonList(card.Touches), card.VerifyCmd, card.Goal,
		card.OutOfScope, card.Acceptance, card.ExecAgent, card.ExecModel, card.ReviewAgent,
		card.RetryCount, card.LastReviewedSHA, card.LastError, card.WaitReason, nullableTS(card.WaitSince),
		ts(card.CreatedAt), nullableTS(card.StartedAt), nullableTS(card.FinishedAt), nullableTS(card.ArchivedAt))
	if err != nil {
		return nil, err
	}
	return s.GetCard(ctx, card.ID)
}

func nullableTS(t *time.Time) any {
	if t == nil {
		return nil
	}
	return ts(*t)
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

const cardCols = `id, spec_id, project_path, xissue_proj, xissue_issue_id, xissue_run_id,
 title, priority, size, security_sensitive, status, depends_on_json, touches_json,
 verify_cmd, goal, out_of_scope, acceptance, exec_agent, exec_model, review_agent,
 retry_count, last_reviewed_sha, last_error, wait_reason, wait_since,
 created_at, started_at, finished_at, archived_at`

func scanCard(sc interface{ Scan(...any) error }) (*Card, error) {
	var c Card
	var sec int
	var depends, touches string
	var created string
	var started, finished, archived, waitSince sql.NullString
	err := sc.Scan(&c.ID, &c.SpecID, &c.ProjectPath, &c.XIssueProj, &c.XIssueIssueID, &c.XIssueRunID,
		&c.Title, &c.Priority, &c.Size, &sec, &c.Status, &depends, &touches,
		&c.VerifyCmd, &c.Goal, &c.OutOfScope, &c.Acceptance, &c.ExecAgent, &c.ExecModel, &c.ReviewAgent,
		&c.RetryCount, &c.LastReviewedSHA, &c.LastError, &c.WaitReason, &waitSince,
		&created, &started, &finished, &archived)
	if err != nil {
		return nil, err
	}
	c.SecuritySensitive = sec != 0
	c.DependsOn = parseList(depends)
	c.Touches = parseList(touches)
	c.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	c.StartedAt = parseTS(started)
	c.FinishedAt = parseTS(finished)
	c.ArchivedAt = parseTS(archived)
	c.WaitSince = parseTS(waitSince)
	return &c, nil
}

// GetCard loads one card.
func (s *Store) GetCard(ctx context.Context, id string) (*Card, error) {
	c, err := scanCard(s.db.QueryRowContext(ctx, `SELECT `+cardCols+` FROM cards WHERE id=?`, id))
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("card %s not found", id)
	}
	return c, err
}

// UpdateCard writes every mutable column.
func (s *Store) UpdateCard(ctx context.Context, card *Card) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE cards SET spec_id=?, project_path=?, xissue_proj=?, xissue_issue_id=?, xissue_run_id=?,
		 title=?, priority=?, size=?, security_sensitive=?, status=?, depends_on_json=?, touches_json=?,
		 verify_cmd=?, goal=?, out_of_scope=?, acceptance=?, exec_agent=?, exec_model=?, review_agent=?,
		 retry_count=?, last_reviewed_sha=?, last_error=?, wait_reason=?, wait_since=?,
		 started_at=?, finished_at=?, archived_at=?
		 WHERE id=?`,
		card.SpecID, card.ProjectPath, card.XIssueProj, card.XIssueIssueID, card.XIssueRunID,
		card.Title, card.Priority, card.Size, boolInt(card.SecuritySensitive), card.Status,
		jsonList(card.DependsOn), jsonList(card.Touches), card.VerifyCmd, card.Goal,
		card.OutOfScope, card.Acceptance, card.ExecAgent, card.ExecModel, card.ReviewAgent,
		card.RetryCount, card.LastReviewedSHA, card.LastError, card.WaitReason, nullableTS(card.WaitSince),
		nullableTS(card.StartedAt), nullableTS(card.FinishedAt), nullableTS(card.ArchivedAt), card.ID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("card %s not found", card.ID)
	}
	return nil
}

// DeleteCard permanently removes a card and its review evidence. It reports
// whether the card existed so callers can warn about unknown ids.
func (s *Store) DeleteCard(ctx context.Context, id string) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM review_runs WHERE card_id=?`, id); err != nil {
		return false, err
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM cards WHERE id=?`, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return n == 1, nil
}

// ListCards returns cards matching filter, ordered by priority then creation.
func (s *Store) ListCards(ctx context.Context, filter CardFilter) ([]*Card, error) {
	query := `SELECT ` + cardCols + ` FROM cards`
	var where []string
	var args []any
	if len(filter.Statuses) > 0 {
		ph := make([]string, len(filter.Statuses))
		for i, st := range filter.Statuses {
			ph[i] = "?"
			args = append(args, st)
		}
		where = append(where, "status IN ("+strings.Join(ph, ",")+")")
	}
	if filter.ProjectPath != "" {
		where = append(where, "project_path = ?")
		args = append(args, filter.ProjectPath)
	}
	if filter.SpecID != "" {
		where = append(where, "spec_id = ?")
		args = append(args, filter.SpecID)
	}
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	query += " ORDER BY priority ASC, created_at ASC, id ASC"
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Card{}
	for rows.Next() {
		c, err := scanCard(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ClaimTodo atomically moves a card from todo to running so that a TUI and a
// daemon scheduler cannot dispatch the same card twice.
func (s *Store) ClaimTodo(ctx context.Context, id string) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE cards SET status=?, started_at=?, wait_reason='', wait_since=NULL WHERE id=? AND status=?`,
		StatusRunning, ts(now()), id, StatusTodo)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n == 1, nil
}

// RecordReviewRun appends verifier evidence.
func (s *Store) RecordReviewRun(ctx context.Context, rr *ReviewRun) error {
	if rr.CreatedAt.IsZero() {
		rr.CreatedAt = now()
	}
	if rr.FindingsJSON == "" {
		rr.FindingsJSON = "[]"
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO review_runs(card_id, round, stage, reviewer, base_sha, head_sha, verdict, findings_json, created_at)
		 VALUES(?,?,?,?,?,?,?,?,?)`,
		rr.CardID, rr.Round, rr.Stage, rr.Reviewer, rr.BaseSHA, rr.HeadSHA, rr.Verdict, rr.FindingsJSON, ts(rr.CreatedAt))
	if err != nil {
		return err
	}
	rr.ID, _ = res.LastInsertId()
	return nil
}

// ListReviewRuns returns evidence for a card in chronological order.
func (s *Store) ListReviewRuns(ctx context.Context, cardID string) ([]*ReviewRun, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, card_id, round, stage, reviewer, base_sha, head_sha, verdict, findings_json, created_at
		 FROM review_runs WHERE card_id=? ORDER BY id ASC`, cardID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*ReviewRun{}
	for rows.Next() {
		var rr ReviewRun
		var created string
		if err := rows.Scan(&rr.ID, &rr.CardID, &rr.Round, &rr.Stage, &rr.Reviewer, &rr.BaseSHA,
			&rr.HeadSHA, &rr.Verdict, &rr.FindingsJSON, &created); err != nil {
			return nil, err
		}
		rr.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		out = append(out, &rr)
	}
	return out, rows.Err()
}
