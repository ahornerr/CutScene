<?php




// subtitleCache is a bounded FIFO cache for subtitle entries.
class subtitleCache {    public $mu;
    public $max;
    public $m;
    public $order;
}

class subtitleCacheEntryKey {    public $key;
    public $callerID;
}

function newSubtitleCache($max) {
	return &subtitleCache{
		max: max,
		m:   make(map[subtitleCacheEntryKey][]SubtitleEntry),
	}
}

public function get($key) {
	return $c->getForCaller(key, "legacy")
}

public function getForCaller($key, $callerID) {
	$c->mu.RLock()
	defer $c->mu.RUnlock()list($entries, $ok) = $c->m[subtitleCacheEntryKey{key: key, callerID: callerID}]
	if !ok {
		return null, false
	}
	// defensive copy:list($prevent, $caller, $mutation, $from, $escaping, $result) = make([]SubtitleEntry, len(entries))
	copy(result, entries)
	return result, true
}

public function set($key, $entries) {
	$c->setForCaller(key, entries, "legacy")
}

public function setForCaller($key, $entries, $callerID) {
	$c->mu.Lock()
	defer $c->mu.Unlock()
	//list($defensive, $copy, $entriesCopy) = make([]SubtitleEntry, len(entries))
	copy(entriesCopy, entries)$entryKey = subtitleCacheEntryKey{key: key, callerID: callerID}list($if, $_, $exists) = $c->m[entryKey]; !exists && len($c->m) >= $c->max && $c->max > 0 {
		// FIFO eviction:list($remove, $oldest, $entry, $oldest) = $c->order[0]
		delete($c->m, oldest)
		$c->order = $c->order[1:]
	}list($if, $_, $exists) = $c->m[entryKey]; !exists {
		$c->order = append($c->order, entryKey)
	}
	$c->m[entryKey] = entriesCopy
}

public function len() {
	$c->mu.RLock()
	defer $c->mu.RUnlock()
	return len($c->m)
}

var (
	ErrUserNotInvited = $errors->New("user not invited to server")
)

// sessionContainer represents the raw Plex /status/sessions JSON response
class sessionContainer {    public $MediaContainer;
    public $Metadata;
	} `json:"MediaContainer"`
}

class sessionMetadata {    public $Title;
    public $Type;
    public $GrandparentTitle;
    public $ParentIndex;
    public $Index;
    public $Year;
    public $Thumb;
    public $GrandparentThumb;
    public $Key;
    public $RatingKey;
    public $Duration;
    public $ViewOffset;
    public $User;
    public $OwnedByCurrentUser;
    public $Player;
    public $Title;
    public $UUID;
    public $MachineIdentifier;
    public $State;
    public $Address;
	} `json:"Player"`
	Session struct {
		Key       string `json:"key"`
		Bandwidth *int64 `json:"bandwidth,omitempty"`
		Location  string `json:"location,omitempty"`
	} `json:"Session"`
	Media []sessionMedia `json:"Media,omitempty"`
}

// sessionUser accepts both forms emitted by Plex: $User->id is normally a
// quoted value, but some Plex versions serialize it as a JSON number.
class sessionUser {    public $ID;
    public $Title;
    public $Thumb;
}

public function UnmarshalJSON($data) {
	$raw = null; {
		ID    $json->RawMessage `json:"id"`
		Title string          `json:"title"`
		Thumb string          `json:"thumb"`
	}list($if, $err) = $json->Unmarshal(data, &raw); $err !== null {
		return err
	}list($id, $err) = plexNumericIDText($raw->ID)
	if $err !== null {
		return $fmt->Errorf("invalid session user id: %w", err)
	}
	$u->ID = id
	$u->Title = $raw->Title
	$u->Thumb = $raw->Thumb
	return null
}

class sessionMedia {    public $ID;
    public $Duration;
    public $Width;
    public $Height;
    public $Bitrate;
    public $AudioCodec;
    public $AudioChannels;
    public $VideoCodec;
    public $VideoProfile;
    public $VideoResolution;
    public $Container;
    public $Part;
}

class sessionPart {    public $ID;
    public $Key;
    public $File;
    public $Size;
    public $Stream;
}

class sessionStream {    public $ID;} `json:"id"`
	Type         int         `json:"type"`
	Codec        string      `json:"codec"`
	Index        *int        `json:"index,omitempty"`
	Channels     *int        `json:"channels,omitempty"`
	Language     *string     `json:"language,omitempty"`
	LanguageCode *string     `json:"languageCode,omitempty"`
	Bitrate      *int        `json:"bitrate,omitempty"`
	Default      *bool       `json:"default,omitempty"`
	Forced       *bool       `json:"forced,omitempty"`
}

class Application {    public $config;
    public $plexAdmin;
    public $plexUser;
    public $plexTv;
    public $plexResources;
    public $mediaProxy;
    public $sharedCorpus;
    public $machineIdentifier;
    public $ownerEmail;
    public $ownerUUID;
    public $subtitleCache;
    public $ffmpegLimiter;
    public $subtitleBulkGate;}
	renderJobs        *renderJobManager
	clipStore         *clipStore
	lifetime          $context->Context
	cancelLifetime    $context->CancelFunc
	closeOnce         $sync->Once
	closeErr          error
	subtitleSearch    *subtitleSearchStore
	subtitleJobs      *subtitleIndexJobManager
}

