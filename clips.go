package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"
	_ "github.com/mattn/go-sqlite3"
)

const defaultDurableStorageRoot = "./data"

var errClipNotFound = errors.New("clip not found")

func setCapabilityResponseHeaders(ctx fiber.Ctx) {
	ctx.Set("Cache-Control", "no-store")
	ctx.Set("Referrer-Policy", "no-referrer")
}

// Clip is the durable representation of a completed render. FilePath and the
// raw share token are intentionally not serialized; callers receive URLs
// rather than storage implementation details or credentials.
type Clip struct {
	ID                 string    `json:"id"`
	OwnerUUID          string    `json:"-"`
	CreatorDisplayName string    `json:"creatorDisplayName,omitempty"`
	MediaKind          string    `json:"mediaKind,omitempty"`
	MovieTitle         string    `json:"movieTitle,omitempty"`
	MovieYear          *int      `json:"movieYear,omitempty"`
	ShowTitle          string    `json:"showTitle,omitempty"`
	SeasonNumber       *int      `json:"seasonNumber,omitempty"`
	EpisodeNumber      *int      `json:"episodeNumber,omitempty"`
	EpisodeTitle       string    `json:"episodeTitle,omitempty"`
	Title              string    `json:"title"`
	RatingKey          string    `json:"ratingKey"`
	MediaID            int64     `json:"mediaId"`
	FromMs             int64     `json:"fromMs"`
	ToMs               int64     `json:"toMs"`
	CreatedAt          time.Time `json:"createdAt"`
	FilePath           string    `json:"-"`
	ArtworkPath        string    `json:"-"`
	ThumbnailMIME      string    `json:"-"`
	ShareToken         string    `json:"-"`
	tokenCiphertext    []byte
	tokenHash          string
}

type clipStore struct {
	root      string
	files     string
	db        *sql.DB
	tokenAEAD cipher.AEAD
}

func durableStorageRoot(config Config) string {
	if strings.TrimSpace(config.Storage.Root) != "" {
		return config.Storage.Root
	}
	return defaultDurableStorageRoot
}

const clipTokenKeyFile = "clip-token.key"

func loadOrCreateTokenKey(root string, requireExisting bool) ([]byte, error) {
	path := filepath.Join(root, clipTokenKeyFile)
	readKey := func() ([]byte, error) {
		info, err := os.Lstat(path)
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
			return nil, fmt.Errorf("clip token key must be a regular 0600 file")
		}
		key, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read clip token key: %w", err)
		}
		if len(key) != 32 {
			return nil, fmt.Errorf("clip token key has invalid length")
		}
		return key, nil
	}

	if key, err := readKey(); err == nil {
		return key, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	} else if requireExisting {
		return nil, fmt.Errorf("clip token key is required by existing encrypted clip records")
	}

	var key [32]byte
	if _, err := io.ReadFull(rand.Reader, key[:]); err != nil {
		return nil, fmt.Errorf("generate clip token key: %w", err)
	}
	tmp := filepath.Join(root, "."+clipTokenKeyFile+"."+uuid.NewString()+".tmp")
	file, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return readKey()
		}
		return nil, fmt.Errorf("create clip token key: %w", err)
	}
	writeErr := error(nil)
	if _, writeErr = file.Write(key[:]); writeErr == nil {
		writeErr = file.Sync()
	}
	if closeErr := file.Close(); writeErr == nil {
		writeErr = closeErr
	}
	if writeErr != nil {
		_ = os.Remove(tmp)
		return nil, fmt.Errorf("write clip token key: %w", writeErr)
	}
	// A hard link gives create-if-absent semantics for the final name: a
	// concurrent starter cannot replace an already durable key.
	if err := os.Link(tmp, path); err != nil {
		_ = os.Remove(tmp)
		if errors.Is(err, os.ErrExist) {
			return readKey()
		}
		return nil, fmt.Errorf("install clip token key: %w", err)
	}
	_ = os.Remove(tmp)
	if err := syncDirectory(root); err != nil {
		return nil, fmt.Errorf("sync clip token key: %w", err)
	}
	return key[:], nil
}

type clipSchemaKind int

const (
	clipSchemaNone clipSchemaKind = iota
	clipSchemaPhase1HashOnly
	clipSchemaPhase1Raw
	clipSchemaPhase2Encrypted
	clipSchemaCurrent
	clipSchemaPresentation
)

type clipSchema struct {
	kind   clipSchemaKind
	rows   int64
	exists bool
}

var clipBaseColumns = map[string]bool{
	"id": true, "owner_uuid": true, "title": true, "rating_key": true,
	"media_id": true, "from_ms": true, "to_ms": true, "created_at": true,
	"share_token_hash": true,
}

var clipPresentationColumns = map[string]bool{
	"creator_display_name": true, "media_kind": true, "movie_title": true,
	"movie_year": true, "show_title": true, "season_number": true,
	"episode_number": true, "episode_title": true, "thumbnail_mime": true,
}

