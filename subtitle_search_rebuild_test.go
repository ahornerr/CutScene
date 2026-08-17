package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type rebuildRecorderDB struct {
	state rebuildRecorderState
	tx    *rebuildRecorderTx
}

type rebuildRecorderState struct {
	columns        []subtitleVectorColumn
	contract       *persistedEmbeddingContract
	rows           map[string]int
	indexesPresent bool
}

type rebuildRecorderTx struct {
	pgx.Tx
	db         *rebuildRecorderDB
	snapshot   rebuildRecorderState
	statements []string
	committed  bool
	rolledBack bool
	failOn     string
}

func cloneRebuildState(state rebuildRecorderState) rebuildRecorderState {
	copyState := state
	copyState.columns = append([]subtitleVectorColumn(nil), state.columns...)
	copyState.rows = make(map[string]int, len(state.rows))
	for key, value := range state.rows {
		copyState.rows[key] = value
	}
	if state.contract != nil {
		copyState.contract = &persistedEmbeddingContract{embeddingContract: state.contract.embeddingContract, Generation: state.contract.Generation, State: state.contract.State}
	}
	return copyState
}

func (db *rebuildRecorderDB) Begin(context.Context) (pgx.Tx, error) {
	db.tx = &rebuildRecorderTx{db: db, snapshot: cloneRebuildState(db.state)}
	return db.tx, nil
}

func (tx *rebuildRecorderTx) Exec(_ context.Context, statement string, args ...any) (pgconn.CommandTag, error) {
	tx.statements = append(tx.statements, statement)
	if tx.failOn != "" && strings.Contains(statement, tx.failOn) {
		return pgconn.CommandTag{}, errors.New("injected rebuild failure")
	}
	switch {
	case strings.Contains(statement, "TRUNCATE TABLE"):
		for table := range tx.db.state.rows {
			tx.db.state.rows[table] = 0
		}
		tx.db.state.contract = nil
	case strings.HasPrefix(statement, "DROP INDEX"):
		tx.db.state.indexesPresent = false
	case strings.HasPrefix(statement, "ALTER TABLE subtitle_chunks"):
		tx.db.state.columns[0].Typmod = int32(argsDimension(statement))
	case strings.HasPrefix(statement, "ALTER TABLE subtitle_shared_chunks"):
		tx.db.state.columns[1].Typmod = int32(argsDimension(statement))
	case strings.HasPrefix(statement, "CREATE INDEX"):
		tx.db.state.indexesPresent = true
	case strings.HasPrefix(statement, "INSERT INTO subtitle_embedding_contract"):
		tx.db.state.contract = &persistedEmbeddingContract{
			embeddingContract: embeddingContract{
				Hash: args[0].(string), Provider: args[1].(string), Model: args[2].(string), Revision: args[3].(string),
				Dimensions: args[4].(int), Profile: args[5].(string), TemplateVersion: args[6].(string), IndexVersion: args[7].(string),
			},
			Generation: args[8].(int64), State: "ready",
		}
	}
	return pgconn.CommandTag{}, nil
}

func argsDimension(statement string) int {
	start := strings.Index(statement, "vector(") + len("vector(")
	end := strings.Index(statement[start:], ")") + start
	var dimension int
	_, _ = fmt.Sscanf(statement[start:end], "%d", &dimension)
	return dimension
}

func (tx *rebuildRecorderTx) Query(_ context.Context, _ string, _ ...any) (pgx.Rows, error) {
	return &rebuildRecorderRows{columns: tx.db.state.columns}, nil
}

func (tx *rebuildRecorderTx) QueryRow(_ context.Context, _ string, _ ...any) pgx.Row {
	return &rebuildRecorderRow{contract: tx.db.state.contract}
}

func (tx *rebuildRecorderTx) Commit(context.Context) error {
	tx.committed = true
	return nil
}

func (tx *rebuildRecorderTx) Rollback(context.Context) error {
	if tx.committed {
		return nil
	}
	tx.db.state = cloneRebuildState(tx.snapshot)
	tx.rolledBack = true
	return nil
}

type rebuildRecorderRow struct{ contract *persistedEmbeddingContract }

