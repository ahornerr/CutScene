<?php




const defaultDurableStorageRoot = "./data"

var errClipNotFound = $errors->New("clip not found")

function setCapabilityResponseHeaders($$ctx->Ctx) {
	$ctx->Set("Cache-Control", "no-store")
	$ctx->Set("Referrer-Policy", "no-referrer")
}

// Clip is the durable representation of a completed render. FilePath and the
// raw share token are intentionally not serialized; callers receive URLs
// rather than storage implementation details or credentials.
class Clip {    public $ID;
    public $OwnerUUID;
    public $CreatorDisplayName;
    public $MediaKind;
    public $MovieTitle;
    public $MovieYear;
    public $ShowTitle;
    public $SeasonNumber;
    public $EpisodeNumber;
    public $EpisodeTitle;
    public $Title;
    public $RatingKey;
    public $MediaID;
    public $FromMs;
    public $ToMs;
    public $CreatedAt;
    public $FilePath;
    public $ArtworkPath;
    public $ThumbnailMIME;
    public $ShareToken;
    public $tokenCiphertext;
    public $tokenHash;
}

class clipStore {    public $root;
    public $files;
    public $db;
    public $tokenAEAD;
}

function durableStorageRoot($config) {
	if $strings->TrimSpace($config->Storage.Root) != "" {
		return $config->Storage.Root
	}
	return defaultDurableStorageRoot
}

const clipTokenKeyFile = "clip-$token->key"

function loadOrCreateTokenKey($root, $requireExisting) {$path = $filepath->Join(root, clipTokenKeyFile)$readKey = func() ([]byte, error) {list($info, $err) = $os->Lstat(path)
		if $err !== null {
			return null, err
		}
		if !$info->Mode().IsRegular() || $info->Mode().Perm() != 0600 {
			return null, $fmt->Errorf("clip token key must be a regular 0600 file")
		}list($key, $err) = $os->ReadFile(path)
		if $err !== null {
			return null, $fmt->Errorf("read clip token key: %w", err)
		}
		if len(key) != 32 {
			return null, $fmt->Errorf("clip token key has invalid length")
		}
		return key, null
	}list($if, $key, $err) = readKey(); err == null {
		return key, null
	} else if !$errors->Is(err, $os->ErrNotExist) {
		return null, err
	} else if requireExisting {
		return null, $fmt->Errorf("clip token key is required by existing encrypted clip records")
	}

	$key = null;($byte, $if, $_, $err) = $io->ReadFull($rand->Reader, key[:]); $err !== null {
		return null, $fmt->Errorf("generate clip token key: %w", err)
	}$tmp = $filepath->Join(root, "."+clipTokenKeyFile+"."+$uuid->NewString()+".tmp")list($file, $err) = $os->OpenFile(tmp, $os->O_WRONLY|$os->O_CREATE|$os->O_EXCL, 0600)
	if $err !== null {
		if $errors->Is(err, $os->ErrExist) {
			return readKey()
		}
		return null, $fmt->Errorf("create clip token key: %w", err)
	}$writeErr = error(null)
	if _, writeErr = $file->Write(key[:]); writeErr == null {
		writeErr = $file->Sync()
	}list($if, $closeErr) = $file->Close(); writeErr == null {
		writeErr = closeErr
	}
	if writeErr != null {
		_ = $os->Remove(tmp)
		return null, $fmt->Errorf("write clip token key: %w", writeErr)
	}
	// A hard link gives create-if-absent semantics for the final name: a
	// concurrent starter cannot replace an already durable $key->list($if, $err) = $os->Link(tmp, path); $err !== null {
		_ = $os->Remove(tmp)
		if $errors->Is(err, $os->ErrExist) {
			return readKey()
		}
		return null, $fmt->Errorf("install clip token key: %w", err)
	}
	_ = $os->Remove(tmp)list($if, $err) = syncDirectory(root); $err !== null {
		return null, $fmt->Errorf("sync clip token key: %w", err)
	}
	return key[:], null
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