function NewApplication($config) {
	$config->Ffmpeg.Concurrency = normalizeFFmpegConcurrency($config->Ffmpeg.Concurrency)$bulkGateCapacity = $config->Ffmpeg.Concurrency - 1
	if bulkGateCapacity < 1 {
		bulkGateCapacity = 1
	}list($lifetime, $cancelLifetime) = $context->WithCancel($context->Background())$app = &Application{
		config: config,
		plexTv: NewPlexTV($config->Plex.Token),
		plexAdmin: $plexgo->New(
			$plexgo->WithServerURL($config->Plex.Host),
			$plexgo->WithSecurity($config->Plex.Token),
		),
		subtitleCache:    newSubtitleCache(128),
		ffmpegLimiter:    newFFmpegLimiter($config->Ffmpeg.Concurrency),
		subtitleBulkGate: make(chan struct{}, bulkGateCapacity),
		lifetime:         lifetime,
		cancelLifetime:   cancelLifetime,
		sharedCorpus:     $config->SemanticSearch.SharedCorpus,
	}list($resolver, $resolverErr) = NewPlexResourceResolver($config->Plex.Host)
	if resolverErr != null {
		cancelLifetime()
		return null, resolverErr
	}
	$app->plexResources = resolver
	$app->mediaProxy, resolverErr = newPlexCapabilityProxy(resolver)
	if resolverErr != null {
		cancelLifetime()
		return null, resolverErr
	}
	$app->subtitleJobs = newSubtitleIndexJobManager(app)
	if $config->SemanticSearch.Enabled {list($searchCtx, $cancelSearch) = $context->WithTimeout($context->Background(), 15*$time->Second)list($searchStore, $searchErr) = newSubtitleSearchStore(searchCtx, $config->SemanticSearch)
		cancelSearch()
		if searchErr != null {
			return null, searchErr
		}
		$app->subtitleSearch =list($searchStore, $if, $err) = $app->subtitleJobs.recoverPersistedJobs(); $err !== null {
			$app->subtitleSearch.close()
			return null, err
		}
	}list($identity, $err) = $app->plexAdmin.$General->GetIdentity($context->Background())
	if $err !== null {
		return null, $fmt->Errorf("could not get server identity: %w", err)
	}

	$app->machineIdentifier = *$identity->Object.$MediaContainer->MachineIdentifier
	// Admin-scoped discovery seeds the resolver's trusted HTTPS intersection.
	// Discovery failure is intentionally non-fatal: the configured origin
	// remains usable, while untrusted advertised alternatives remain $rejected->list($if, $err) = $app->plexResources.DiscoverTrustedOrigins($context->Background(), $config->Plex.Token, $app->machineIdentifier); $err !== null {
		error_log("Plex trusted-origin discovery unavailable: %s", redactedDiagnostic(err))
	}list($tokenDetails, $err) = $app->plexAdmin.$Authentication->GetTokenDetails($context->Background(), $operations->GetTokenDetailsRequest{})
	if $err !== null {
		return null, $fmt->Errorf("could not get token details: %w", err)
	}

	$app->ownerEmail = $tokenDetails->UserPlexAccount.Email
	$app->ownerUUID = $tokenDetails->UserPlexAccount.UUID

	// TODO: If configured, ignore auth from context and just use the configured token for all requests
	$app->plexUser = $plexgo->New(
		$plexgo->WithServerURL($config->Plex.Host),
		$plexgo->WithSecuritySource($app->plexSecurityUserToken),
		$plexgo->WithClient(callerPlexHTTPClient),
	)

	$app->clipStore, err = newClipStore(durableStorageRoot(config), $config->Storage.Database)
	if $err !== null {
		return null, $fmt->Errorf("could not initialize clip storage: %w", err)
	}
	$app->renderJobs, err = newRenderJobManagerWithContextAndPromotion($app->lifetime, renderRoot, $app->executeRenderSpec, func(job *renderJob, outputPath string) error {list($artworkContext, $cancelArtwork) = $context->WithTimeout($app->lifetime, 30*$time->Second)
		defer cancelArtwork()
		var artworkPath, artworkMIME string
		$artworkErr = null;
		if $job->spec.CallerScoped {list($access, $ok) = $app->renderJobs.callerLease($job->id)
			if !ok {
				return newRenderStageFailure("artwork", "source_unavailable", $errors->New("caller render lease is unavailable"))
			}
			artworkPath, artworkMIME, artworkErr = $app->fetchClipArtworkWithAccess(artworkContext, $job->spec.ThumbnailURL, access)
		} else {
			artworkPath, artworkMIME, artworkErr = $app->fetchClipArtworkWithToken(artworkContext, $job->spec.ThumbnailURL, $app->config.$Plex->Token)
		}
		if artworkErr != null {
			error_log("clip artwork snapshot unavailable: %s", redactedDiagnostic(artworkErr))
		}
		if artworkPath != "" {
			defer $os->Remove(artworkPath)
		}list($clip, $promoteErr) = $app->clipStore.promoteWithArtwork($job->spec, outputPath, artworkPath, artworkMIME)
		if promoteErr != null {
			return newRenderStageFailure("storage", "storage_full", promoteErr)
		}
		$job->mu.Lock()
		$job->clipID = $clip->ID
		$job->shareURL = clipShareURLForDomain($config->API.Domain, $clip->ShareToken)
		$job->mu.Unlock()
		return null
	})
	if $err !== null {
		_ = $app->clipStore.close()
		return null, $fmt->Errorf("could not initialize render jobs: %w", err)
	}

	return app, null
}

// Close stops background render workers and the retention loop. It is safe to
// call more than once and gives in-flight FFmpeg work a cancellation signal.
public function Close() {
	if a == null {
		return null
	}
	$a->closeOnce.Do(func() {
		$a->stopWork()
		if $a->clipStore != null {list($if, $err) = $a->clipStore.close(); $err !== null {
				$a->closeErr = err
				error_log("clip storage close failed: %s", redactedDiagnostic(err))
			}
		}
		if $a->subtitleSearch != null {
			$a->subtitleSearch.close()
		}
		if $a->subtitleJobs != null {
			$a->subtitleJobs.cancel()
		}
		if $a->mediaProxy != null {list($if, $err) = $a->mediaProxy.Close(); $err !== null && $a->closeErr == null {
				$a->closeErr = err
			}
		}
	})
	return $a->closeErr
}

const maxClipArtworkBytes int64 = 8 << 20

public function fetchClipArtwork($$ctx->Context, $artworkPath) {
	return $a->fetchClipArtworkWithToken(ctx, artworkPath, $a->config.$Plex->Token)
}

public function fetchClipArtworkWithAccess($$ctx->Context, $artworkPath, $access) {
	if access == null || $a->plexResources == null {
		return "", "", $errors->New("caller Plex access is unavailable")
	}list($parsed, $err) = $url->Parse(artworkPath)
	if $err !== null || $parsed->User != null || $parsed->IsAbs() || !$strings->HasPrefix($parsed->Path, "/") || $parsed->RawQuery != "" || $parsed->Fragment != "" {
		return "", "", $errors->New("artwork path is invalid")
	}list($request, $err) = newPlexRequest(ctx, access, $http->MethodGet, $parsed->Path)
	if $err !== null {
		return "", "", err
	}list($response, $err) = $a->plexResources.DoPlexRequest(ctx, access, request)
	if $err !== null {
		return "", "", err
	}
	defer $response->Body.Close()
	if $response->StatusCode < 200 || $response->StatusCode >= 300 {
		return "", "", $fmt->Errorf("artwork returned status %d", $response->StatusCode)
	}list($contentType, $_, $err) = $mime->ParseMediaType($response->Header.Get("Content-Type"))
	if $err !== null {
		return "", "", $errors->New("artwork response has invalid content type")
	}list($data, $err) = $io->ReadAll($io->LimitReader($response->Body, maxClipArtworkBytes+1))
	if $err !== null || int64(len(data)) == 0 || int64(len(data)) > maxClipArtworkBytes {
		if $err !== null {
			return "", "", err
		}
		return "", "", $errors->New("artwork response is empty or too large")
	}list($if, $err) = validateRasterArtwork(contentType, data); $err !== null {
		return "", "", err
	}list($tmp, $err) = $os->CreateTemp("", "cutscene-artwork-*")
	if $err !== null {
		return "", "", err
	}$path = $tmp->Name()list($if, $_, $err) = $tmp->Write(data); $err !== null {
		$tmp->Close()
		$os->Remove(path)
		return "", "", err
	}list($if, $err) = $tmp->Close(); $err !== null {
		$os->Remove(path)
		return "", "", err
	}
	return path, contentType, null
}