func (row *rebuildRecorderRow) Scan(dest ...any) error {
	if row.contract == nil {
		return pgx.ErrNoRows
	}
	values := []any{row.contract.Hash, row.contract.Provider, row.contract.Model, row.contract.Revision, row.contract.Dimensions, row.contract.Profile, row.contract.TemplateVersion, row.contract.IndexVersion, row.contract.Generation, row.contract.State}
	for i, value := range values {
		switch target := dest[i].(type) {
		case *string:
			*target = value.(string)
		case *int:
			*target = value.(int)
		case *int64:
			*target = value.(int64)
		}
	}
	return nil
}

type rebuildRecorderRows struct {
	columns []subtitleVectorColumn
	index   int
}

func (rows *rebuildRecorderRows) Close()                                       {}
func (rows *rebuildRecorderRows) Err() error                                   { return nil }
func (rows *rebuildRecorderRows) CommandTag() pgconn.CommandTag                { return pgconn.CommandTag{} }
func (rows *rebuildRecorderRows) FieldDescriptions() []pgconn.FieldDescription { return nil }
func (rows *rebuildRecorderRows) Next() bool {
	if rows.index >= len(rows.columns) {
		return false
	}
	rows.index++
	return true
}
func (rows *rebuildRecorderRows) Scan(dest ...any) error {
	column := rows.columns[rows.index-1]
	values := []any{column.Table, column.HasEmbedding, column.Typmod, column.DataType}
	for i, value := range values {
		switch target := dest[i].(type) {
		case *string:
			*target = value.(string)
		case *bool:
			*target = value.(bool)
		case *int32:
			*target = value.(int32)
		}
	}
	return nil
}
func (rows *rebuildRecorderRows) Values() ([]any, error) { return nil, nil }
func (rows *rebuildRecorderRows) RawValues() [][]byte    { return nil }
func (rows *rebuildRecorderRows) Conn() *pgx.Conn        { return nil }

func testEmbeddingContract(model string, dimensions int) embeddingContract {
	profile := embeddingProfileQwen
	if model == openAIEmbeddingModel {
		profile = embeddingProfileBGE
	}
	contract, _ := newEmbeddingContract(SemanticSearchConfig{EmbeddingsProvider: embeddingsProviderOpenAI, EmbeddingsModel: model, EmbeddingsDimensions: dimensions, EmbeddingsProfile: profile})
	return contract
}

func TestRebuildEmbeddingCorpusResetsPersistsAndUsesAdvisoryLock(t *testing.T) {
	old := testEmbeddingContract(openAIEmbeddingModel, 768)
	target := testEmbeddingContract(qwenEmbeddingModel, 1024)
	db := &rebuildRecorderDB{state: rebuildRecorderState{
		columns:  []subtitleVectorColumn{{Table: "subtitle_chunks", TableExists: true, HasEmbedding: true, Typmod: 768, DataType: "public.vector"}, {Table: "subtitle_shared_chunks", TableExists: true, HasEmbedding: true, Typmod: 768, DataType: "public.vector"}},
		contract: &persistedEmbeddingContract{embeddingContract: old, Generation: 4, State: "ready"},
		rows:     map[string]int{"subtitle_chunks": 4, "subtitle_shared_chunks": 5, "subtitle_index_sources": 2, "subtitle_shared_sources": 3, "subtitle_shared_sections": 1, "subtitle_index_jobs": 1}, indexesPresent: true,
	}}
	if err := rebuildEmbeddingCorpus(context.Background(), db, target); err != nil {
		t.Fatal(err)
	}
	if !db.tx.committed || db.tx.rolledBack || !db.state.indexesPresent || db.state.contract == nil || db.state.contract.Generation != 5 || db.state.contract.State != "ready" || !embeddingContractsMatch(target, db.state.contract) {
		t.Fatalf("rebuild state = %+v tx=%+v", db.state, db.tx)
	}
	for table, count := range db.state.rows {
		if count != 0 {
			t.Fatalf("%s retained %d rows", table, count)
		}
	}
	if db.state.columns[0].Typmod != 1024 || db.state.columns[1].Typmod != 1024 {
		t.Fatalf("vector dimensions = %+v", db.state.columns)
	}
	if !containsRebuildStatement(db.tx.statements, "pg_advisory_xact_lock") {
		t.Fatal("rebuild did not invoke advisory lock")
	}
}

func containsRebuildStatement(statements []string, fragment string) bool {
	for _, statement := range statements {
		if strings.Contains(statement, fragment) {
			return true
		}
	}
	return false
}