func resolveClipDatabasePath(root, configured string) (string, error) {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve durable storage root: %w", err)
	}
	if strings.TrimSpace(configured) == "" {
		configured = "clips.sqlite3"
	}
	if filepath.IsAbs(configured) {
		return "", errors.New("storage.database must be relative to storage.root")
	}
	databasePath := filepath.Clean(filepath.Join(rootAbs, configured))
	rel, err := filepath.Rel(rootAbs, databasePath)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("storage.database must remain below storage.root")
	}
	canonicalRoot, err := filepath.EvalSymlinks(rootAbs)
	if err != nil {
		return "", fmt.Errorf("resolve durable storage root: %w", err)
	}
	current := rootAbs
	parts := strings.Split(rel, string(filepath.Separator))
	for index, part := range parts {
		current = filepath.Join(current, part)
		info, statErr := os.Lstat(current)
		if errors.Is(statErr, os.ErrNotExist) {
			break
		}
		if statErr != nil {
			return "", fmt.Errorf("inspect storage.database path: %w", statErr)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("storage.database cannot contain symlinked path components")
		}
		if index < len(parts)-1 && !info.IsDir() {
			return "", errors.New("storage.database has a non-directory path component")
		}
	}
	existing := databasePath
	for {
		if _, statErr := os.Lstat(existing); statErr == nil {
			canonicalExisting, evalErr := filepath.EvalSymlinks(existing)
			if evalErr != nil {
				return "", fmt.Errorf("resolve storage.database target: %w", evalErr)
			}
			canonicalRel, relErr := filepath.Rel(canonicalRoot, canonicalExisting)
			if relErr != nil || canonicalRel == ".." || strings.HasPrefix(canonicalRel, ".."+string(filepath.Separator)) {
				return "", errors.New("storage.database target escapes storage.root")
			}
			break
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return "", fmt.Errorf("inspect storage.database target: %w", statErr)
		}
		next := filepath.Dir(existing)
		if next == existing {
			break
		}
		existing = next
	}
	return databasePath, nil
}

func hasDurableClipEntries(files string) (bool, error) {
	entries, err := os.ReadDir(files)
	if err != nil {
		return false, err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			return true, nil
		}
	}
	return false, nil
}

func clipPathForFiles(files, id string) (string, error) {
	if id == "" || filepath.Base(id) != id || id == "." || id == ".." {
		return "", errors.New("clip id is invalid")
	}
	path := filepath.Join(files, id+".mp4")
	rel, err := filepath.Rel(files, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("clip path escapes durable storage")
	}
	return path, nil
}

func artworkPathForFiles(files, id string) (string, error) {
	path, err := clipPathForFiles(files, id)
	if err != nil {
		return "", err
	}
	return strings.TrimSuffix(path, ".mp4") + ".thumb", nil
}