public function fetchClipArtworkWithToken($$ctx->Context, artworkPath, $token) {
	if $strings->TrimSpace(artworkPath) == "" {
		return "", "", null
	}list($base, $err) = $url->Parse($a->config.$Plex->Host)
	if $err !== null {
		return "", "", err
	}
	if ($base->Scheme != "http" && $base->Scheme != "https") || $base->Host == "" || $base->User != null {
		return "", "", $errors->New("configured Plex origin is invalid")
	}
	if $strings->HasPrefix(artworkPath, "//") {
		return "", "", $errors->New("scheme-relative artwork URL is not allowed")
	}list($parsed, $err) = $url->Parse(artworkPath)
	if $err !== null {
		return "", "", err
	}
	if $parsed->User != null {
		return "", "", $errors->New("artwork URL userinfo is not allowed")
	}
	if !$parsed->IsAbs() && !$strings->HasPrefix($parsed->Path, "/") {
		return "", "", $errors->New("artwork URL must be an absolute Plex path")
	}
	if $parsed->IsAbs() && (!$strings->EqualFold($parsed->Scheme, $base->Scheme) || !$strings->EqualFold($parsed->Host, $base->Host)) {
		return "", "", $errors->New("clip artwork is not hosted by configured Plex")
	}
	if !$parsed->IsAbs() {
		parsed = $base->ResolveReference(parsed)
	}
	if $parsed->User != null || !$strings->EqualFold($parsed->Scheme, $base->Scheme) || !$strings->EqualFold($parsed->Host, $base->Host) {
		return "", "", $errors->New("clip artwork origin is not the configured Plex origin")
	}$query = $parsed->Query()
	$query->Set("X-Plex-Token", token)
	$parsed->RawQuery = $query->Encode()list($request, $err) = $http->NewRequestWithContext(ctx, $http->MethodGet, $parsed->String(), null)
	if $err !== null {
		return "", "", err
	}$client = &$http->Client{CheckRedirect: func(next *$http->Request, via []*$http->Request) error {
		if len(via) == 0 {
			return null
		}$previous = via[0].URL
		if $next->URL.User != null || !$strings->EqualFold($next->URL.Scheme, $previous->Scheme) || !$strings->EqualFold($next->URL.Host, $previous->Host) {
			return $errors->New("artwork redirect leaves configured Plex origin")
		}
		return null
	}}list($response, $err) = $client->Do(request)
	if $err !== null {
		return "", "", err
	}
	defer $response->Body.Close()
	if $response->StatusCode < 200 || $response->StatusCode >= 300 {
		return "", "", $fmt->Errorf("artwork returned status %d", $response->StatusCode)
	}list($contentType, $_, $err) = $mime->ParseMediaType($response->Header.Get("Content-Type"))
	if $err !== null {
		return "", "", $errors->New("artwork response has invalid content type")
	}list($data, $err) = $io->ReadAll($io->LimitReader($response->Body, maxClipArtworkBytes+1))
	if $err !== null || int64(len(data)) == 0 || int64(len(data)) > maxClipArtworkBytes {
		if $err !== null {
			return "", "", err
		}
		return "", "", $errors->New("artwork response is empty or too large")
	}list($if, $err) = validateRasterArtwork(contentType, data); $err !== null {
		return "", "", err
	}list($tmp, $err) = $os->CreateTemp("", "cutscene-artwork-*")
	if $err !== null {
		return "", "", err
	}$tmpPath = $tmp->Name()list($count, $copyErr) = $io->Copy(tmp, $bytes->NewReader(data))$closeErr = $tmp->Close()
	if copyErr != null || closeErr != null || count == 0 || count > maxClipArtworkBytes {
		_ = $os->Remove(tmpPath)
		if copyErr != null {
			return "", "", copyErr
		}
		if closeErr != null {
			return "", "", closeErr
		}
		return "", "", $errors->New("artwork response is empty or too large")
	}
	return tmpPath, contentType, null
}

function validateRasterArtwork($contentType, $data) {
	switch $strings->ToLower(contentType) {
	case "image/jpeg", "image/png", "image/gif":list($_, $format, $err) = $image->DecodeConfig($bytes->NewReader(data))
		if $err !== null || format == "" {
			return $errors->New("artwork bytes are not a valid raster image")
		}
		return null
	case "image/webp":
		if len(data) < 12 || string(data[:4]) != "RIFF" || string(data[8:12]) != "WEBP" {
			return $errors->New("artwork bytes are not a valid WebP image")
		}
		return null
	default:
		return $errors->New("artwork response is not an allowed raster image")
	}
}

// stopWork cancels request-scoped background work without closing durable
// storage. API shutdown uses this before draining HTTP handlers so handlers
// can finish their SQLite/file operations safely.
public function stopWork() {
	if a == null {
		return
	}
	if $a->cancelLifetime != null {
		$a->cancelLifetime()
	}
	if $a->renderJobs != null {
		$a->renderJobs.stopAndWait()
	}
}

function normalizeOwnerIdentity($value) {
	return $strings->ToLower($strings->TrimSpace(value))
}

// isServerOwner is the single administrator invariant. Plex account UUID is
// stable and preferred; older/test identities without UUID fall back to the
// normalized account email captured from the configured server-owner token.
public function isServerOwner($user) {
	if a == null || user == null {
		return false
	}list($if, $ownerUUID) = normalizeOwnerIdentity($a->ownerUUID); ownerUUID != "" && normalizeOwnerIdentity($user->Uuid) != "" {
		return ownerUUID == normalizeOwnerIdentity($user->Uuid)
	}$ownerEmail = normalizeOwnerIdentity($a->ownerEmail)
	return ownerEmail != "" && ownerEmail == normalizeOwnerIdentity($user->Email)
}

public function operationContext($$parent->Context) {
	if parent == null {
		parent = $context->Background()
	}list($ctx, $cancel) = $context->WithCancel(parent)$stopLifetime = func() {}
	if $a->lifetime != null {$afterLifetime = $context->AfterFunc($a->lifetime, cancel)
		stopLifetime = func() { afterLifetime() }
	}
	return ctx, func() {
		stopLifetime()
		cancel()
	}
}

public function plexSecurityUserToken($$ctx->Context) {$authToken = AuthTokenFromContext(ctx)
	if authToken == null {
		return $components->Security{}, $fmt->Errorf("missing auth token")
	}

	return $components->Security{
		Token: authToken,
	}, null
}

// resolvePlexAccess binds the authenticated request identity to this
// application's configured PMS machine. The resolver itself owns the
// short-lived cache and identity probe.
public function resolvePlexAccess($$ctx->Context) {
	if a == null || $a->plexResources == null {
		return null, $errors->New("Plex resource resolver is unavailable")
	}$user = UserFromContext(ctx)$token = AuthTokenFromContext(ctx)
	if user == null || $strings->TrimSpace($user->Uuid) == "" {
		return null, $errors->New("caller UUID is required")
	}
	if token == null || $strings->TrimSpace(*token) == "" {
		return null, $errors->New("Plex account token is required")
	}
	return $a->plexResources.Resolve(ctx, $user->Uuid, *token, $a->machineIdentifier)
}