func TestRebuildEmbeddingCorpusMatchingContractIsIdempotent(t *testing.T) {
	contract := testEmbeddingContract(qwenEmbeddingModel, 1024)
	db := &rebuildRecorderDB{state: rebuildRecorderState{
		columns:  []subtitleVectorColumn{{Table: "subtitle_chunks", TableExists: true, HasEmbedding: true, Typmod: 1024, DataType: "vector"}, {Table: "subtitle_shared_chunks", TableExists: true, HasEmbedding: true, Typmod: 1024, DataType: "vector"}},
		contract: &persistedEmbeddingContract{embeddingContract: contract, Generation: 7, State: "ready"},
		rows:     map[string]int{"subtitle_chunks": 4, "subtitle_shared_chunks": 5}, indexesPresent: true,
	}}
	if err := rebuildEmbeddingCorpus(context.Background(), db, contract); err != nil {
		t.Fatal(err)
	}
	if !db.tx.committed || containsRebuildStatement(db.tx.statements, "TRUNCATE TABLE") || db.state.rows["subtitle_chunks"] != 4 || db.state.contract.Generation != 7 {
		t.Fatalf("matching rebuild was not idempotent: %+v tx=%+v", db.state, db.tx)
	}
}

func TestRebuildEmbeddingCorpusRollsBackInjectedFailure(t *testing.T) {
	old := testEmbeddingContract(openAIEmbeddingModel, 768)
	target := testEmbeddingContract(qwenEmbeddingModel, 1024)
	db := &rebuildRecorderDB{state: rebuildRecorderState{
		columns:  []subtitleVectorColumn{{Table: "subtitle_chunks", TableExists: true, HasEmbedding: true, Typmod: 768, DataType: "vector"}, {Table: "subtitle_shared_chunks", TableExists: true, HasEmbedding: true, Typmod: 768, DataType: "vector"}},
		contract: &persistedEmbeddingContract{embeddingContract: old, Generation: 2, State: "ready"},
		rows:     map[string]int{"subtitle_chunks": 4, "subtitle_shared_chunks": 5}, indexesPresent: true,
	}}
	// The recorder injects the failure after the reset and first index creation.
	failingDB := &rebuildFailingDB{inner: db, failOn: "CREATE INDEX subtitle_shared_chunks"}
	if err := rebuildEmbeddingCorpus(context.Background(), failingDB, target); err == nil {
		t.Fatal("injected rebuild failure unexpectedly succeeded")
	}
	if db.state.rows["subtitle_chunks"] != 4 || db.state.rows["subtitle_shared_chunks"] != 5 || db.state.columns[0].Typmod != 768 || db.state.contract.Generation != 2 || !db.tx.rolledBack {
		t.Fatalf("rollback did not restore prior state: %+v tx=%+v", db.state, db.tx)
	}
}

type rebuildFailingDB struct {
	inner  *rebuildRecorderDB
	failOn string
}

func (db *rebuildFailingDB) Begin(ctx context.Context) (pgx.Tx, error) {
	tx, err := db.inner.Begin(ctx)
	if err == nil {
		db.inner.tx.failOn = db.failOn
	}
	return tx, err
}

func TestBootstrapLegacyEmbeddingContractPreservesRows(t *testing.T) {
	contract := testEmbeddingContract(openAIEmbeddingModel, 768)
	db := &rebuildRecorderDB{state: rebuildRecorderState{
		columns: []subtitleVectorColumn{{Table: "subtitle_chunks", TableExists: true, HasEmbedding: true, Typmod: 768, DataType: "public.vector"}, {Table: "subtitle_shared_chunks", TableExists: true, HasEmbedding: true, Typmod: 768, DataType: "public.vector"}},
		rows:    map[string]int{"subtitle_chunks": 4, "subtitle_shared_chunks": 5}, indexesPresent: true,
	}}
	if err := bootstrapLegacyEmbeddingContract(context.Background(), db, contract); err != nil {
		t.Fatal(err)
	}
	if db.state.contract == nil || !embeddingContractsMatch(contract, db.state.contract) || db.state.rows["subtitle_chunks"] != 4 || db.state.rows["subtitle_shared_chunks"] != 5 || !db.tx.committed {
		t.Fatalf("legacy bootstrap changed corpus: %+v tx=%+v", db.state, db.tx)
	}
}