func preflightClipFiles(db *sql.DB, files string, hasThumbnails bool) error {
	query := "SELECT id"
	if hasThumbnails {
		query += ", thumbnail_mime"
	}
	query += " FROM clips"
	rows, err := db.Query(query)
	if err != nil {
		return fmt.Errorf("preflight clip metadata: %w", err)
	}
	expected := make(map[string]string)
	for rows.Next() {
		var id string
		var thumbnailMIME sql.NullString
		args := []any{&id}
		if hasThumbnails {
			args = append(args, &thumbnailMIME)
		}
		if err := rows.Scan(args...); err != nil {
			rows.Close()
			return fmt.Errorf("preflight clip metadata: %w", err)
		}
		path, err := clipPathForFiles(files, id)
		if err != nil {
			rows.Close()
			return fmt.Errorf("preflight clip %s: %w", id, err)
		}
		expected[path] = id
		if hasThumbnails && thumbnailMIME.Valid && thumbnailMIME.String != "" {
			artworkPath, err := artworkPathForFiles(files, id)
			if err != nil {
				rows.Close()
				return fmt.Errorf("preflight artwork for clip %s: %w", id, err)
			}
			expected[artworkPath] = id
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("preflight clip metadata: %w", err)
	}
	rows.Close()
	entries, err := os.ReadDir(files)
	if err != nil {
		return fmt.Errorf("preflight durable clip files: %w", err)
	}
	actual := make(map[string]bool)
	for _, entry := range entries {
		if entry.IsDir() {
			return fmt.Errorf("preflight found unexpected directory %q in clip storage", entry.Name())
		}
		path := filepath.Join(files, entry.Name())
		ext := strings.ToLower(filepath.Ext(entry.Name()))
		if ext != ".mp4" && !(hasThumbnails && ext == ".thumb") {
			return fmt.Errorf("preflight found unexpected clip file %q", entry.Name())
		}
		info, err := os.Lstat(path)
		if err != nil {
			return fmt.Errorf("preflight clip file %q: %w", entry.Name(), err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() == 0 {
			return fmt.Errorf("preflight clip file %q is not a regular nonempty file", entry.Name())
		}
		actual[path] = true
	}
	if len(expected) != len(actual) {
		return errors.New("preflight database and clip files do not correspond exactly")
	}
	for path := range expected {
		if !actual[path] {
			return fmt.Errorf("preflight database references missing clip bytes %q", filepath.Base(path))
		}
	}
	for path := range actual {
		if _, ok := expected[path]; !ok {
			return fmt.Errorf("preflight found unreferenced clip bytes %q", filepath.Base(path))
		}
	}
	return nil
}

func inspectClipSchema(db *sql.DB) (clipSchema, error) {
	var tableName string
	err := db.QueryRow("SELECT name FROM sqlite_master WHERE type = 'table' AND name = 'clips'").Scan(&tableName)
	if errors.Is(err, sql.ErrNoRows) {
		return clipSchema{kind: clipSchemaNone}, nil
	}
	if err != nil {
		return clipSchema{}, fmt.Errorf("inspect clip schema: %w", err)
	}
	rows, err := db.Query("PRAGMA table_info(clips)")
	if err != nil {
		return clipSchema{}, fmt.Errorf("inspect clip columns: %w", err)
	}
	columns := make(map[string]bool)
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			rows.Close()
			return clipSchema{}, fmt.Errorf("inspect clip columns: %w", err)
		}
		columns[name] = true
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return clipSchema{}, fmt.Errorf("inspect clip columns: %w", err)
	}
	rows.Close()
	for column := range clipBaseColumns {
		if !columns[column] {
			return clipSchema{}, fmt.Errorf("clips table is missing required column %q", column)
		}
	}
	kind := clipSchemaKind(0)
	hasPath := columns["file_path"]
	hasRaw := columns["share_token"]
	hasCipher := columns["share_token_ciphertext"]
	allowed := make(map[string]bool)
	for column := range clipBaseColumns {
		allowed[column] = true
	}
	presentationCount := 0
	for column := range clipPresentationColumns {
		if columns[column] {
			presentationCount++
		}
		allowed[column] = true
	}
	if hasPath {
		allowed["file_path"] = true
	}
	if hasRaw {
		allowed["share_token"] = true
	}
	if hasCipher {
		allowed["share_token_ciphertext"] = true
	}
	for column := range columns {
		if !allowed[column] {
			return clipSchema{}, fmt.Errorf("clips table has unknown column %q", column)
		}
	}
	switch {
	case hasCipher && !hasPath && !hasRaw && presentationCount == len(clipPresentationColumns):
		kind = clipSchemaPresentation
	case hasCipher && !hasPath && !hasRaw && presentationCount == 0:
		kind = clipSchemaCurrent
	case hasCipher && hasPath && !hasRaw && presentationCount == 0:
		kind = clipSchemaPhase2Encrypted
	case !hasCipher && hasPath && hasRaw && presentationCount == 0:
		kind = clipSchemaPhase1Raw
	case !hasCipher && hasPath && !hasRaw && presentationCount == 0:
		kind = clipSchemaPhase1HashOnly
	default:
		return clipSchema{}, errors.New("clips table has an unsupported partial schema")
	}
	var count int64
	if err := db.QueryRow("SELECT count(*) FROM clips").Scan(&count); err != nil {
		return clipSchema{}, fmt.Errorf("count clips: %w", err)
	}
	return clipSchema{kind: kind, rows: count, exists: true}, nil
}

func newClipStore(root, databasePath string) (*clipStore, error) {
	if strings.TrimSpace(root) == "" {
		root = defaultDurableStorageRoot
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, fmt.Errorf("create durable storage root: %w", err)
	}
	if err := os.Chmod(root, 0700); err != nil {
		return nil, fmt.Errorf("secure durable storage root: %w", err)
	}
	files := filepath.Join(root, "clips")
	if err := os.MkdirAll(files, 0700); err != nil {
		return nil, fmt.Errorf("create durable clip storage: %w", err)
	}
	if err := os.Chmod(files, 0700); err != nil {
		return nil, fmt.Errorf("secure durable clip storage: %w", err)
	}
	filesInfo, err := os.Lstat(files)
	if err != nil || filesInfo.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("durable clip storage cannot be a symlink")
	}
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, fmt.Errorf("resolve durable storage root: %w", err)
	}
	canonicalFiles, err := filepath.EvalSymlinks(files)
	if err != nil {
		return nil, fmt.Errorf("resolve durable clip storage: %w", err)
	}
	filesRel, err := filepath.Rel(canonicalRoot, canonicalFiles)
	if err != nil || filesRel == ".." || strings.HasPrefix(filesRel, ".."+string(filepath.Separator)) {
		return nil, errors.New("durable clip storage escapes storage.root")
	}
	databasePath, err = resolveClipDatabasePath(root, databasePath)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(databasePath), 0700); err != nil {
		return nil, fmt.Errorf("create clip database directory: %w", err)
	}
	databaseInfo, databaseErr := os.Lstat(databasePath)
	databaseExists := databaseErr == nil
	if databaseErr != nil && !errors.Is(databaseErr, os.ErrNotExist) {
		return nil, fmt.Errorf("inspect clip database: %w", databaseErr)
	}
	hasMP4s, err := hasDurableClipEntries(files)
	if err != nil {
		return nil, fmt.Errorf("inspect durable clip files: %w", err)
	}
	if hasMP4s && (!databaseExists || databaseInfo.Size() == 0) {
		return nil, errors.New("durable clip files exist but the selected database is missing or empty; refusing recovery cleanup")
	}

	var schema clipSchema
	var db *sql.DB
	if databaseExists {
		db, err = sql.Open("sqlite3", databasePath)
		if err != nil {
			return nil, fmt.Errorf("open clip database: %w", err)
		}
		db.SetMaxOpenConns(1)
		schema, err = inspectClipSchema(db)
		if err != nil {
			_ = db.Close()
			return nil, err
		}
		if hasMP4s && (!schema.exists || schema.rows == 0) {
			_ = db.Close()
			return nil, errors.New("durable clip files exist but the selected database has no clip records; refusing recovery cleanup")
		}
		if !schema.exists {
			_ = db.Close()
			return nil, errors.New("existing clip database has no recognized clips table")
		}
		if err := preflightClipFiles(db, files, schema.kind == clipSchemaPresentation); err != nil {
			_ = db.Close()
			return nil, err
		}
	} else {
		db, err = sql.Open("sqlite3", databasePath)
		if err != nil {
			return nil, fmt.Errorf("open clip database: %w", err)
		}
		db.SetMaxOpenConns(1)
		schema = clipSchema{kind: clipSchemaNone}
	}

	requireExistingKey := schema.kind == clipSchemaPhase2Encrypted || schema.kind == clipSchemaCurrent || schema.kind == clipSchemaPresentation
	tokenKey, err := loadOrCreateTokenKey(root, requireExistingKey)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	block, err := aes.NewCipher(tokenKey)
	if err != nil {
		return nil, fmt.Errorf("initialize clip token encryption: %w", err)
	}
	tokenAEAD, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("initialize clip token encryption: %w", err)
	}
	store := &clipStore{root: root, files: files, db: db, tokenAEAD: tokenAEAD}
	if err := store.initialize(schema); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := store.validateEncryptedTokens(); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := store.reconcile(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *clipStore) initialize(schema clipSchema) error {
	if schema.kind == clipSchemaNone {
		_, err := s.db.Exec(`
			CREATE TABLE clips (
				id TEXT PRIMARY KEY,
				owner_uuid TEXT NOT NULL,
				title TEXT NOT NULL,
				rating_key TEXT NOT NULL,
				media_id INTEGER NOT NULL,
				from_ms INTEGER NOT NULL,
				to_ms INTEGER NOT NULL,
				created_at TEXT NOT NULL,
				share_token_hash TEXT NOT NULL UNIQUE,
				share_token_ciphertext BLOB NOT NULL,
				creator_display_name TEXT,
				media_kind TEXT,
				movie_title TEXT,
				movie_year INTEGER,
				show_title TEXT,
				season_number INTEGER,
				episode_number INTEGER,
				episode_title TEXT,
				thumbnail_mime TEXT
			);
			CREATE INDEX clips_owner_created ON clips(owner_uuid, created_at DESC);
			CREATE INDEX clips_created ON clips(created_at DESC);
		`)
		if err != nil {
			return fmt.Errorf("initialize clip database: %w", err)
		}
		return nil
	}
	if schema.kind == clipSchemaPresentation {
		return nil
	}
	if _, err := s.db.Exec("PRAGMA secure_delete = ON"); err != nil {
		return fmt.Errorf("prepare legacy clip migration: %w", err)
	}
	if err := s.migrateToEncryptedTokens(schema.kind == clipSchemaPhase1Raw, schema.kind == clipSchemaPhase2Encrypted || schema.kind == clipSchemaCurrent, schema.kind != clipSchemaCurrent); err != nil {
		return err
	}
	if _, err := s.db.Exec("VACUUM"); err != nil {
		return fmt.Errorf("vacuum legacy clip data: %w", err)
	}
	return nil
}