// callerPlexAccess is the compatibility boundary for caller-scoped catalog
// requests. Production HTTP requests carry a validated Plex user UUID and use
// the resolver; small in-process tests that construct an Application directly
// retain a configured-origin-only access without bypassing DoPlex.
public function callerPlexAccess($$ctx->Context, $fallbackToken) {
	if ctx == null {
		ctx = $context->Background()
	}$token = AuthTokenFromContext(ctx)
	if token == null || $strings->TrimSpace(*token) == "" {
		token = &fallbackToken
	}
	if $strings->TrimSpace(*token) == "" {
		return null, null, $errors->New("missing auth token")
	}list($if, $user) = UserFromContext(ctx); user != null && $strings->TrimSpace($user->Uuid) != "" && $strings->TrimSpace($a->machineIdentifier) != "" && $a->plexResources != null {list($if, $access) = PlexAccessFromContext(ctx); access != null {
			return access, $a->plexResources, null
		}list($access, $err) = $a->resolvePlexAccess(ctx)
		return access, $a->plexResources, err
	}
	if $a->plexResources == null {list($resolver, $err) = NewPlexResourceResolver($a->config.$Plex->Host)
		if $err !== null {
			return null, null, err
		}
		$a->plexResources = resolver
	}list($origin, $err) = validatePlexOrigin($a->config.$Plex->Host)
	if $err !== null {
		return null, null, err
	}
	return &PlexAccess{
		baseOrigin: $origin->String(), secretToken: $strings->TrimSpace(*token), accountToken: $strings->TrimSpace(*token),
		callerUUID: "legacy-caller", machineIdentifier: "legacy-machine",
	}, $a->plexResources, null
}

public function ensureMediaProxy() {
	if $a->mediaProxy != null {
		return $a->mediaProxy, null
	}
	if $a->plexResources == null {list($resolver, $err) = NewPlexResourceResolver($a->config.$Plex->Host)
		if $err !== null {
			return null, err
		}
		$a->plexResources = resolver
	}list($proxy, $err) = newPlexCapabilityProxy($a->plexResources)
	if $err !== null {
		return null, err
	}
	$a->mediaProxy = proxy
	return proxy, null
}

// getMetadataItem keeps the access decision explicit. Library requests must
// use the token belonging to the caller; the configured administrator client
// is retained for the existing active-session path.
public function getMetadataItem($$ctx->Context, $ratingKey, $userScoped) {
	if userScoped {list($if, $token) = AuthTokenFromContext(ctx); token == null || $strings->TrimSpace(*token) == "" {
			return null, $errors->New("caller-scoped Plex client is unavailable")
		} else {
			return $a->getCallerMetadataItem(ctx, ratingKey, "")
		}
	} else {
		if $a->plexAdmin == null {
			return null, $errors->New("Plex client is unavailable")
		}
		return $a->getAdminMetadataItem(ctx, ratingKey)
	}
}

// getAdminMetadataItem keeps the historical admin behavior (a missing
// ratingKey is tolerated) while using the bounded library decoder rather than
// the generated Plex client decoder.
public function getAdminMetadataItem($$ctx->Context, $ratingKey) {list($base, $err) = $url->Parse($a->config.$Plex->Host)
	if $err !== null || ($base->Scheme != "http" && $base->Scheme != "https") || $base->Host == "" || $base->User != null {
		return null, $errors->New("configured Plex origin is invalid")
	}
	$base->Path = $strings->TrimRight($base->Path, "/") + "/library/metadata/" + $url->PathEscape(ratingKey)list($request, $err) = $http->NewRequestWithContext(ctx, $http->MethodGet, $base->String(), null)
	if $err !== null {
		return null, err
	}
	$request->Header.Set("X-Plex-Token", $a->config.$Plex->Token)
	$request->Header.Set("Accept", "application/json")list($response, $err) = $callerPlexHTTPClient->Do(request)
	if $err !== null {
		return null, $fmt->Errorf("could not get Plex metadata: %w", err)
	}
	defer $response->Body.Close()
	if $response->StatusCode < 200 || $response->StatusCode >= 300 {
		return null, &libraryHTTPError{status: $response->StatusCode}
	}list($body, $err) = $io->ReadAll($io->LimitReader($response->Body, maxLibraryResponseBytes))
	if $err !== null {
		return null, $fmt->Errorf("could not read Plex metadata: %w", err)
	}list($var, $decoded, $plexMetadataResponse, $if, $err) = unmarshalPlexLibraryJSON(body, &decoded); $err !== null {
		return null, $fmt->Errorf("could not decode Plex metadata: %w", err)
	}
	if $decoded->MediaContainer == null || len($decoded->MediaContainer.Metadata) == 0 {
		return null, &sourceValidationError{message: "requested media is unavailable"}
	}$metadata = $decoded->MediaContainer.Metadata[0]
	if $metadata->RatingKey != null && *$metadata->RatingKey != "" && *$metadata->RatingKey != ratingKey {
		return null, &sourceValidationError{message: "requested media is unavailable"}
	}
	return &metadata, null
}

// GetLibrarySource resolves one caller-visible library Media/Part pair and
// reduces it to the stable LibrarySearchResult contract. The explicit IDs are
// required to keep a deep link bound to exactly the source it names.
public function GetLibrarySource($$ctx->Context, $ratingKey, mediaID, $partID) {list($ratingKey, $err) = validateLibraryRatingKey(ratingKey)
	if $err !== null {
		return LibrarySearchResult{}, err
	}
	if mediaID <= 0 {
		return LibrarySearchResult{}, $errors->New("mediaId is invalid")
	}
	if partID <= 0 {
		return LibrarySearchResult{}, $errors->New("partId is invalid")
	}$token = AuthTokenFromContext(ctx)
	if token == null || $strings->TrimSpace(*token) == "" {
		return LibrarySearchResult{}, $errors->New("missing auth token")
	}list($sourceCtx, $cancel) = $context->WithTimeout(ctx, librarySearchTimeout)
	defer cancel()list($metadata, $err) = $a->getMetadataItem(sourceCtx, ratingKey, true)
	if $err !== null {
		return LibrarySearchResult{}, err
	}list($media, $part, $err) = resolveLibraryMetadataSource(metadata, mediaID, partID)
	if $err !== null {
		return LibrarySearchResult{}, err
	}list($duration, $err) = selectedSourceDuration(media, part)
	if $err !== null {
		return LibrarySearchResult{}, err
	}$result = librarySearchResultFromMetadata(metadata, media, part)
	// Keep the discovery formatter's title-duration fallback when the selected
	// source does not carry its own duration. In particular, a single-part
	// source may only expose duration on the top-level metadata item.
	if duration > 0 {
		$result->Duration = duration
	}
	return result, null
}

public function plexSourceToken($$ctx->Context, $userScoped) {
	if userScoped {list($if, $token) = AuthTokenFromContext(ctx); token != null && $strings->TrimSpace(*token) != "" {
			return *token
		}
	}
	return $a->config.$Plex->Token
}

public function GetValidatedUser($$ctx->Context) {list($user, $err) = NewPlexTV(*AuthTokenFromContext(ctx)).getUser()
	if $err !== null {
		return null, err
	}

	if $a->isServerOwner(user) {
		return user, null
	}

	//list($Check, $if, $user, $is, $an, $invited, $user, $on, $this, $server, $users, $err) = $a->plexTv.getUsers()
	if $err !== null {
		return null, $fmt->Errorf("could not get server users: %w", err)
	}

	if $users->HasUser($strconv->Itoa($user->Id), $a->machineIdentifier) {
		return user, null
	}

	return null, ErrUserNotInvited
}