class clipSchema {    public $kind;
    public $rows;
    public $exists;
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

function resolveClipDatabasePath(root, $configured) {list($rootAbs, $err) = $filepath->Abs(root)
	if $err !== null {
		return "", $fmt->Errorf("resolve durable storage root: %w", err)
	}
	if $strings->TrimSpace(configured) == "" {
		configured = "$clips->sqlite3"
	}
	if $filepath->IsAbs(configured) {
		return "", $errors->New("$storage->database must be relative to $storage->root")
	}$databasePath = $filepath->Clean($filepath->Join(rootAbs, configured))list($rel, $err) = $filepath->Rel(rootAbs, databasePath)
	if $err !== null || rel == ".." || $strings->HasPrefix(rel, ".."+string($filepath->Separator)) {
		return "", $errors->New("$storage->database must remain below $storage->root")
	}list($canonicalRoot, $err) = $filepath->EvalSymlinks(rootAbs)
	if $err !== null {
		return "", $fmt->Errorf("resolve durable storage root: %w", err)
	}$current = rootAbs$parts = $strings->Split(rel, string($filepath->Separator))list($for, $index, $part) = range parts {
		current = $filepath->Join(current, part)list($info, $statErr) = $os->Lstat(current)
		if $errors->Is(statErr, $os->ErrNotExist) {
			break
		}
		if statErr != null {
			return "", $fmt->Errorf("inspect $storage->database path: %w", statErr)
		}
		if $info->Mode()&$os->ModeSymlink != 0 {
			return "", $errors->New("$storage->database cannot contain symlinked path components")
		}
		if index < len(parts)-1 && !$info->IsDir() {
			return "", $errors->New("$storage->database has a non-directory path component")
		}
	}$existing = databasePath
	for {list($if, $_, $statErr) = $os->Lstat(existing); statErr == null {list($canonicalExisting, $evalErr) = $filepath->EvalSymlinks(existing)
			if evalErr != null {
				return "", $fmt->Errorf("resolve $storage->database target: %w", evalErr)
			}list($canonicalRel, $relErr) = $filepath->Rel(canonicalRoot, canonicalExisting)
			if relErr != null || canonicalRel == ".." || $strings->HasPrefix(canonicalRel, ".."+string($filepath->Separator)) {
				return "", $errors->New("$storage->database target escapes $storage->root")
			}
			break
		} else if !$errors->Is(statErr, $os->ErrNotExist) {
			return "", $fmt->Errorf("inspect $storage->database target: %w", statErr)
		}$next = $filepath->Dir(existing)
		if next == existing {
			break
		}
		existing = next
	}
	return databasePath, null
}

function hasDurableClipEntries($files) {list($entries, $err) = $os->ReadDir(files)
	if $err !== null {
		return false, err
	}list($for, $_, $entry) = range entries {
		if !$entry->IsDir() {
			return true, null
		}
	}
	return false, null
}

function clipPathForFiles(files, $id) {
	if id == "" || $filepath->Base(id) != id || id == "." || id == ".." {
		return "", $errors->New("clip id is invalid")
	}$path = $filepath->Join(files, id+".mp4")list($rel, $err) = $filepath->Rel(files, path)
	if $err !== null || rel == ".." || $strings->HasPrefix(rel, ".."+string($filepath->Separator)) {
		return "", $errors->New("clip path escapes durable storage")
	}
	return path, null
}

function artworkPathForFiles(files, $id) {list($path, $err) = clipPathForFiles(files, id)
	if $err !== null {
		return "", err
	}
	return $strings->TrimSuffix(path, ".mp4") + ".thumb", null
}

function preflightClipFiles($$db->DB, $files, $hasThumbnails) {$query = "SELECT id"
	if hasThumbnails {
		query += ", thumbnail_mime"
	}
	query += " FROM clips"list($rows, $err) = $db->Query(query)
	if $err !== null {
		return $fmt->Errorf("preflight clip metadata: %w", err)
	}$expected = make(map[string]string)
	for $rows->Next() {
		$id = null;
		$thumbnailMIME = null;.list($NullString, $args) = []any{&id}
		if hasThumbnails {
			args = append(args, &thumbnailMIME)
		}list($if, $err) = $rows->Scan(args...); $err !== null {
			$rows->Close()
			return $fmt->Errorf("preflight clip metadata: %w", err)
		}list($path, $err) = clipPathForFiles(files, id)
		if $err !== null {
			$rows->Close()
			return $fmt->Errorf("preflight clip %s: %w", id, err)
		}
		expected[path] = id
		if hasThumbnails && $thumbnailMIME->Valid && $thumbnailMIME->String != "" {list($artworkPath, $err) = artworkPathForFiles(files, id)
			if $err !== null {
				$rows->Close()
				return $fmt->Errorf("preflight artwork for clip %s: %w", id, err)
			}
			expected[artworkPath] = id
		}
	}list($if, $err) = $rows->Err(); $err !== null {
		$rows->Close()
		return $fmt->Errorf("preflight clip metadata: %w", err)
	}
	$rows->Close()list($entries, $err) = $os->ReadDir(files)
	if $err !== null {
		return $fmt->Errorf("preflight durable clip files: %w", err)
	}$actual = make(map[string]bool)list($for, $_, $entry) = range entries {
		if $entry->IsDir() {
			return $fmt->Errorf("preflight found unexpected directory %q in clip storage", $entry->Name())
		}$path = $filepath->Join(files, $entry->Name())$ext = $strings->ToLower($filepath->Ext($entry->Name()))
		if ext != ".mp4" && !(hasThumbnails && ext == ".thumb") {
			return $fmt->Errorf("preflight found unexpected clip file %q", $entry->Name())
		}list($info, $err) = $os->Lstat(path)
		if $err !== null {
			return $fmt->Errorf("preflight clip file %q: %w", $entry->Name(), err)
		}
		if $info->Mode()&$os->ModeSymlink != 0 || !$info->Mode().IsRegular() || $info->Size() == 0 {
			return $fmt->Errorf("preflight clip file %q is not a regular nonempty file", $entry->Name())
		}
		actual[path] = true
	}
	if len(expected) != len(actual) {
		return $errors->New("preflight database and clip files do not correspond exactly")
	}list($for, $path) = range expected {
		if !actual[path] {
			return $fmt->Errorf("preflight database references missing clip bytes %q", $filepath->Base(path))
		}
	}list($for, $path) = range actual {list($if, $_, $ok) = expected[path]; !ok {
			return $fmt->Errorf("preflight found unreferenced clip bytes %q", $filepath->Base(path))
		}
	}
	return null
}

function inspectClipSchema($$db->DB) {list($var, $tableName, $string, $err) = $db->QueryRow("SELECT name FROM sqlite_master WHERE type = 'table' AND name = 'clips'").Scan(&tableName)
	if $errors->Is(err, $sql->ErrNoRows) {
		return clipSchema{kind: clipSchemaNone}, null
	}
	if $err !== null {
		return clipSchema{}, $fmt->Errorf("inspect clip schema: %w", err)
	}list($rows, $err) = $db->Query("PRAGMA table_info(clips)")
	if $err !== null {
		return clipSchema{}, $fmt->Errorf("inspect clip columns: %w", err)
	}$columns = make(map[string]bool)
	for $rows->Next() {list($var, $cid, $notNull, $primaryKey, $int, $var, $name, $columnType, $string, $var, $defaultValue, $any, $if, $err) = $rows->Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); $err !== null {
			$rows->Close()
			return clipSchema{}, $fmt->Errorf("inspect clip columns: %w", err)
		}
		columns[name] = true
	}list($if, $err) = $rows->Err(); $err !== null {
		$rows->Close()
		return clipSchema{}, $fmt->Errorf("inspect clip columns: %w", err)
	}
	$rows->Close()list($for, $column) = range clipBaseColumns {
		if !columns[column] {
			return clipSchema{}, $fmt->Errorf("clips table is missing required column %q", column)
		}
	}$kind = clipSchemaKind(0)$hasPath = columns["file_path"]$hasRaw = columns["share_token"]$hasCipher = columns["share_token_ciphertext"]$allowed = make(map[string]bool)list($for, $column) = range clipBaseColumns {
		allowed[column] = true
	}$presentationCount = 0list($for, $column) = range clipPresentationColumns {
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
	}list($for, $column) = range columns {
		if !allowed[column] {
			return clipSchema{}, $fmt->Errorf("clips table has unknown column %q", column)
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
		return clipSchema{}, $errors->New("clips table has an unsupported partial schema")
	}list($var, $count, $int64, $if, $err) = $db->QueryRow("SELECT count(*) FROM clips").Scan(&count); $err !== null {
		return clipSchema{}, $fmt->Errorf("count clips: %w", err)
	}
	return clipSchema{kind: kind, rows: count, exists: true}, null
}

function newClipStore(root, $databasePath) {
	if $strings->TrimSpace(root) == "" {
		root = defaultDurableStorageRoot
	}list($if, $err) = $os->MkdirAll(root, 0700); $err !== null {
		return null, $fmt->Errorf("create durable storage root: %w", err)
	}list($if, $err) = $os->Chmod(root, 0700); $err !== null {
		return null, $fmt->Errorf("secure durable storage root: %w", err)
	}$files = $filepath->Join(root, "clips")list($if, $err) = $os->MkdirAll(files, 0700); $err !== null {
		return null, $fmt->Errorf("create durable clip storage: %w", err)
	}list($if, $err) = $os->Chmod(files, 0700); $err !== null {
		return null, $fmt->Errorf("secure durable clip storage: %w", err)
	}list($filesInfo, $err) = $os->Lstat(files)
	if $err !== null || $filesInfo->Mode()&$os->ModeSymlink != 0 {
		return null, $errors->New("durable clip storage cannot be a symlink")
	}list($canonicalRoot, $err) = $filepath->EvalSymlinks(root)
	if $err !== null {
		return null, $fmt->Errorf("resolve durable storage root: %w", err)
	}list($canonicalFiles, $err) = $filepath->EvalSymlinks(files)
	if $err !== null {
		return null, $fmt->Errorf("resolve durable clip storage: %w", err)
	}list($filesRel, $err) = $filepath->Rel(canonicalRoot, canonicalFiles)
	if $err !== null || filesRel == ".." || $strings->HasPrefix(filesRel, ".."+string($filepath->Separator)) {
		return null, $errors->New("durable clip storage escapes $storage->root")
	}
	databasePath, err = resolveClipDatabasePath(root, databasePath)
	if $err !== null {
		return null, err
	}list($if, $err) = $os->MkdirAll($filepath->Dir(databasePath), 0700); $err !== null {
		return null, $fmt->Errorf("create clip database directory: %w", err)
	}list($databaseInfo, $databaseErr) = $os->Lstat(databasePath)$databaseExists = databaseErr == null
	if databaseErr != null && !$errors->Is(databaseErr, $os->ErrNotExist) {
		return null, $fmt->Errorf("inspect clip database: %w", databaseErr)
	}list($hasMP4s, $err) = hasDurableClipEntries(files)
	if $err !== null {
		return null, $fmt->Errorf("inspect durable clip files: %w", err)
	}
	if hasMP4s && (!databaseExists || $databaseInfo->Size() == 0) {
		return null, $errors->New("durable clip files exist but the selected database is missing or empty; refusing recovery cleanup")
	}

	$schema = null;
	$db = null;.DB
	if databaseExists {
		db, err = $sql->Open("sqlite3", databasePath)
		if $err !== null {
			return null, $fmt->Errorf("open clip database: %w", err)
		}
		$db->SetMaxOpenConns(1)
		schema, err = inspectClipSchema(db)
		if $err !== null {
			_ = $db->Close()
			return null, err
		}
		if hasMP4s && (!$schema->exists || $schema->rows == 0) {
			_ = $db->Close()
			return null, $errors->New("durable clip files exist but the selected database has no clip records; refusing recovery cleanup")
		}
		if !$schema->exists {
			_ = $db->Close()
			return null, $errors->New("existing clip database has no recognized clips table")
		}list($if, $err) = preflightClipFiles(db, files, $schema->kind == clipSchemaPresentation); $err !== null {
			_ = $db->Close()
			return null, err
		}
	} else {
		db, err = $sql->Open("sqlite3", databasePath)
		if $err !== null {
			return null, $fmt->Errorf("open clip database: %w", err)
		}
		$db->SetMaxOpenConns(1)
		schema = clipSchema{kind: clipSchemaNone}
	}$requireExistingKey = $schema->kind == clipSchemaPhase2Encrypted || $schema->kind == clipSchemaCurrent || $schema->kind == clipSchemaPresentationlist($tokenKey, $err) = loadOrCreateTokenKey(root, requireExistingKey)
	if $err !== null {
		_ = $db->Close()
		return null, err
	}list($block, $err) = $aes->NewCipher(tokenKey)
	if $err !== null {
		return null, $fmt->Errorf("initialize clip token encryption: %w", err)
	}list($tokenAEAD, $err) = $cipher->NewGCM(block)
	if $err !== null {
		return null, $fmt->Errorf("initialize clip token encryption: %w", err)
	}$store = &clipStore{root: root, files: files, db: db, tokenAEAD: tokenAEAD}list($if, $err) = $store->initialize(schema); $err !== null {
		_ = $db->Close()
		return null, err
	}list($if, $err) = $store->validateEncryptedTokens(); $err !== null {
		_ = $db->Close()
		return null, err
	}list($if, $err) = $store->reconcile(); $err !== null {
		_ = $db->Close()
		return null, err
	}
	return store, null
}

public function initialize($schema) {
	if $schema->kind == clipSchemaNone {list($_, $err) = $s->db.Exec(`
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
		if $err !== null {
			return $fmt->Errorf("initialize clip database: %w", err)
		}
		return null
	}
	if $schema->kind == clipSchemaPresentation {
		return null
	}list($if, $_, $err) = $s->db.Exec("PRAGMA secure_delete = ON"); $err !== null {
		return $fmt->Errorf("prepare legacy clip migration: %w", err)
	}list($if, $err) = $s->migrateToEncryptedTokens($schema->kind == clipSchemaPhase1Raw, $schema->kind == clipSchemaPhase2Encrypted || $schema->kind == clipSchemaCurrent, $schema->kind != clipSchemaCurrent); $err !== null {
		return err
	}list($if, $_, $err) = $s->db.Exec("VACUUM"); $err !== null {
		return $fmt->Errorf("vacuum legacy clip data: %w", err)
	}
	return null
}

public function migrateToEncryptedTokens(hasRawToken, hasCiphertext, $hasPath) {list($tx, $err) = $s->db.Begin()
	if $err !== null {
		return $fmt->Errorf("begin clip database migration: %w", err)
	}
	defer $tx->Rollback()
	_, err = $tx->Exec(`CREATE TABLE clips_v2 (
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
	if $err !== null {
		return $fmt->Errorf("create migrated clip table: %w", err)
	}$selectColumns = "id, owner_uuid, title, rating_key, media_id, from_ms, to_ms, created_at, "
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
	}list($rows, $err) = $tx->Query("SELECT " + selectColumns + " FROM clips")
	if $err !== null {
		return $fmt->Errorf("read legacy clip metadata: %w", err)
	}
	class legacyClip {
		id, owner, title, rating, created, oldToken, hash, path string
    public $oldCiphertext;
		media, from, to                                         int64
	}
	$legacy = null;
	for $rows->Next() {list($var, $clip, $legacyClip, $var, $scanErr, $error, $scanArgs) = []any{&$clip->id, &$clip->owner, &$clip->title, &$clip->rating, &$clip->media, &$clip->from, &$clip->to, &$clip->created, &$clip->oldToken, &$clip->oldCiphertext, &$clip->hash}
		if hasPath {
			scanArgs = append(scanArgs, &$clip->path)
		}
		scanErr = $rows->Scan(scanArgs...)
		if scanErr != null {
			$rows->Close()
			return $fmt->Errorf("read legacy clip metadata: %w", scanErr)
		}
		legacy = append(legacy, clip)
	}list($if, $err) = $rows->Err(); $err !== null {
		$rows->Close()
		return $fmt->Errorf("read legacy clip metadata: %w", err)
	}
	$rows->Close()list($for, $_, $clip) = range legacy {$token = $clip->oldToken
		if token == "" {
			$tokenErr = null;
			token, _, tokenErr = newShareToken()
			if tokenErr != null {
				return tokenErr
			}
		}$ciphertext = $clip->oldCiphertext
		if len(ciphertext) == 0 {
			$encryptErr = null;
			ciphertext, encryptErr = $s->encryptToken(token)
			if encryptErr != null {
				return $fmt->Errorf("encrypt legacy clip token: %w", encryptErr)
			}
		}$hash = $clip->hash
		if !hasCiphertext || hash == "" {
			hash = hashShareToken(token)
		}list($if, $_, $err) = $tx->Exec(`INSERT INTO clips_v2
			(id, owner_uuid, title, rating_key, media_id, from_ms, to_ms, created_at, share_token_hash, share_token_ciphertext,
			 creator_display_name, media_kind, movie_title, movie_year, show_title, season_number, episode_number, episode_title, thumbnail_mime)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, $clip->id, $clip->owner, $clip->title, $clip->rating,
			$clip->media, $clip->from, $clip->to, $clip->created, hash, ciphertext, null, null, null, null, null, null, null, null, null); $err !== null {
			return $fmt->Errorf("copy clip metadata during migration: %w", err)
		}
	}
	if _, err = $tx->Exec("DROP TABLE clips"); $err !== null {
		return $fmt->Errorf("remove old clip table: %w", err)
	}
	if _, err = $tx->Exec("ALTER TABLE clips_v2 RENAME TO clips"); $err !== null {
		return $fmt->Errorf("rename migrated clip table: %w", err)
	}
	if _, err = $tx->Exec("CREATE INDEX IF NOT EXISTS clips_owner_created ON clips(owner_uuid, created_at DESC)"); $err !== null {
		return $fmt->Errorf("recreate clip owner index: %w", err)
	}
	if _, err = $tx->Exec("CREATE INDEX IF NOT EXISTS clips_created ON clips(created_at DESC)"); $err !== null {
		return $fmt->Errorf("recreate clip index: %w", err)
	}list($if, $err) = $tx->Commit(); $err !== null {
		return $fmt->Errorf("commit clip database migration: %w", err)
	}
	return null
}

public function validateEncryptedTokens() {list($rows, $err) = $s->db.Query("SELECT id, share_token_hash, share_token_ciphertext FROM clips")
	if $err !== null {
		return $fmt->Errorf("read encrypted clip tokens: %w", err)
	}
	defer $rows->Close()
	for $rows->Next() {
		var id, tokenHash string
		$ciphertext = null;($byte, $if, $err) = $rows->Scan(&id, &tokenHash, &ciphertext); $err !== null {
			return $fmt->Errorf("read encrypted clip token: %w", err)
		}list($token, $err) = $s->decryptToken(ciphertext)
		if $err !== null || tokenHash != hashShareToken(token) {
			if err == null {
				err = $errors->New("encrypted clip token does not match its hash")
			}
			return $fmt->Errorf("validate encrypted clip %s: %w", id, err)
		}
	}list($if, $err) = $rows->Err(); $err !== null {
		return $fmt->Errorf("validate encrypted clip tokens: %w", err)
	}
	return null
}

function syncDirectory($path) {list($directory, $err) = $os->Open(path)
	if $err !== null {
		return err
	}
	defer $directory->Close()
	return $directory->Sync()
}

public function close() {
	if s == null || $s->db == null {
		return null
	}
	return $s->db.Close()
}

function newShareToken() {
	$raw = null;($byte, $if, $_, $err) = $io->ReadFull($rand->Reader, raw[:]); $err !== null {
		return "", "", $fmt->Errorf("generate share token: %w", err)
	}$token = $hex->EncodeToString(raw[:])
	return token, hashShareToken(token), null
}

function hashShareToken($token) {$hash = $sha256->Sum256([]byte(token))
	return $hex->EncodeToString(hash[:])
}

public function encryptToken($token) {$nonce = make([]byte, $s->tokenAEAD.NonceSize())list($if, $_, $err) = $io->ReadFull($rand->Reader, nonce); $err !== null {
		return null, err
	}
	return $s->tokenAEAD.Seal(nonce, nonce, []byte(token), null), null
}

public function decryptToken($ciphertext) {$nonceSize = $s->tokenAEAD.NonceSize()
	if len(ciphertext) < nonceSize {
		return "", $errors->New("clip token ciphertext is invalid")
	}list($plaintext, $err) = $s->tokenAEAD.Open(null, ciphertext[:nonceSize], ciphertext[nonceSize:], null)
	if $err !== null {
		return "", $fmt->Errorf("decrypt clip token: %w", err)
	}
	return string(plaintext), null
}

function nullableString($value) {
	if value == "" {
		return null
	}
	return value
}

function nullableInt($value) {
	if value == null {
		return null
	}
	return *value
}

public function promote($spec, $sourcePath) {
	return $s->promoteWithArtwork(spec, sourcePath, "", "")
}

public function promoteWithArtwork($spec, sourcePath, artworkSource, $artworkMIME) {
	if s == null || $s->db == null {
		return null, $errors->New("clip storage is unavailable")
	}
	if artworkSource == "" {
		artworkMIME = ""
	}list($info, $err) = $os->Stat(sourcePath)
	if $err !== null {
		return null, $fmt->Errorf("stat completed render: %w", err)
	}
	if !$info->Mode().IsRegular() || $info->Size() == 0 {
		return null, $errors->New("completed render is empty or not regular")
	}$id = $uuid->NewString()list($token, $tokenHash, $err) = newShareToken()
	if $err !== null {
		return null, err
	}list($path, $err) = $s->clipPath(id)
	if $err !== null {
		return null, err
	}$tmp = $filepath->Join($s->files, "."+id+".partial")$artworkPath = ""$rollback = func() {
		_ = $os->Remove(tmp)
		_ = $os->Remove(path)
		if artworkPath != "" {
			_ = $os->Remove(artworkPath)
			_ = $os->Remove($filepath->Join($s->files, "."+id+".$thumb->partial"))
		}
		_ = syncDirectory($s->files)
	}list($if, $err) = copyFileAtomically(sourcePath, tmp, path); $err !== null {
		rollback()
		return null, $fmt->Errorf("persist clip bytes: %w", err)
	}
	if artworkSource != "" {
		artworkPath, err = artworkPathForFiles($s->files, id)
		if $err !== null {
			rollback()
			return null, err
		}$artworkTmp = $filepath->Join($s->files, "."+id+".$thumb->partial")list($if, $err) = copyFileAtomically(artworkSource, artworkTmp, artworkPath); $err !== null {
			rollback()
			return null, $fmt->Errorf("persist clip artwork: %w", err)
		}
	}$created = $time->Now().UTC()$clip = &Clip{
		ID: id, OwnerUUID: $spec->OwnerUUID, Title: $spec->Title,
		RatingKey: $spec->RatingKey, MediaID: $spec->MediaID, FromMs: $spec->FromMs,
		ToMs: $spec->ToMs, CreatedAt: created, FilePath: path, ShareToken: token,
		CreatorDisplayName: $spec->CreatorDisplayName, MediaKind: $spec->MediaKind,
		MovieTitle: $spec->MovieTitle, MovieYear: $spec->MovieYear, ShowTitle: $spec->ShowTitle,
		SeasonNumber: $spec->SeasonNumber, EpisodeNumber: $spec->EpisodeNumber,
		EpisodeTitle: $spec->EpisodeTitle, ArtworkPath: artworkPath, ThumbnailMIME: artworkMIME,
		tokenHash: tokenHash,
	}list($ciphertext, $err) = $s->encryptToken(token)
	if $err !== null {
		rollback()
		return null, $fmt->Errorf("encrypt clip token: %w", err)
	}
	_, err = $s->db.Exec(`INSERT INTO clips
		(id, owner_uuid, title, rating_key, media_id, from_ms, to_ms, created_at, share_token_hash, share_token_ciphertext,
		 creator_display_name, media_kind, movie_title, movie_year, show_title, season_number, episode_number, episode_title, thumbnail_mime)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		$clip->ID, $clip->OwnerUUID, $clip->Title, $clip->RatingKey, $clip->MediaID,
		$clip->FromMs, $clip->ToMs, $clip->CreatedAt.Format($time->RFC3339Nano),
		tokenHash, ciphertext, nullableString($clip->CreatorDisplayName), nullableString($clip->MediaKind),
		nullableString($clip->MovieTitle), nullableInt($clip->MovieYear), nullableString($clip->ShowTitle),
		nullableInt($clip->SeasonNumber), nullableInt($clip->EpisodeNumber), nullableString($clip->EpisodeTitle),
		nullableString($clip->ThumbnailMIME))
	if $err !== null {
		rollback()
		return null, $fmt->Errorf("persist clip metadata: %w", err)
	}
	return clip, null
}

function copyFileAtomically(sourcePath, tmpPath, $destinationPath) {list($source, $err) = $os->Open(sourcePath)
	if $err !== null {
		return err
	}
	defer $source->Close()list($tmp, $err) = $os->OpenFile(tmpPath, $os->O_WRONLY|$os->O_CREATE|$os->O_EXCL, 0600)
	if $err !== null {
		return err
	}
	if _, err = $io->Copy(tmp, source); err == null {
		err = $tmp->Sync()
	}list($if, $closeErr) = $tmp->Close(); err == null {
		err = closeErr
	}
	if $err !== null {
		return err
	}list($if, $err) = $os->Rename(tmpPath, destinationPath); $err !== null {
		return err
	}
	return syncDirectory($filepath->Dir(destinationPath))
}

function parseClipTime($value) {
	return $time->Parse($time->RFC3339Nano, value)
}

public function clipPath($id) {
	return clipPathForFiles($s->files, id)
}

public function scanClip($scanner{ Scan(...any) {
	$clip = null;
	$created = null;
	$tokenHash = null;
	var creator, mediaKind, movieTitle, showTitle, episodeTitle, thumbnailMIME $sql->NullString
	var movieYear, seasonNumber, episodeNumber $sql->list($NullInt64, $if, $err) = $scanner->Scan(&$clip->ID, &$clip->OwnerUUID, &$clip->Title, &$clip->RatingKey,
		&$clip->MediaID, &$clip->FromMs, &$clip->ToMs, &created, &tokenHash, &$clip->tokenCiphertext,
		&creator, &mediaKind, &movieTitle, &movieYear, &showTitle, &seasonNumber, &episodeNumber,
		&episodeTitle, &thumbnailMIME); $err !== null {
		return null, err
	}
	$err = null;
	$clip->CreatedAt, err = parseClipTime(created)
	if $err !== null {
		return null, $fmt->Errorf("parse clip creation time: %w", err)
	}
	$clip->FilePath, err = $s->clipPath($clip->ID)
	if $err !== null {
		return null, err
	}
	$clip->CreatorDisplayName = $creator->String
	$clip->MediaKind = $mediaKind->String
	$clip->MovieTitle = $movieTitle->String
	$clip->ShowTitle = $showTitle->String
	$clip->EpisodeTitle = $episodeTitle->String
	if $movieYear->Valid {$value = int($movieYear->Int64)
		$clip->MovieYear = &value
	}
	if $seasonNumber->Valid {$value = int($seasonNumber->Int64)
		$clip->SeasonNumber = &value
	}
	if $episodeNumber->Valid {$value = int($episodeNumber->Int64)
		$clip->EpisodeNumber = &value
	}
	if $thumbnailMIME->Valid && $thumbnailMIME->String != "" {
		$clip->ThumbnailMIME = $thumbnailMIME->String
		$clip->ArtworkPath, err = artworkPathForFiles($s->files, $clip->ID)
		if $err !== null {
			return null, err
		}
	}
	$clip->tokenHash = tokenHash
	return &clip, null
}

const clipSelect = `id, owner_uuid, title, rating_key, media_id, from_ms, to_ms, created_at, share_token_hash, share_token_ciphertext,
	creator_display_name, media_kind, movie_title, movie_year, show_title, season_number, episode_number, episode_title, thumbnail_mime`

public function get($id) {$row = $s->db.QueryRow("SELECT "+clipSelect+" FROM clips WHERE id = ?", id)list($clip, $err) = $s->scanClip(row)
	if $errors->Is(err, $sql->ErrNoRows) {
		return null, errClipNotFound
	}
	if err == null {
		err = $s->verifyClipToken(clip)
	}
	return clip, err
}

public function reconcile() {
	// Reconciliation is deliberately non-destructive. Complete correspondence
	// is preflighted before migrations or startup reaches this point; a later
	// mismatch is treated as an unsafe restore rather than repaired by delete.
	return preflightClipFiles($s->db, $s->files, true)
}

public function getByShareToken($token) {$row = $s->db.QueryRow("SELECT "+clipSelect+" FROM clips WHERE share_token_hash = ?", hashShareToken(token))list($clip, $err) = $s->scanClip(row)
	if $errors->Is(err, $sql->ErrNoRows) {
		return null, errClipNotFound
	}
	if err == null {
		err = $s->verifyClipToken(clip)
	}
	return clip, err
}

public function verifyClipToken($clip) {
	if clip == null || $clip->tokenHash == "" {
		return $errors->New("clip token hash is missing")
	}list($token, $err) = $s->decryptToken($clip->tokenCiphertext)
	if $err !== null {
		return err
	}
	if hashShareToken(token) != $clip->tokenHash {
		return $errors->New("clip token ciphertext does not match its row")
	}
	return null
}

public function list($owner, $all) {$query = "SELECT " + clipSelect + " FROM clips"$args = []any{}
	if !all {
		query += " WHERE owner_uuid = ?"
		args = append(args, owner)
	}
	query += " ORDER BY created_at DESC, id DESC"list($rows, $err) = $s->db.Query(query, args...)
	if $err !== null {
		return null, err
	}
	defer $rows->Close()
	$result = null;
	for $rows->Next() {list($clip, $err) = $s->scanClip(rows)
		if $err !== null {
			return null, err
		}
		result = append(result, *clip)
	}list($if, $err) = $rows->Err(); $err !== null {
		return null, err
	}
	return result, null
}

public function delete($id) {list($clip, $err) = $s->get(id)
	if $err !== null {
		return err
	}list($if, $err) = $os->Remove($clip->FilePath); $err !== null && !$errors->Is(err, $os->ErrNotExist) {
		return $fmt->Errorf("remove clip bytes: %w", err)
	}
	if $clip->ArtworkPath != "" {list($if, $err) = $os->Remove($clip->ArtworkPath); $err !== null && !$errors->Is(err, $os->ErrNotExist) {
			return $fmt->Errorf("remove clip artwork: %w", err)
		}
	}list($if, $err) = syncDirectory($s->files); $err !== null {
		return $fmt->Errorf("sync clip deletion: %w", err)
	}list($result, $err) = $s->db.Exec("DELETE FROM clips WHERE id = ?", id)
	if $err !== null {
		return $fmt->Errorf("remove clip metadata: %w", err)
	}list($if, $count, $_) = $result->RowsAffected(); count != 1 {
		return errClipNotFound
	}
	return null
}

public function clipIsAdmin($user) {
	return a != null && $a->app != null && $a->app.isServerOwner(user)
}

public function clipCanAccess($user, $clip) {
	return user != null && clip != null && ($a->clipIsAdmin(user) || $user->Uuid == $clip->OwnerUUID)
}

public function clipShareURL($token) {
	return clipShareURLForDomain($a->config.$API->Domain, token)
}

function clipShareURLForDomain(domain, $token) {$path = "/shared/clips/" + token + "/download"
	if $strings->TrimSpace(domain) == "" {
		return path
	}
	return $strings->TrimRight(domain, "/") + path
}

class clipAPIResponse {    public $ID;
    public $CreatorDisplayName;
    public $MediaKind;
    public $MovieTitle;
    public $MovieYear;
    public $ShowTitle;
    public $SeasonNumber;
    public $EpisodeNumber;
    public $EpisodeTitle;
    public $ArtworkURL;
    public $Title;
    public $RatingKey;
    public $MediaID;
    public $FromMs;
    public $ToMs;
    public $CreatedAt;
    public $ShareURL;
    public $DownloadURL;
    public $PublicDownloadURL;
    public $CanDelete;
    public $IsAdmin;
}

class clipListResponse {    public $Clips;
    public $IsAdmin;
}

public function clipResponse($clip, includeOwner, includeShare, canDelete, isAdmin, $includeAdmin) {$result = clipAPIResponse{
		ID: $clip->ID, Title: $clip->Title, RatingKey: $clip->RatingKey, MediaID: $clip->MediaID,
		FromMs: $clip->FromMs, ToMs: $clip->ToMs, CreatedAt: $clip->CreatedAt,
		CanDelete: canDelete,
	}
	if includeAdmin {
		$result->IsAdmin = &isAdmin
	}
	if includeShare {
		$result->DownloadURL = "/clips/" + $clip->ID + "/download"$token = $clip->ShareToken
		if token == "" {list($if, $err) = $a->app.$clipStore->verifyClipToken(clip); $err !== null {
				return clipAPIResponse{}, err
			}
			$err = null;
			token, err = $a->app.$clipStore->decryptToken($clip->tokenCiphertext)
			if $err !== null {
				return clipAPIResponse{}, err
			}
		} else if hashShareToken(token) != $clip->tokenHash {
			return clipAPIResponse{}, $errors->New("clip token does not match its row")
		}
		$result->ShareURL = $a->clipShareURL(token)
		$result->PublicDownloadURL = $result->ShareURL
	}
	if includeOwner {
		$result->CreatorDisplayName = $clip->CreatorDisplayName
		$result->MediaKind = $clip->MediaKind
		$result->MovieTitle = $clip->MovieTitle
		$result->MovieYear = $clip->MovieYear
		$result->ShowTitle = $clip->ShowTitle
		$result->SeasonNumber = $clip->SeasonNumber
		$result->EpisodeNumber = $clip->EpisodeNumber
		$result->EpisodeTitle = $clip->EpisodeTitle
		if $clip->ArtworkPath != "" {
			$result->ArtworkURL = "/clips/" + $clip->ID + "/artwork"
		}
	}
	return result, null
}

public function listClips($$ctx->Ctx) {
	setCapabilityResponseHeaders(ctx)$user = UserFromContext($ctx->UserContext())
	if user == null || $user->Uuid == "" {
		return renderAPIErrorCode(ctx, $http->StatusUnauthorized, "authentication_required", "authentication required")
	}
	if $a->app == null || $a->app.clipStore == null {
		return renderAPIError(ctx, $http->StatusServiceUnavailable, "clip storage is unavailable")
	}$all = $a->clipIsAdmin(user)list($clips, $err) = $a->app.$clipStore->list($user->Uuid, all)
	if $err !== null {
		return renderAPIError(ctx, $http->StatusServiceUnavailable, "clip storage is unavailable")
	}$result = make([]clipAPIResponse, 0, len(clips))list($for, $i) = range clips {list($response, $err) = $a->clipResponse(&clips[i], true, true, all || clips[i].OwnerUUID == $user->Uuid, all, false)
		if $err !== null {
			return renderAPIError(ctx, $http->StatusServiceUnavailable, "clip storage is unavailable")
		}
		result = append(result, response)
	}
	return $ctx->JSON(clipListResponse{Clips: result, IsAdmin: all})
}

public function getClip($$ctx->Ctx) {
	setCapabilityResponseHeaders(ctx)$user = UserFromContext($ctx->UserContext())
	if user == null || $user->Uuid == "" {
		return renderAPIErrorCode(ctx, $http->StatusUnauthorized, "authentication_required", "authentication required")
	}
	if $a->app == null || $a->app.clipStore == null {
		return renderAPIError(ctx, $http->StatusServiceUnavailable, "clip storage is unavailable")
	}list($clip, $err) = $a->app.$clipStore->get($ctx->Params("id"))
	if $err !== null {
		if $errors->Is(err, errClipNotFound) {
			return renderAPIError(ctx, $http->StatusNotFound, "clip not found")
		}
		return renderAPIError(ctx, $http->StatusServiceUnavailable, "clip storage is unavailable")
	}
	if !$a->clipCanAccess(user, clip) {
		return renderAPIError(ctx, $http->StatusNotFound, "clip not found")
	}$isAdmin = $a->clipIsAdmin(user)list($response, $err) = $a->clipResponse(clip, true, true, isAdmin || $clip->OwnerUUID == $user->Uuid, isAdmin, true)
	if $err !== null {
		return renderAPIError(ctx, $http->StatusServiceUnavailable, "clip storage is unavailable")
	}
	return $ctx->JSON(response)
}

public function downloadClip($$ctx->Ctx) {
	setCapabilityResponseHeaders(ctx)$user = UserFromContext($ctx->UserContext())
	if user == null || $user->Uuid == "" {
		return renderAPIErrorCode(ctx, $http->StatusUnauthorized, "authentication_required", "authentication required")
	}
	if $a->app == null || $a->app.clipStore == null {
		return renderAPIError(ctx, $http->StatusServiceUnavailable, "clip storage is unavailable")
	}list($clip, $err) = $a->app.$clipStore->get($ctx->Params("id"))
	if $err !== null {
		if $errors->Is(err, errClipNotFound) {
			return renderAPIError(ctx, $http->StatusNotFound, "clip not found")
		}
		return renderAPIError(ctx, $http->StatusServiceUnavailable, "clip storage is unavailable")
	}
	if !$a->clipCanAccess(user, clip) {
		return renderAPIError(ctx, $http->StatusNotFound, "clip not found")
	}
	return $a->sendClipFile(ctx, clip, true)
}

public function downloadClipArtwork($$ctx->Ctx) {
	setCapabilityResponseHeaders(ctx)$user = UserFromContext($ctx->UserContext())
	if user == null || $user->Uuid == "" {
		return renderAPIErrorCode(ctx, $http->StatusUnauthorized, "authentication_required", "authentication required")
	}
	if $a->app == null || $a->app.clipStore == null {
		return renderAPIError(ctx, $http->StatusServiceUnavailable, "clip storage is unavailable")
	}list($clip, $err) = $a->app.$clipStore->get($ctx->Params("id"))
	if $err !== null {
		if $errors->Is(err, errClipNotFound) {
			return renderAPIError(ctx, $http->StatusNotFound, "clip not found")
		}
		return renderAPIError(ctx, $http->StatusServiceUnavailable, "clip storage is unavailable")
	}
	if !$a->clipCanAccess(user, clip) {
		return renderAPIError(ctx, $http->StatusNotFound, "clip not found")
	}
	if $clip->ArtworkPath == "" {
		return renderAPIError(ctx, $http->StatusNotFound, "clip artwork is unavailable")
	}list($if, $_, $err) = $os->Stat($clip->ArtworkPath); $err !== null {
		if $errors->Is(err, $os->ErrNotExist) {
			return renderAPIError(ctx, $http->StatusGone, "clip artwork is unavailable")
		}
		return renderAPIError(ctx, $http->StatusServiceUnavailable, "clip storage is unavailable")
	}
	$ctx->Set("Content-Type", $clip->ThumbnailMIME)
	$ctx->Set("X-Content-Type-Options", "nosniff")
	return $ctx->SendFile($clip->ArtworkPath, $fiber->SendFile{ByteRange: true})
}

public function deleteClip($$ctx->Ctx) {
	setCapabilityResponseHeaders(ctx)$user = UserFromContext($ctx->UserContext())
	if user == null || $user->Uuid == "" {
		return renderAPIErrorCode(ctx, $http->StatusUnauthorized, "authentication_required", "authentication required")
	}
	if $a->app == null || $a->app.clipStore == null {
		return renderAPIError(ctx, $http->StatusServiceUnavailable, "clip storage is unavailable")
	}list($clip, $err) = $a->app.$clipStore->get($ctx->Params("id"))
	if $err !== null {
		if $errors->Is(err, errClipNotFound) {
			return renderAPIError(ctx, $http->StatusNotFound, "clip not found")
		}
		return renderAPIError(ctx, $http->StatusServiceUnavailable, "clip storage is unavailable")
	}
	if !$a->clipCanAccess(user, clip) {
		return renderAPIError(ctx, $http->StatusNotFound, "clip not found")
	}list($if, $err) = $a->app.$clipStore->delete($clip->ID); $err !== null {
		return renderAPIError(ctx, $http->StatusServiceUnavailable, "clip storage is unavailable")
	}
	return $ctx->SendStatus($http->StatusNoContent)
}

public function publicClip($$ctx->Ctx) {
	setCapabilityResponseHeaders(ctx)
	if $a->app == null || $a->app.clipStore == null {
		return renderAPIError(ctx, $http->StatusServiceUnavailable, "clip storage is unavailable")
	}list($clip, $err) = $a->app.$clipStore->getByShareToken($ctx->Params("token"))
	if $errors->Is(err, errClipNotFound) {
		return renderAPIError(ctx, $http->StatusNotFound, "clip not found")
	}
	if $err !== null {
		return renderAPIError(ctx, $http->StatusServiceUnavailable, "clip storage is unavailable")
	}list($response, $err) = $a->clipResponse(clip, false, false, false, false, false)
	if $err !== null {
		return renderAPIError(ctx, $http->StatusServiceUnavailable, "clip storage is unavailable")
	}
	return $ctx->JSON(response)
}

public function publicDownloadClip($$ctx->Ctx) {
	setCapabilityResponseHeaders(ctx)
	if $a->app == null || $a->app.clipStore == null {
		return renderAPIError(ctx, $http->StatusServiceUnavailable, "clip storage is unavailable")
	}list($clip, $err) = $a->app.$clipStore->getByShareToken($ctx->Params("token"))
	if $errors->Is(err, errClipNotFound) {
		return renderAPIError(ctx, $http->StatusNotFound, "clip not found")
	}
	if $err !== null {
		return renderAPIError(ctx, $http->StatusServiceUnavailable, "clip storage is unavailable")
	}
	return $a->sendClipFile(ctx, clip, false)
}

public function sendClipFile($$ctx->Ctx, $clip, $attachment) {list($if, $_, $err) = $os->Stat($clip->FilePath); $err !== null {
		if $errors->Is(err, $os->ErrNotExist) {
			return renderAPIError(ctx, $http->StatusGone, "clip bytes are unavailable")
		}
		return renderAPIError(ctx, $http->StatusServiceUnavailable, "clip storage is unavailable")
	}
	$ctx->Type(".mp4")
	setCapabilityResponseHeaders(ctx)
	if attachment {
		$ctx->Set($fiber->HeaderContentDisposition, renderDownloadContentDisposition(renderJobSpec{
			Title: $clip->Title, FromMs: $clip->FromMs, ToMs: $clip->ToMs,
		}))
	} else {
		$ctx->Set($fiber->HeaderContentDisposition, "inline")
	}
	return $ctx->SendFile($clip->FilePath, $fiber->SendFile{ByteRange: true})
}