func (s *clipStore) migrateToEncryptedTokens(hasRawToken, hasCiphertext, hasPath bool) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin clip database migration: %w", err)
	}
	defer tx.Rollback()
	_, err = tx.Exec(`CREATE TABLE clips_v2 (
		id TEXT PRIMARY KEY,
		owner_uuid TEXT NOT NULL,
		title TEXT NOT NULL,
		rating_key TEXT NOT NULL,
		media_id INTEGER NOT NULL,
		from_ms INTEGER NOT NULL,
		to_ms INTEGER NOT NULL,
		created_at TEXT NOT NULL,
		share_token_hash TEXT NOT NULL UNIQUE,
		share_token_ciphertext BLOB NOT NULL,
		creator_display_name TEXT,
		media_kind TEXT,
		movie_title TEXT,
		movie_year INTEGER,
		show_title TEXT,
		season_number INTEGER,
		episode_number INTEGER,
		episode_title TEXT,
		thumbnail_mime TEXT
	)`)
	if err != nil {
		return fmt.Errorf("create migrated clip table: %w", err)
	}
	selectColumns := "id, owner_uuid, title, rating_key, media_id, from_ms, to_ms, created_at, "
	if hasRawToken {
		selectColumns += "share_token, "
	} else {
		selectColumns += "'' AS share_token, "
	}
	if hasCiphertext {
		selectColumns += "share_token_ciphertext, "
	} else {
		selectColumns += "NULL AS share_token_ciphertext, "
	}
	selectColumns += "share_token_hash"
	if hasPath {
		selectColumns += ", file_path"
	}
	rows, err := tx.Query("SELECT " + selectColumns + " FROM clips")
	if err != nil {
		return fmt.Errorf("read legacy clip metadata: %w", err)
	}
	type legacyClip struct {
		id, owner, title, rating, created, oldToken, hash, path string
		oldCiphertext                                           []byte
		media, from, to                                         int64
	}
	var legacy []legacyClip
	for rows.Next() {
		var clip legacyClip
		var scanErr error
		scanArgs := []any{&clip.id, &clip.owner, &clip.title, &clip.rating, &clip.media, &clip.from, &clip.to, &clip.created, &clip.oldToken, &clip.oldCiphertext, &clip.hash}
		if hasPath {
			scanArgs = append(scanArgs, &clip.path)
		}
		scanErr = rows.Scan(scanArgs...)
		if scanErr != nil {
			rows.Close()
			return fmt.Errorf("read legacy clip metadata: %w", scanErr)
		}
		legacy = append(legacy, clip)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("read legacy clip metadata: %w", err)
	}
	rows.Close()
	for _, clip := range legacy {
		token := clip.oldToken
		if token == "" {
			var tokenErr error
			token, _, tokenErr = newShareToken()
			if tokenErr != nil {
				return tokenErr
			}
		}
		ciphertext := clip.oldCiphertext
		if len(ciphertext) == 0 {
			var encryptErr error
			ciphertext, encryptErr = s.encryptToken(token)
			if encryptErr != nil {
				return fmt.Errorf("encrypt legacy clip token: %w", encryptErr)
			}
		}
		hash := clip.hash
		if !hasCiphertext || hash == "" {
			hash = hashShareToken(token)
		}
		if _, err := tx.Exec(`INSERT INTO clips_v2
			(id, owner_uuid, title, rating_key, media_id, from_ms, to_ms, created_at, share_token_hash, share_token_ciphertext,
			 creator_display_name, media_kind, movie_title, movie_year, show_title, season_number, episode_number, episode_title, thumbnail_mime)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, clip.id, clip.owner, clip.title, clip.rating,
			clip.media, clip.from, clip.to, clip.created, hash, ciphertext, nil, nil, nil, nil, nil, nil, nil, nil, nil); err != nil {
			return fmt.Errorf("copy clip metadata during migration: %w", err)
		}
	}
	if _, err = tx.Exec("DROP TABLE clips"); err != nil {
		return fmt.Errorf("remove old clip table: %w", err)
	}
	if _, err = tx.Exec("ALTER TABLE clips_v2 RENAME TO clips"); err != nil {
		return fmt.Errorf("rename migrated clip table: %w", err)
	}
	if _, err = tx.Exec("CREATE INDEX IF NOT EXISTS clips_owner_created ON clips(owner_uuid, created_at DESC)"); err != nil {
		return fmt.Errorf("recreate clip owner index: %w", err)
	}
	if _, err = tx.Exec("CREATE INDEX IF NOT EXISTS clips_created ON clips(created_at DESC)"); err != nil {
		return fmt.Errorf("recreate clip index: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit clip database migration: %w", err)
	}
	return nil
}

func (s *clipStore) validateEncryptedTokens() error {
	rows, err := s.db.Query("SELECT id, share_token_hash, share_token_ciphertext FROM clips")
	if err != nil {
		return fmt.Errorf("read encrypted clip tokens: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, tokenHash string
		var ciphertext []byte
		if err := rows.Scan(&id, &tokenHash, &ciphertext); err != nil {
			return fmt.Errorf("read encrypted clip token: %w", err)
		}
		token, err := s.decryptToken(ciphertext)
		if err != nil || tokenHash != hashShareToken(token) {
			if err == nil {
				err = errors.New("encrypted clip token does not match its hash")
			}
			return fmt.Errorf("validate encrypted clip %s: %w", id, err)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("validate encrypted clip tokens: %w", err)
	}
	return nil
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func (s *clipStore) close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func newShareToken() (string, string, error) {
	var raw [32]byte
	if _, err := io.ReadFull(rand.Reader, raw[:]); err != nil {
		return "", "", fmt.Errorf("generate share token: %w", err)
	}
	token := hex.EncodeToString(raw[:])
	return token, hashShareToken(token), nil
}

func hashShareToken(token string) string {
	hash := sha256.Sum256([]byte(token))
	return hex.EncodeToString(hash[:])
}

func (s *clipStore) encryptToken(token string) ([]byte, error) {
	nonce := make([]byte, s.tokenAEAD.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return s.tokenAEAD.Seal(nonce, nonce, []byte(token), nil), nil
}

func (s *clipStore) decryptToken(ciphertext []byte) (string, error) {
	nonceSize := s.tokenAEAD.NonceSize()
	if len(ciphertext) < nonceSize {
		return "", errors.New("clip token ciphertext is invalid")
	}
	plaintext, err := s.tokenAEAD.Open(nil, ciphertext[:nonceSize], ciphertext[nonceSize:], nil)
	if err != nil {
		return "", fmt.Errorf("decrypt clip token: %w", err)
	}
	return string(plaintext), nil
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nullableInt(value *int) any {
	if value == nil {
		return nil
	}
	return *value
}

func (s *clipStore) promote(spec renderJobSpec, sourcePath string) (*Clip, error) {
	return s.promoteWithArtwork(spec, sourcePath, "", "")
}

func (s *clipStore) promoteWithArtwork(spec renderJobSpec, sourcePath, artworkSource, artworkMIME string) (*Clip, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("clip storage is unavailable")
	}
	if artworkSource == "" {
		artworkMIME = ""
	}
	info, err := os.Stat(sourcePath)
	if err != nil {
		return nil, fmt.Errorf("stat completed render: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() == 0 {
		return nil, errors.New("completed render is empty or not regular")
	}

	id := uuid.NewString()
	token, tokenHash, err := newShareToken()
	if err != nil {
		return nil, err
	}
	path, err := s.clipPath(id)
	if err != nil {
		return nil, err
	}
	tmp := filepath.Join(s.files, "."+id+".partial")
	artworkPath := ""
	rollback := func() {
		_ = os.Remove(tmp)
		_ = os.Remove(path)
		if artworkPath != "" {
			_ = os.Remove(artworkPath)
			_ = os.Remove(filepath.Join(s.files, "."+id+".thumb.partial"))
		}
		_ = syncDirectory(s.files)
	}
	if err := copyFileAtomically(sourcePath, tmp, path); err != nil {
		rollback()
		return nil, fmt.Errorf("persist clip bytes: %w", err)
	}
	if artworkSource != "" {
		artworkPath, err = artworkPathForFiles(s.files, id)
		if err != nil {
			rollback()
			return nil, err
		}
		artworkTmp := filepath.Join(s.files, "."+id+".thumb.partial")
		if err := copyFileAtomically(artworkSource, artworkTmp, artworkPath); err != nil {
			rollback()
			return nil, fmt.Errorf("persist clip artwork: %w", err)
		}
	}

	created := time.Now().UTC()
	clip := &Clip{
		ID: id, OwnerUUID: spec.OwnerUUID, Title: spec.Title,
		RatingKey: spec.RatingKey, MediaID: spec.MediaID, FromMs: spec.FromMs,
		ToMs: spec.ToMs, CreatedAt: created, FilePath: path, ShareToken: token,
		CreatorDisplayName: spec.CreatorDisplayName, MediaKind: spec.MediaKind,
		MovieTitle: spec.MovieTitle, MovieYear: spec.MovieYear, ShowTitle: spec.ShowTitle,
		SeasonNumber: spec.SeasonNumber, EpisodeNumber: spec.EpisodeNumber,
		EpisodeTitle: spec.EpisodeTitle, ArtworkPath: artworkPath, ThumbnailMIME: artworkMIME,
		tokenHash: tokenHash,
	}
	ciphertext, err := s.encryptToken(token)
	if err != nil {
		rollback()
		return nil, fmt.Errorf("encrypt clip token: %w", err)
	}
	_, err = s.db.Exec(`INSERT INTO clips
		(id, owner_uuid, title, rating_key, media_id, from_ms, to_ms, created_at, share_token_hash, share_token_ciphertext,
		 creator_display_name, media_kind, movie_title, movie_year, show_title, season_number, episode_number, episode_title, thumbnail_mime)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		clip.ID, clip.OwnerUUID, clip.Title, clip.RatingKey, clip.MediaID,
		clip.FromMs, clip.ToMs, clip.CreatedAt.Format(time.RFC3339Nano),
		tokenHash, ciphertext, nullableString(clip.CreatorDisplayName), nullableString(clip.MediaKind),
		nullableString(clip.MovieTitle), nullableInt(clip.MovieYear), nullableString(clip.ShowTitle),
		nullableInt(clip.SeasonNumber), nullableInt(clip.EpisodeNumber), nullableString(clip.EpisodeTitle),
		nullableString(clip.ThumbnailMIME))
	if err != nil {
		rollback()
		return nil, fmt.Errorf("persist clip metadata: %w", err)
	}
	return clip, nil
}