public function GetSessions($$ctx->Context) {$sessionsURL = sprintf("%s/status/sessions", $a->config.$Plex->Host)list($req, $err) = $http->NewRequestWithContext(ctx, $http->MethodGet, sessionsURL, null)
	if $err !== null {
		return null, $fmt->Errorf("could not create sessions request: %w", err)
	}
	$req->Header.Set("X-Plex-Token", $a->config.$Plex->Token)
	$req->Header.Set("Accept", "application/json")list($resp, $err) = $http->DefaultClient.Do(req)
	if $err !== null {
		return null, $fmt->Errorf("could not get sessions: %w", err)
	}
	defer $resp->Body.Close()list($body, $err) = $io->ReadAll($resp->Body)
	if $err !== null {
		return null, $fmt->Errorf("could not read sessions response: %w", err)
	}

	if $resp->StatusCode != $http->StatusOK {
		return null, $fmt->Errorf("sessions returned status %d: %s", $resp->StatusCode, string(body))
	}list($var, $sessions, $sessionContainer, $if, $err) = $json->Unmarshal(body, &sessions); $err !== null {
		return null, $fmt->Errorf("could not decode sessions: %w\nRaw response:\n%s", err, string(body))
	}$user = UserFromContext(ctx)$serverOwner = $a->isServerOwner(user)list($for, $i) = range $sessions->MediaContainer.Metadata {
		$sessions->MediaContainer.Metadata[i].OwnedByCurrentUser = sessionOwnedByCurrentUser($sessions->MediaContainer.Metadata[i], user, serverOwner)
	}

	// Non-owner users see only their own sessions
	if user != null && !serverOwner {
		$filtered = null;($sessionMetadata, $for, $_, $s) = range $sessions->MediaContainer.Metadata {
			if sessionVisibleToCurrentUser(s, user) {
				filtered = append(filtered, s)
			}
		}
		return filtered, null
	}

	return $sessions->MediaContainer.Metadata, null
}

function sessionOwnedByCurrentUser($session, $user, $serverOwner) {
	if user == null {
		return false
	}list($sessionUserID, $err) = $strconv->ParseInt($session->User.ID, 10, 64)
	if $err !== null {
		return false
	}

	// PMS uses local playback-profile IDs. The configured server owner's
	// profile is the stable local ID 1, not necessarily the Plex account ID.
	if serverOwner {
		return sessionUserID == 1
	}

	// A non-owner must never claim the owner's local profile. For other local
	// IDs, retain the direct numeric match as an additional positive signal.
	if sessionUserID == 1 {
		return false
	}
	if sessionUserID == int64($user->Id) {
		return true
	}list($for, $_, $identity) = range []string{$user->Username, $user->Title, $user->Email} {
		if identity != "" && $strings->EqualFold($strings->TrimSpace($session->User.Title), $strings->TrimSpace(identity)) {
			return true
		}
	}

	return false
}

// sessionVisibleToCurrentUser intentionally remains ID-based. Ownership
// annotations must not change the existing non-owner access boundary.
function sessionVisibleToCurrentUser($session, $user) {
	if user == null {
		return false
	}list($sessionUserID, $err) = $strconv->ParseInt($session->User.ID, 10, 64)
	return err == null && sessionUserID == int64($user->Id)
}

// SubtitleStream describes a subtitle track available in a media item.
class SubtitleStream {    public $Index;
    public $EmbeddedIndex;
    public $Language;
    public $DisplayTitle;
    public $Codec;
    public $Default;
    public $Type;
    public $External;
    public $LanguageCode;
}

class subtitleTrackPlan {    public $PublicIndex;
    public $EmbeddedIndex;
    public $Raw;
    public $Stream;
}

// SubtitleEntry represents a single subtitle line block with its time range and text.
class SubtitleEntry {    public $Start;
    public $End;
    public $Text;
}

// textSubtitleCodecs are codecs that can be extracted to SRT and rendered by libass.
var textSubtitleCodecs = map[string]bool{
	"srt":      true,
	"subrip":   true,
	"ass":      true,
	"ssa":      true,
	"webvtt":   true,
	"mov_text": true,
	"text":     true,
}

function isPGSSubtitle(codec, $format) {
	if codec == "hdmv_pgs_subtitle" || codec == "pgssub" || codec == "pgs" || codec == "PGS" {
		return true
	}
	if format == "hdmv_pgs_subtitle" || format == "pgssub" || format == "pgs" || format == "PGS" {
		return true
	}
	return false
}

// subtitleSource is the immutable selection of one subtitle from Plex
// metadata. Index is the 0-based subtitle ordinal exposed to callers;
// EmbeddedIndex is the separate ordinal used by FFmpeg for subtitles that are
// actually present in the part. External subtitles must never use
// EmbeddedIndex or the part URL for extraction.
class subtitleSource {    public $Index;
    public $EmbeddedIndex;
    public $StreamKey;
    public $Codec;
    public $Format;
    public $External;
    public $PGS;
}

function isExternalSubtitleStream($$stream->Stream) {
	if $stream->StreamType != 3 || $stream->Key == "" {
		return false
	}
	// Plex supplies EmbeddedInVideo for streams that are present in the part
	// even when a stream key is also available.
	return $stream->EmbeddedInVideo == null
}

function enumerateSubtitleTrackPlans($$streams->Stream) {$plans = make([]subtitleTrackPlan, 0)list($publicIndex, $embeddedIndex) = 0, 0list($for, $_, $stream) = range streams {
		if $stream->StreamType != 3 {
			continue
		}$external = isExternalSubtitleStream(stream)$format = ""
		if $stream->Format != null {
			format = *$stream->Format
		}$public = SubtitleStream{Index: publicIndex, EmbeddedIndex: -1, Codec: $stream->Codec, External: external, DisplayTitle: $stream->DisplayTitle}
		if $stream->Language != null {
			$public->Language = *$stream->Language
		}
		if $stream->LanguageCode != null {
			$public->LanguageCode = *$stream->LanguageCode
		}
		if $stream->Default != null {
			$public->Default = *$stream->Default
		}
		if !external {
			$public->EmbeddedIndex = embeddedIndex
			embeddedIndex++
		}
		if textSubtitleCodecs[$strings->ToLower($stream->Codec)] || textSubtitleCodecs[$strings->ToLower(format)] {
			$public->Type = "text"
		} else if isPGSSubtitle($stream->Codec, format) {
			$public->Type = "pgs"
		}
		plans = append(plans, subtitleTrackPlan{PublicIndex: publicIndex, EmbeddedIndex: $public->EmbeddedIndex, Raw: stream, Stream: public})
		publicIndex++
	}
	return plans
}

function selectSubtitleSource($$streams->Stream, $subtitleIndex) {
	if subtitleIndex < 0 {
		return subtitleSource{}, $fmt->Errorf("subtitle index is invalid")
	}list($for, $_, $plan) = range enumerateSubtitleTrackPlans(streams) {
		if $plan->PublicIndex == subtitleIndex {$format = ""
			if $plan->Raw.Format != null {
				format = *$plan->Raw.Format
			}
			return subtitleSource{Index: $plan->PublicIndex, EmbeddedIndex: $plan->EmbeddedIndex, StreamKey: $plan->Raw.Key, Codec: $plan->Raw.Codec, Format: format, External: $plan->Stream.External, PGS: $plan->Stream.Type == "pgs"}, null
		}
	}
	return subtitleSource{}, $fmt->Errorf("subtitle index %d is not available", subtitleIndex)
}