func copyFileAtomically(sourcePath, tmpPath, destinationPath string) error {
	source, err := os.Open(sourcePath)
	if err != nil {
		return err
	}
	defer source.Close()
	tmp, err := os.OpenFile(tmpPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	if _, err = io.Copy(tmp, source); err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err := os.Rename(tmpPath, destinationPath); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(destinationPath))
}

func parseClipTime(value string) (time.Time, error) {
	return time.Parse(time.RFC3339Nano, value)
}

func (s *clipStore) clipPath(id string) (string, error) {
	return clipPathForFiles(s.files, id)
}

func (s *clipStore) scanClip(scanner interface{ Scan(...any) error }) (*Clip, error) {
	var clip Clip
	var created string
	var tokenHash string
	var creator, mediaKind, movieTitle, showTitle, episodeTitle, thumbnailMIME sql.NullString
	var movieYear, seasonNumber, episodeNumber sql.NullInt64
	if err := scanner.Scan(&clip.ID, &clip.OwnerUUID, &clip.Title, &clip.RatingKey,
		&clip.MediaID, &clip.FromMs, &clip.ToMs, &created, &tokenHash, &clip.tokenCiphertext,
		&creator, &mediaKind, &movieTitle, &movieYear, &showTitle, &seasonNumber, &episodeNumber,
		&episodeTitle, &thumbnailMIME); err != nil {
		return nil, err
	}
	var err error
	clip.CreatedAt, err = parseClipTime(created)
	if err != nil {
		return nil, fmt.Errorf("parse clip creation time: %w", err)
	}
	clip.FilePath, err = s.clipPath(clip.ID)
	if err != nil {
		return nil, err
	}
	clip.CreatorDisplayName = creator.String
	clip.MediaKind = mediaKind.String
	clip.MovieTitle = movieTitle.String
	clip.ShowTitle = showTitle.String
	clip.EpisodeTitle = episodeTitle.String
	if movieYear.Valid {
		value := int(movieYear.Int64)
		clip.MovieYear = &value
	}
	if seasonNumber.Valid {
		value := int(seasonNumber.Int64)
		clip.SeasonNumber = &value
	}
	if episodeNumber.Valid {
		value := int(episodeNumber.Int64)
		clip.EpisodeNumber = &value
	}
	if thumbnailMIME.Valid && thumbnailMIME.String != "" {
		clip.ThumbnailMIME = thumbnailMIME.String
		clip.ArtworkPath, err = artworkPathForFiles(s.files, clip.ID)
		if err != nil {
			return nil, err
		}
	}
	clip.tokenHash = tokenHash
	return &clip, nil
}