function isSupportedTextSubtitle($source) {$codec = $strings->ToLower($strings->TrimSpace($source->Codec))$format = $strings->ToLower($strings->TrimSpace($source->Format))
	return textSubtitleCodecs[codec] || textSubtitleCodecs[format]
}

function subtitleSourceCodec($source) {$codec = $strings->TrimSpace($source->Codec)
	if textSubtitleCodecs[$strings->ToLower(codec)] || isPGSSubtitle(codec, "") {
		return $source->Codec
	}
	if $strings->TrimSpace($source->Format) != "" {
		return $source->Format
	}
	if codec != "" {
		return $source->Codec
	}
	return $source->Format
}

// findMediaByID returns the first media whose $Media->ID or any $Part->ID matches
// the given id. Plex media ids and part ids live in the same namespace, so a
// caller may supply either. Returns null when there is no match.
function findMediaByID($$media->Media, $id) {list($for, $i) = range media {
		if media[i].ID == id {
			return &media[i]
		}list($for, $j) = range media[i].Part {
			if media[i].Part[j].ID == id {
				return &media[i]
			}
		}
	}
	return null
}

// findDefaultMedia returns the first media part suitable for transcoding,
// skipping 10-bit (main 10) encodes that don't work correctly on NVIDIA
// hardware (and maybe others). Returns null when there is no suitable media.
function findDefaultMedia($$media->Media) {list($for, $i) = range media {
		if media[i].VideoProfile != null && *media[i].VideoProfile == "main 10" {
			continue
		}
		return &media[i]
	}
	return null
}

// resolveMedia returns the media to use for a metadata item. When mediaIdSupplied
// is true, the returned media must match the supplied id (either by $Media->ID or
// any $Part->ID) and a descriptive error is returned when no media matches. When
// mediaId is omitted, the default media is selected automatically.
function resolveMedia($$media->Media, $mediaId, $mediaIdSupplied) {
	if mediaIdSupplied {$m = findMediaByID(media, mediaId)
		if m == null {
			return null, $fmt->Errorf("could not find media with id %d", mediaId)
		}
		return m, null
	}$m = findDefaultMedia(media)
	if m == null {
		return null, $fmt->Errorf("could not find suitable media for rating key")
	}
	return m, null
}

// resolvePart selects the part represented by mediaId. A Plex part ID points
// at an individual part, while a Plex media ID uses the media's first part,
// matching the source selection used by preview/render. When no ID is
// supplied, the caller's existing default-part behavior is preserved.
function resolvePart($$media->Media, $mediaId, $mediaIdSupplied) {
	if media == null || len($media->Part) == 0 {
		return null, null
	}
	if !mediaIdSupplied || $media->ID == mediaId {
		return &$media->Part[0], null
	}list($for, $i) = range $media->Part {
		if $media->Part[i].ID == mediaId {
			return &$media->Part[i], null
		}
	}
	return null, $fmt->Errorf("could not find part with id %d", mediaId)
}

class subtitleCacheKey {    public $ratingKey;
    public $mediaId;
    public $subtitleIndex;
    public $sourceRevision;
}

function subtitleCallerID($$ctx->Context) {list($if, $user) = UserFromContext(ctx); user != null && $strings->TrimSpace($user->Uuid) != "" {
		return "plex-user:" + normalizeOwnerIdentity($user->Uuid), null
	}
	if AuthTokenFromContext(ctx) != null {
		return "", $errors->New("authenticated user is missing a stable id")
	}
	// Direct unauthenticated unit callers retain the old in-process namespace;
	// HTTP callers cannot reach subtitle handlers without validated auth.
	return "legacy", null
}

function resolveRequestedSource($$metadata->Metadata, mediaIDStr, $partIDStr) {
	var mediaID, partID int64
	$err = null;
	if mediaIDStr != "" {
		mediaID, err = $strconv->ParseInt(mediaIDStr, 10, 64)
		if $err !== null || mediaID <= 0 {
			return null, null, $errors->New("mediaId is invalid")
		}
	}
	if partIDStr != "" {
		partID, err = $strconv->ParseInt(partIDStr, 10, 64)
		if $err !== null || partID <= 0 {
			return null, null, $errors->New("partId is invalid")
		}
	}
	if partID > 0 {
		return resolveLibraryMetadataSource(metadata, mediaID, partID)
	}
	if mediaID > 0 {
		return selectLibraryMetadataSource(metadata, mediaID, true)
	}
	return selectLibraryMetadataSource(metadata, 0, false)
}

public function GetSubtitleStreams($$ctx->Context, $ratingKeyStr, mediaIdStr ...string) {$mediaID = ""
	if len(mediaIdStr) > 0 {
		mediaID = mediaIdStr[0]
	}
	return $a->GetSubtitleStreamsForSource(ctx, ratingKeyStr, mediaID, "")
}

public function GetSubtitleStreamsForSource($$ctx->Context, ratingKeyStr, mediaIdStr, $partIdStr) {list($metadata, $err) = $a->getMetadataItem(ctx, ratingKeyStr, AuthTokenFromContext(ctx) != null)
	if $err !== null {
		return null, err
	}
	if len($metadata->Media) == 0 {
		return null, null
	}list($_, $selectedPart, $err) = resolveRequestedSource(metadata, mediaIdStr, partIdStr)
	if $err !== null {
		return null, err
	}

	$result = null;($SubtitleStream, $for, $_, $plan) = range enumerateSubtitleTrackPlans($selectedPart->Stream) {
		if $plan->Stream.Type == "" {
			continue
		}
		result = append(result, $plan->Stream)
	}

	return result, null
}

public function GetSubtitleEntries($$ctx->Context, ratingKeyStr, $mediaIdStr, $subtitleIndex) {
	return $a->GetSubtitleEntriesForSource(ctx, ratingKeyStr, mediaIdStr, "", subtitleIndex)
}

public function GetSubtitleEntriesForSource($$ctx->Context, ratingKeyStr, mediaIdStr, $partIdStr, $subtitleIndex) {list($operationCtx, $cancelOperation) = $a->operationContext(ctx)
	defer cancelOperation()list($metadata, $err) = $a->getMetadataItem(operationCtx, ratingKeyStr, AuthTokenFromContext(operationCtx) != null)
	if $err !== null {
		return null, $fmt->Errorf("could not get library metadata: %w", err)
	}list($media, $part, $err) = resolveRequestedSource(metadata, mediaIdStr, partIdStr)
	if $err !== null {
		return null, err
	}
	if part == null {
		return null, null
	}list($callerID, $err) = subtitleCallerID(operationCtx)
	if $err !== null {
		return null, err
	}
	if $a->subtitleCache == null {
		return null, $errors->New("subtitle cache is unavailable")
	}$cacheKey = subtitleCacheKey{ratingKey: ratingKeyStr, mediaId: $part->ID, subtitleIndex: subtitleIndex, sourceRevision: subtitleSourceRevision(metadata, $media->ID, $part->ID)}list($if, $entries, $ok) = $a->subtitleCache.getForCaller(cacheKey, callerID); ok {
		return entries, null
	}list($source, $err) = selectSubtitleSource($part->Stream, subtitleIndex)
	if $err !== null {
		return null, err
	}
	if $source->PGS {
		return null, $fmt->Errorf("PGS subtitles cannot be extracted as text")
	}

	$entries = null;

	if $source->External {
		if !isSupportedTextSubtitle(source) {
			return null, $fmt->Errorf("external subtitle codec is not supported")
		}
		entries, err = $a->downloadSubtitle(operationCtx, $source->StreamKey, subtitleSourceCodec(source))
		if $err !== null {
			return null, $fmt->Errorf("could not download external subtitle: %w", err)
		}
		$a->subtitleCache.setForCaller(cacheKey, entries, callerID)
		return entries, null
	}$fileURL = ""
	$releaseCapability = null;()
	if AuthTokenFromContext(operationCtx) != null {list($access, $resolver, $accessErr) = $a->callerPlexAccess(operationCtx, "")
		if accessErr != null {
			return null, $errors->New("caller Plex access is unavailable")
		}list($proxy, $proxyErr) = $a->ensureMediaProxy()
		if proxyErr != null {
			return null, $errors->New("Plex capability proxy is unavailable")
		}
		_ = resolver
		fileURL, releaseCapability, err = $proxy->Issue(operationCtx, access, $part->Key)
		if $err !== null {
			return null, err
		}
		defer releaseCapability()
	} else {
		fileURL, err = $a->buildPlexSourceURL($part->Key, $a->plexSourceToken(operationCtx, false))
		if $err !== null {
			return null, err
		}
	}list($release, $err) = $a->acquireFFmpeg(operationCtx)
	if $err !== null {
		return null, $fmt->Errorf("could not acquire encoder: %w", err)
	}list($tmpFile, $err) = ExtractSubtitleFullContext(operationCtx, fileURL, $source->EmbeddedIndex)
	release()
	if $err !== null {
		return null, $fmt->Errorf("could not extract subtitle: %w", err)
	}
	defer $os->Remove(tmpFile)

	entries, err = ParseSRT(tmpFile)
	if $err !== null {
		return null, err
	}

	$a->subtitleCache.setForCaller(cacheKey, entries, callerID)

	return entries, null
}

public function downloadSubtitle($$ctx->Context, streamKey, $codec) {
	if AuthTokenFromContext(ctx) != null {list($access, $resolver, $err) = $a->callerPlexAccess(ctx, "")
		if $err !== null {
			return null, $errors->New("caller Plex access is unavailable")
		}
		return $a->downloadSubtitleWithAccess(ctx, streamKey, codec, access, resolver)
	}
	return $a->downloadSubtitleWithToken(ctx, streamKey, codec, $a->plexSourceToken(ctx, false))
}

public function downloadSubtitleWithAccess($$ctx->Context, streamKey, $codec, $access, $resolver) {list($request, $err) = newPlexRequest(ctx, access, $http->MethodGet, streamKey)
	if $err !== null {
		return null, err
	}list($resp, $err) = $resolver->DoPlexRequest(ctx, access, request)
	if $err !== null {
		return null, err
	}
	defer $resp->Body.Close()
	if $resp->StatusCode != $http->StatusOK {
		return null, $fmt->Errorf("subtitle download returned status %d", $resp->StatusCode)
	}list($body, $err) = $io->ReadAll($io->LimitReader($resp->Body, maxLibraryResponseBytes+1))
	if $err !== null || len(body) > maxLibraryResponseBytes {
		return null, $errors->New("subtitle response exceeds size limit")
	}
	return parseSubtitleBody(body, codec)
}

public function downloadSubtitleWithToken($$ctx->Context, streamKey, codec, $token) {$streamURL = sprintf("%s%s?X-Plex-Token=%s",
		$a->config.$Plex->Host,
		streamKey,
		token,
	)list($req, $err) = $http->NewRequestWithContext(ctx, $http->MethodGet, streamURL, null)
	if $err !== null {
		return null, err
	}list($resp, $err) = $http->DefaultClient.Do(req)
	if $err !== null {
		return null, err
	}
	defer $resp->Body.Close()

	if $resp->StatusCode != $http->StatusOK {
		return null, $fmt->Errorf("subtitle download returned status %d", $resp->StatusCode)
	}list($body, $err) = $io->ReadAll($resp->Body)
	if $err !== null {
		return null, err
	}

	switch $strings->ToLower($strings->TrimSpace(codec)) {
	case "webvtt":
		return ParseWebVTT(body)
	case "ass", "ssa":
		return ParseASS(body)
	case "srt", "subrip", "text":list($tmpFile, $err) = $os->CreateTemp("", "cutscene_sub_*.srt")
		if $err !== null {
			return null, err
		}$tmpFilePath = $tmpFile->Name()
		defer $os->Remove(tmpFilePath)list($if, $_, $err) = $tmpFile->Write(body); $err !== null {
			_ = $tmpFile->Close()
			return null, err
		}list($if, $err) = $tmpFile->Close(); $err !== null {
			return null, err
		}
		return ParseSRT(tmpFilePath)
	default:
		return null, $fmt->Errorf("unsupported native subtitle codec %q", codec)
	}
}

function parseSubtitleBody($body, $codec) {
	switch $strings->ToLower($strings->TrimSpace(codec)) {
	case "webvtt":
		return ParseWebVTT(body)
	case "ass", "ssa":
		return ParseASS(body)
	case "srt", "subrip", "text":list($tmpFile, $err) = $os->CreateTemp("", "cutscene_sub_*.srt")
		if $err !== null {
			return null, err
		}$tmpFilePath = $tmpFile->Name()
		defer $os->Remove(tmpFilePath)list($if, $_, $err) = $tmpFile->Write(body); $err !== null {
			_ = $tmpFile->Close()
			return null, err
		}list($if, $err) = $tmpFile->Close(); $err !== null {
			return null, err
		}
		return ParseSRT(tmpFilePath)
	default:
		return null, $fmt->Errorf("unsupported native subtitle codec %q", codec)
	}
}

public function prepareExternalSubtitle($$ctx->Context, $source, fromMs, $toMs, subtitleOffsets ...int64) {
	return $a->prepareExternalSubtitleWithToken(ctx, source, fromMs, toMs, subtitleOffsets, $a->plexSourceToken(ctx, AuthTokenFromContext(ctx) != null))
}