const clipSelect = `id, owner_uuid, title, rating_key, media_id, from_ms, to_ms, created_at, share_token_hash, share_token_ciphertext,
	creator_display_name, media_kind, movie_title, movie_year, show_title, season_number, episode_number, episode_title, thumbnail_mime`

func (s *clipStore) get(id string) (*Clip, error) {
	row := s.db.QueryRow("SELECT "+clipSelect+" FROM clips WHERE id = ?", id)
	clip, err := s.scanClip(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errClipNotFound
	}
	if err == nil {
		err = s.verifyClipToken(clip)
	}
	return clip, err
}

func (s *clipStore) reconcile() error {
	// Reconciliation is deliberately non-destructive. Complete correspondence
	// is preflighted before migrations or startup reaches this point; a later
	// mismatch is treated as an unsafe restore rather than repaired by delete.
	return preflightClipFiles(s.db, s.files, true)
}

func (s *clipStore) getByShareToken(token string) (*Clip, error) {
	row := s.db.QueryRow("SELECT "+clipSelect+" FROM clips WHERE share_token_hash = ?", hashShareToken(token))
	clip, err := s.scanClip(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errClipNotFound
	}
	if err == nil {
		err = s.verifyClipToken(clip)
	}
	return clip, err
}

func (s *clipStore) verifyClipToken(clip *Clip) error {
	if clip == nil || clip.tokenHash == "" {
		return errors.New("clip token hash is missing")
	}
	token, err := s.decryptToken(clip.tokenCiphertext)
	if err != nil {
		return err
	}
	if hashShareToken(token) != clip.tokenHash {
		return errors.New("clip token ciphertext does not match its row")
	}
	return nil
}