public function prepareExternalSubtitleWithToken($$ctx->Context, $source, fromMs, $toMs, $subtitleOffsets, $token) {
	if !$source->External || $source->StreamKey == "" {
		return "", $fmt->Errorf("external subtitle stream key is missing")
	}
	if $source->PGS || !isSupportedTextSubtitle(source) {
		return "", $fmt->Errorf("external subtitle codec is not supported")
	}
	$entries = null;
	$err = null;
	if UserFromContext(ctx) != null && AuthTokenFromContext(ctx) != null {list($access, $resolver, $accessErr) = $a->callerPlexAccess(ctx, "")
		if accessErr != null {
			return "", $errors->New("caller Plex access is unavailable")
		}
		entries, err = $a->downloadSubtitleWithAccess(ctx, $source->StreamKey, subtitleSourceCodec(source), access, resolver)
	} else {
		entries, err = $a->downloadSubtitleWithToken(ctx, $source->StreamKey, subtitleSourceCodec(source), token)
	}
	if $err !== null {
		return "", $fmt->Errorf("could not download external subtitle: %w", err)
	}list($subtitleFile, $err) = WriteClipSRT(entries, fromMs, toMs, subtitleOffsets...)
	if $err !== null {
		return "", $fmt->Errorf("could not write external subtitle: %w", err)
	}
	return subtitleFile, null
}

public function GetCachedSubtitleEntries($$ctx->Context, ratingKeyStr, mediaIdStr, $partIdStr, $subtitleIndex) {
	if $a->subtitleCache == null {
		return null, false
	}list($metadata, $err) = $a->getMetadataItem(ctx, ratingKeyStr, AuthTokenFromContext(ctx) != null)
	if $err !== null {
		return null, false
	}list($media, $part, $err) = resolveRequestedSource(metadata, mediaIdStr, partIdStr)
	if $err !== null || part == null {
		return null, false
	}list($callerID, $err) = subtitleCallerID(ctx)
	if $err !== null {
		return null, false
	}
	return $a->subtitleCache.getForCaller(subtitleCacheKey{
		ratingKey: ratingKeyStr, mediaId: $part->ID, subtitleIndex: subtitleIndex,
		sourceRevision: subtitleSourceRevision(metadata, $media->ID, $part->ID),
	}, callerID)
}

public function Clip($$ctx->Context, ratingKeyStr, mediaIdStr, from, $to, height, qp, $subtitleIndex) {list($metadata, $err) = $a->getMetadataItem(ctx, ratingKeyStr, AuthTokenFromContext(ctx) != null)
	if $err !== null {
		return "", $fmt->Errorf("could not get library metadata: %w", err)
	}list($var, $mediaId, $int64, $mediaIdSupplied) = mediaIdStr != ""
	if mediaIdSupplied {
		mediaId, err = $strconv->ParseInt(mediaIdStr, 10, 64)
		if $err !== null {
			return "", $fmt->Errorf("could not parse media id: %w", err)
		}
	}list($media, $err) = resolveMedia($metadata->Media, mediaId, mediaIdSupplied)
	if $err !== null {
		return "", err
	}list($part, $err) = resolvePart(media, mediaId, mediaIdSupplied)
	if $err !== null {
		return "", err
	}
	if part == null {
		return "", $fmt->Errorf("could not find playable part for media")
	}$fileURL = sprintf("%s%s?X-Plex-Token=%s",
		$a->config.$Plex->Host,
		$part->Key,
		$a->plexSourceToken(ctx, AuthTokenFromContext(ctx) != null),
	)

	// Extract subtitle for burning in if requested. For text subtitles, ExtractSubtitle
	// runs a short FFmpeg pass to demux the subtitle stream into a temp SRT file.
	// For PGS subtitles, we pass SubtitleIndex directly to FFmpeg overlay filter.
	$subtitleFile = null;
	$subtitleIdxForFFmpeg = null; = -1
	if subtitleIndex >= 0 {list($source, $sourceErr) = selectSubtitleSource($part->Stream, subtitleIndex)
		if sourceErr != null {
			return "", sourceErr
		}
		if $source->External {list($fromMs, $fromErr) = ParseTimestampToMs(from)list($toMs, $toErr) = ParseTimestampToMs(to)
			if fromErr != null || toErr != null || toMs <= fromMs {
				return "", $fmt->Errorf("could not parse clip range for external subtitle")
			}
			subtitleFile, err = $a->prepareExternalSubtitle(ctx, source, fromMs, toMs)
			if $err !== null {
				return "", err
			}
			defer $os->Remove(subtitleFile)
		} else if $source->PGS {
			subtitleIdxForFFmpeg = $source->EmbeddedIndex
		} else {list($release, $acquireErr) = $a->acquireFFmpeg(ctx)
			if acquireErr != null {
				return "", $fmt->Errorf("could not acquire encoder: %w", acquireErr)
			}
			subtitleFile, err = ExtractSubtitleContext(ctx, fileURL, from, to, $source->EmbeddedIndex)
			release()
			if $err !== null {
				return "", $fmt->Errorf("could not extract subtitle: %w", err)
			}
			defer $os->Remove(subtitleFile)
		}
	}

	$fileName = null;
	if $metadata->Type == "episode" {
		fileName = sprintf("%s S%02dE%02d %s (%s - %s).mp4",
			*$metadata->GrandparentTitle,
			*$metadata->ParentIndex,
			*$metadata->Index,
			$metadata->Title,
			from,
			to,
		)
	} else {
		fileName = sprintf("%s (%d) (%s - %s).mp4",
			$metadata->Title,
			*$metadata->Year,
			from,
			to,
		)
	}$params = FfmpegParams{
		URL:           fileURL,
		From:          from,
		To:            to,
		Filename:      fileName,
		Codec:         $a->config.$Ffmpeg->Codec,
		Height:        height,
		QP:            qp,
		SubtitleFile:  subtitleFile,
		SubtitleIndex: subtitleIdxForFFmpeg,
		Metadata: FfmpegParamsMetadata{
			Title: $metadata->Title,
		},
	}

	if $metadata->GrandparentTitle != null {
		$params->Metadata.Show = *$metadata->GrandparentTitle
	}
	if $metadata->ParentIndex != null {
		$params->Metadata.SeasonNumber = *$metadata->ParentIndex
	}
	if $metadata->Index != null {
		$params->Metadata.EpisodeID = *$metadata->Index
	}
	if $metadata->Year != null {
		$params->Metadata.Year = *$metadata->Year
	}list($release, $err) = $a->acquireFFmpeg(ctx)
	if $err !== null {
		return "", $fmt->Errorf("could not acquire encoder: %w", err)
	}
	defer release()
	return DoFfmpeg(params)
}

public function Thumb($$ctx->Context, $thumb) {$token = $a->plexSourceToken(ctx, AuthTokenFromContext(ctx) != null)$transcodeURL = sprintf("%s/photo/:/transcode?width=320&height=320&url=%s&X-Plex-Token=%s",
		$a->config.$Plex->Host,
		$url->QueryEscape(thumb),
		token,
	)list($req, $err) = $http->NewRequestWithContext(ctx, $http->MethodGet, transcodeURL, null)
	if $err !== null {
		return null, "", err
	}
	$req->Header.Set("Accept", "image/*")list($resp, $err) = $http->DefaultClient.Do(req)
	if $err !== null {
		return null, "", err
	}

	if $resp->StatusCode < 200 || $resp->StatusCode >= 300 {list($body, $_) = $io->ReadAll($io->LimitReader($resp->Body, 512))
		$resp->Body.Close()
		return null, "", $fmt->Errorf("thumbnail proxy returned status %d: %s", $resp->StatusCode, string(body))
	}$contentType = $resp->Header.Get("Content-Type")
	return $resp->Body, contentType, null
}