func (s *clipStore) list(owner string, all bool) ([]Clip, error) {
	query := "SELECT " + clipSelect + " FROM clips"
	args := []any{}
	if !all {
		query += " WHERE owner_uuid = ?"
		args = append(args, owner)
	}
	query += " ORDER BY created_at DESC, id DESC"
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []Clip
	for rows.Next() {
		clip, err := s.scanClip(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, *clip)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func (s *clipStore) delete(id string) error {
	clip, err := s.get(id)
	if err != nil {
		return err
	}
	if err := os.Remove(clip.FilePath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove clip bytes: %w", err)
	}
	if clip.ArtworkPath != "" {
		if err := os.Remove(clip.ArtworkPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove clip artwork: %w", err)
		}
	}
	if err := syncDirectory(s.files); err != nil {
		return fmt.Errorf("sync clip deletion: %w", err)
	}
	result, err := s.db.Exec("DELETE FROM clips WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("remove clip metadata: %w", err)
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return errClipNotFound
	}
	return nil
}

func (a *API) clipIsAdmin(user *User) bool {
	return a != nil && a.app != nil && a.app.isServerOwner(user)
}

func (a *API) clipCanAccess(user *User, clip *Clip) bool {
	return user != nil && clip != nil && (a.clipIsAdmin(user) || user.Uuid == clip.OwnerUUID)
}

func (a *API) clipShareURL(token string) string {
	return clipShareURLForDomain(a.config.API.Domain, token)
}

func clipShareURLForDomain(domain, token string) string {
	path := "/shared/clips/" + token + "/download"
	if strings.TrimSpace(domain) == "" {
		return path
	}
	return strings.TrimRight(domain, "/") + path
}

type clipAPIResponse struct {
	ID                 string    `json:"id"`
	CreatorDisplayName string    `json:"creatorDisplayName,omitempty"`
	MediaKind          string    `json:"mediaKind,omitempty"`
	MovieTitle         string    `json:"movieTitle,omitempty"`
	MovieYear          *int      `json:"movieYear,omitempty"`
	ShowTitle          string    `json:"showTitle,omitempty"`
	SeasonNumber       *int      `json:"seasonNumber,omitempty"`
	EpisodeNumber      *int      `json:"episodeNumber,omitempty"`
	EpisodeTitle       string    `json:"episodeTitle,omitempty"`
	ArtworkURL         string    `json:"artworkUrl,omitempty"`
	Title              string    `json:"title"`
	RatingKey          string    `json:"ratingKey"`
	MediaID            int64     `json:"mediaId"`
	FromMs             int64     `json:"fromMs"`
	ToMs               int64     `json:"toMs"`
	CreatedAt          time.Time `json:"createdAt"`
	ShareURL           string    `json:"shareUrl,omitempty"`
	DownloadURL        string    `json:"downloadUrl,omitempty"`
	PublicDownloadURL  string    `json:"publicDownloadUrl,omitempty"`
	CanDelete          bool      `json:"canDelete"`
	IsAdmin            *bool     `json:"isAdmin,omitempty"`
}

type clipListResponse struct {
	Clips   []clipAPIResponse `json:"clips"`
	IsAdmin bool              `json:"isAdmin"`
}

func (a *API) clipResponse(clip *Clip, includeOwner, includeShare, canDelete, isAdmin, includeAdmin bool) (clipAPIResponse, error) {
	result := clipAPIResponse{
		ID: clip.ID, Title: clip.Title, RatingKey: clip.RatingKey, MediaID: clip.MediaID,
		FromMs: clip.FromMs, ToMs: clip.ToMs, CreatedAt: clip.CreatedAt,
		CanDelete: canDelete,
	}
	if includeAdmin {
		result.IsAdmin = &isAdmin
	}
	if includeShare {
		result.DownloadURL = "/clips/" + clip.ID + "/download"
		token := clip.ShareToken
		if token == "" {
			if err := a.app.clipStore.verifyClipToken(clip); err != nil {
				return clipAPIResponse{}, err
			}
			var err error
			token, err = a.app.clipStore.decryptToken(clip.tokenCiphertext)
			if err != nil {
				return clipAPIResponse{}, err
			}
		} else if hashShareToken(token) != clip.tokenHash {
			return clipAPIResponse{}, errors.New("clip token does not match its row")
		}
		result.ShareURL = a.clipShareURL(token)
		result.PublicDownloadURL = result.ShareURL
	}
	if includeOwner {
		result.CreatorDisplayName = clip.CreatorDisplayName
		result.MediaKind = clip.MediaKind
		result.MovieTitle = clip.MovieTitle
		result.MovieYear = clip.MovieYear
		result.ShowTitle = clip.ShowTitle
		result.SeasonNumber = clip.SeasonNumber
		result.EpisodeNumber = clip.EpisodeNumber
		result.EpisodeTitle = clip.EpisodeTitle
		if clip.ArtworkPath != "" {
			result.ArtworkURL = "/clips/" + clip.ID + "/artwork"
		}
	}
	return result, nil
}

func (a *API) listClips(ctx fiber.Ctx) error {
	setCapabilityResponseHeaders(ctx)
	user := UserFromContext(ctx.UserContext())
	if user == nil || user.Uuid == "" {
		return renderAPIErrorCode(ctx, http.StatusUnauthorized, "authentication_required", "authentication required")
	}
	if a.app == nil || a.app.clipStore == nil {
		return renderAPIError(ctx, http.StatusServiceUnavailable, "clip storage is unavailable")
	}
	all := a.clipIsAdmin(user)
	clips, err := a.app.clipStore.list(user.Uuid, all)
	if err != nil {
		return renderAPIError(ctx, http.StatusServiceUnavailable, "clip storage is unavailable")
	}
	result := make([]clipAPIResponse, 0, len(clips))
	for i := range clips {
		response, err := a.clipResponse(&clips[i], true, true, all || clips[i].OwnerUUID == user.Uuid, all, false)
		if err != nil {
			return renderAPIError(ctx, http.StatusServiceUnavailable, "clip storage is unavailable")
		}
		result = append(result, response)
	}
	return ctx.JSON(clipListResponse{Clips: result, IsAdmin: all})
}

func (a *API) getClip(ctx fiber.Ctx) error {
	setCapabilityResponseHeaders(ctx)
	user := UserFromContext(ctx.UserContext())
	if user == nil || user.Uuid == "" {
		return renderAPIErrorCode(ctx, http.StatusUnauthorized, "authentication_required", "authentication required")
	}
	if a.app == nil || a.app.clipStore == nil {
		return renderAPIError(ctx, http.StatusServiceUnavailable, "clip storage is unavailable")
	}
	clip, err := a.app.clipStore.get(ctx.Params("id"))
	if err != nil {
		if errors.Is(err, errClipNotFound) {
			return renderAPIError(ctx, http.StatusNotFound, "clip not found")
		}
		return renderAPIError(ctx, http.StatusServiceUnavailable, "clip storage is unavailable")
	}
	if !a.clipCanAccess(user, clip) {
		return renderAPIError(ctx, http.StatusNotFound, "clip not found")
	}
	isAdmin := a.clipIsAdmin(user)
	response, err := a.clipResponse(clip, true, true, isAdmin || clip.OwnerUUID == user.Uuid, isAdmin, true)
	if err != nil {
		return renderAPIError(ctx, http.StatusServiceUnavailable, "clip storage is unavailable")
	}
	return ctx.JSON(response)
}

func (a *API) downloadClip(ctx fiber.Ctx) error {
	setCapabilityResponseHeaders(ctx)
	user := UserFromContext(ctx.UserContext())
	if user == nil || user.Uuid == "" {
		return renderAPIErrorCode(ctx, http.StatusUnauthorized, "authentication_required", "authentication required")
	}
	if a.app == nil || a.app.clipStore == nil {
		return renderAPIError(ctx, http.StatusServiceUnavailable, "clip storage is unavailable")
	}
	clip, err := a.app.clipStore.get(ctx.Params("id"))
	if err != nil {
		if errors.Is(err, errClipNotFound) {
			return renderAPIError(ctx, http.StatusNotFound, "clip not found")
		}
		return renderAPIError(ctx, http.StatusServiceUnavailable, "clip storage is unavailable")
	}
	if !a.clipCanAccess(user, clip) {
		return renderAPIError(ctx, http.StatusNotFound, "clip not found")
	}
	return a.sendClipFile(ctx, clip, true)
}

func (a *API) downloadClipArtwork(ctx fiber.Ctx) error {
	setCapabilityResponseHeaders(ctx)
	user := UserFromContext(ctx.UserContext())
	if user == nil || user.Uuid == "" {
		return renderAPIErrorCode(ctx, http.StatusUnauthorized, "authentication_required", "authentication required")
	}
	if a.app == nil || a.app.clipStore == nil {
		return renderAPIError(ctx, http.StatusServiceUnavailable, "clip storage is unavailable")
	}
	clip, err := a.app.clipStore.get(ctx.Params("id"))
	if err != nil {
		if errors.Is(err, errClipNotFound) {
			return renderAPIError(ctx, http.StatusNotFound, "clip not found")
		}
		return renderAPIError(ctx, http.StatusServiceUnavailable, "clip storage is unavailable")
	}
	if !a.clipCanAccess(user, clip) {
		return renderAPIError(ctx, http.StatusNotFound, "clip not found")
	}
	if clip.ArtworkPath == "" {
		return renderAPIError(ctx, http.StatusNotFound, "clip artwork is unavailable")
	}
	if _, err := os.Stat(clip.ArtworkPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return renderAPIError(ctx, http.StatusGone, "clip artwork is unavailable")
		}
		return renderAPIError(ctx, http.StatusServiceUnavailable, "clip storage is unavailable")
	}
	ctx.Set("Content-Type", clip.ThumbnailMIME)
	ctx.Set("X-Content-Type-Options", "nosniff")
	return ctx.SendFile(clip.ArtworkPath, fiber.SendFile{ByteRange: true})
}

func (a *API) deleteClip(ctx fiber.Ctx) error {
	setCapabilityResponseHeaders(ctx)
	user := UserFromContext(ctx.UserContext())
	if user == nil || user.Uuid == "" {
		return renderAPIErrorCode(ctx, http.StatusUnauthorized, "authentication_required", "authentication required")
	}
	if a.app == nil || a.app.clipStore == nil {
		return renderAPIError(ctx, http.StatusServiceUnavailable, "clip storage is unavailable")
	}
	clip, err := a.app.clipStore.get(ctx.Params("id"))
	if err != nil {
		if errors.Is(err, errClipNotFound) {
			return renderAPIError(ctx, http.StatusNotFound, "clip not found")
		}
		return renderAPIError(ctx, http.StatusServiceUnavailable, "clip storage is unavailable")
	}
	if !a.clipCanAccess(user, clip) {
		return renderAPIError(ctx, http.StatusNotFound, "clip not found")
	}
	if err := a.app.clipStore.delete(clip.ID); err != nil {
		return renderAPIError(ctx, http.StatusServiceUnavailable, "clip storage is unavailable")
	}
	return ctx.SendStatus(http.StatusNoContent)
}

func (a *API) publicClip(ctx fiber.Ctx) error {
	setCapabilityResponseHeaders(ctx)
	if a.app == nil || a.app.clipStore == nil {
		return renderAPIError(ctx, http.StatusServiceUnavailable, "clip storage is unavailable")
	}
	clip, err := a.app.clipStore.getByShareToken(ctx.Params("token"))
	if errors.Is(err, errClipNotFound) {
		return renderAPIError(ctx, http.StatusNotFound, "clip not found")
	}
	if err != nil {
		return renderAPIError(ctx, http.StatusServiceUnavailable, "clip storage is unavailable")
	}
	response, err := a.clipResponse(clip, false, false, false, false, false)
	if err != nil {
		return renderAPIError(ctx, http.StatusServiceUnavailable, "clip storage is unavailable")
	}
	return ctx.JSON(response)
}

func (a *API) publicDownloadClip(ctx fiber.Ctx) error {
	setCapabilityResponseHeaders(ctx)
	if a.app == nil || a.app.clipStore == nil {
		return renderAPIError(ctx, http.StatusServiceUnavailable, "clip storage is unavailable")
	}
	clip, err := a.app.clipStore.getByShareToken(ctx.Params("token"))
	if errors.Is(err, errClipNotFound) {
		return renderAPIError(ctx, http.StatusNotFound, "clip not found")
	}
	if err != nil {
		return renderAPIError(ctx, http.StatusServiceUnavailable, "clip storage is unavailable")
	}
	return a.sendClipFile(ctx, clip, false)
}

func (a *API) sendClipFile(ctx fiber.Ctx, clip *Clip, attachment bool) error {
	if _, err := os.Stat(clip.FilePath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return renderAPIError(ctx, http.StatusGone, "clip bytes are unavailable")
		}
		return renderAPIError(ctx, http.StatusServiceUnavailable, "clip storage is unavailable")
	}
	ctx.Type(".mp4")
	setCapabilityResponseHeaders(ctx)
	if attachment {
		ctx.Set(fiber.HeaderContentDisposition, renderDownloadContentDisposition(renderJobSpec{
			Title: clip.Title, FromMs: clip.FromMs, ToMs: clip.ToMs,
		}))
	} else {
		ctx.Set(fiber.HeaderContentDisposition, "inline")
	}
	return ctx.SendFile(clip.FilePath, fiber.SendFile{ByteRange: true})
}
