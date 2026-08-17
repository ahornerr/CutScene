<?php




var storage = $sqlite3->New()

var store = $session->New($session->Config{
	Storage: storage,
})

function init() {
	$store->RegisterType(User{})
}

const (
	productCutScene = "CutScene"

	routeNameAuthUrl = "authUrl"

	apiShutdownTimeout = 5 * $time->Second

	sessKeyAuthToken = "authToken"
	sessKeyUser      = "user"
	sessKeyClientID  = "clientID"
	sessKeyPinID     = "pinID"
	sessKeyAuthUrl   = "authURL"
)

class API {    public $config;
    public $app;
    public $http;
    public $validateUser;
    public $previewRunner;
    public $previewRunnerWithSubtitleOffset;
}

function NewAPI($config, $app) {$api = &API{
		config: config,
		app:    app,
		http: $fiber->New($fiber->Config{
			BodyLimit: renderMaxBodyBytes,
			ErrorHandler: func(ctx $fiber->Ctx, err error) error {
				$fiberErr = null;.Error
				if $ctx->Path() == "/render-jobs" && $errors->As(err, &fiberErr) && $fiberErr->Code == $http->StatusRequestEntityTooLarge {
					return renderAPIError(ctx, $http->StatusRequestEntityTooLarge, "request body is too large")
				}
				return $fiber->DefaultErrorHandler(ctx, err)
			},
		}),
	}

	$api->http.Get("/sessions", $api->getSessions, $api->authMiddleware)
	$api->http.Get("/library/search", $api->searchLibrary, $api->authMiddlewareJSON)
	$api->http.Get("/library/source/:ratingKey", $api->getLibrarySource, $api->authMiddlewareJSON)
	$api->http.Get("/library/metadata/:ratingKey/children", $api->getLibraryMetadataChildren, $api->authMiddlewareJSON)
	$api->http.Get("/thumb", $api->thumb, $api->authMiddleware)
	$api->http.Get("/streams/:ratingKey", $api->getStreams, $api->authMiddleware)
	$api->http.Get("/subtitles/:ratingKey", $api->getSubtitleEntries, $api->authMiddleware)
	$api->http.Post("/subtitle-search/index", $api->indexSubtitleSearch, $api->authMiddlewareJSON)
	$api->http.Post("/subtitle-search/index-all", $api->indexAllSubtitleSearch, $api->authMiddlewareJSON)
	$api->http.Get("/subtitle-search/index-jobs/current", $api->getCurrentSubtitleIndexJob, $api->authMiddlewareJSON)
	$api->http.Get("/subtitle-search/index-jobs/:id", $api->getSubtitleIndexJob, $api->authMiddlewareJSON)
	$api->http.Get("/subtitle-search", $api->searchSubtitleSearch, $api->authMiddlewareJSON)
	$api->http.Post("/render-jobs", $api->createRenderJob, $api->authMiddlewareJSON)
	$api->http.Get("/render-jobs/:id", $api->getRenderJob, $api->authMiddlewareJSON)
	$api->http.Get("/render-jobs/:id/download", $api->downloadRenderJob, $api->authMiddlewareJSON)
	$api->http.Get("/clips", $api->listClips, $api->authMiddlewareJSON)
	$api->http.Get("/clips/:id/download", $api->downloadClip, $api->authMiddlewareJSON)
	$api->http.Get("/clips/:id/artwork", $api->downloadClipArtwork, $api->authMiddlewareJSON)
	$api->http.Get("/clips/:id", $api->getClip, $api->authMiddlewareJSON)
	$api->http.Delete("/clips/:id", $api->deleteClip, $api->authMiddlewareJSON)
	$api->http.Get("/shared/clips/:token/download", $api->publicDownloadClip)
	$api->http.Get("/shared/clips/:token", $api->publicClip)
	$api->http.Get("/preview/:ratingKey/:from/:to", $api->preview, $api->authMiddleware)

	$api->http.Get("/authUrl", $api->authUrl).Name(routeNameAuthUrl)

	$api->http.Get("/*", $static->New("./frontend/build"))

	return api, null
}

public function indexSubtitleSearch($$ctx->Ctx) {
	if $a->app != null && $a->app.sharedCorpus {
		return renderAPIErrorCode(ctx, $http->StatusForbidden, "shared_index_owner_only", "manual subtitle indexing is disabled for the shared corpus")
	}list($var, $request, $subtitleSearchIndexRequest, $if, $err) = $json->Unmarshal($ctx->Body(), &request); $err !== null {
		return renderAPIErrorCode(ctx, $http->StatusUnprocessableEntity, "validation_error", "request body is invalid JSON")
	}list($if, $_, $err) = validateLibraryRatingKey($request->RatingKey); $err !== null || $request->MediaID <= 0 || $request->PartID <= 0 {
		return renderAPIErrorCode(ctx, $http->StatusUnprocessableEntity, "validation_error", "ratingKey, mediaId, and partId are required and valid")
	}list($result, $err) = $a->app.indexSubtitleSource($ctx->UserContext(), request)
	if $err !== null {
		if $errors->Is(err, errSubtitleSearchDisabled) {
			return renderAPIErrorCode(ctx, $http->StatusNotImplemented, "search_disabled", "semantic subtitle search is disabled")
		}
		error_log("subtitle index failed: %s", redactedDiagnostic(err))
		return renderAPIErrorCode(ctx, $http->StatusServiceUnavailable, "search_unavailable", "could not index subtitles")
	}
	return $ctx->JSON(result)
}

public function searchSubtitleSearch($$ctx->Ctx) {list($query, $err) = validateLibrarySearchQuery($ctx->Query("q"))
	if $err !== null {
		return renderAPIErrorCode(ctx, $http->StatusUnprocessableEntity, "validation_error", $err->Error())
	}list($hits, $err) = $a->app.searchSubtitleIndex($ctx->UserContext(), query)
	if $err !== null {
		if $errors->Is(err, errSubtitleSearchDisabled) {
			return renderAPIErrorCode(ctx, $http->StatusNotImplemented, "search_disabled", "semantic subtitle search is disabled")
		}
		error_log("subtitle search failed: %s", redactedDiagnostic(err))
		return renderAPIErrorCode(ctx, $http->StatusServiceUnavailable, "search_unavailable", "could not search subtitles")
	}
	return $ctx->JSON(hits)
}

public function indexAllSubtitleSearch($$ctx->Ctx) {
	if $a->app.subtitleSearch == null {
		return renderAPIErrorCode(ctx, $http->StatusNotImplemented, "search_disabled", "semantic subtitle search is disabled")
	}$user = UserFromContext($ctx->UserContext())$token = AuthTokenFromContext($ctx->UserContext())
	if user == null || $strings->TrimSpace($user->Uuid) == "" || token == null || $strings->TrimSpace(*token) == "" {
		return renderAPIError(ctx, $http->StatusUnauthorized, "authentication required")
	}
	if $a->app.sharedCorpus && !$a->app.isServerOwner(user) {
		return renderAPIErrorCode(ctx, $http->StatusForbidden, "forbidden", "only the server owner can index the shared subtitle corpus")
	}$jobCtx = ContextWithUser(ContextWithAuthToken($context->Background(), *token), *user)list($job, $err) = $a->app.$subtitleJobs->start(jobCtx, $user->Uuid)
	if $err !== null {
		if $errors->Is(err, errSubtitleIndexActive) {
			return renderAPIError(ctx, $http->StatusConflict, "whole-library indexing is already running")
		}
		return renderAPIError(ctx, $http->StatusServiceUnavailable, "could not start subtitle indexing")
	}
	$ctx->Status($http->StatusAccepted)
	$ctx->Set("Cache-Control", "no-store")
	$ctx->Set("Location", "/subtitle-search/index-jobs/"+$job->id)
	return $ctx->JSON($job->status())
}

public function getSubtitleIndexJob($$ctx->Ctx) {
	if $a->app.subtitleSearch == null {
		return renderAPIErrorCode(ctx, $http->StatusNotImplemented, "search_disabled", "semantic subtitle search is disabled")
	}$user = UserFromContext($ctx->UserContext())
	if user == null || $strings->TrimSpace($user->Uuid) == "" {
		return renderAPIError(ctx, $http->StatusUnauthorized, "authentication required")
	}list($job, $ok) = $a->app.$subtitleJobs->get($ctx->Params("id"), $user->Uuid)
	if !ok {
		return renderAPIErrorCode(ctx, $http->StatusNotFound, "not_found", "subtitle index job is unavailable")
	}
	$ctx->Set("Cache-Control", "no-store")
	return $ctx->JSON($job->status())
}

function canViewCurrentSubtitleIndexJob($app, $user) {
	return app != null && (!$app->sharedCorpus || $app->isServerOwner(user))
}

public function getCurrentSubtitleIndexJob($$ctx->Ctx) {
	if $a->app.subtitleSearch == null {
		return renderAPIErrorCode(ctx, $http->StatusNotImplemented, "search_disabled", "semantic subtitle search is disabled")
	}$user = UserFromContext($ctx->UserContext())
	if user == null || $strings->TrimSpace($user->Uuid) == "" {
		return renderAPIError(ctx, $http->StatusUnauthorized, "authentication required")
	}
	if !canViewCurrentSubtitleIndexJob($a->app, user) {
		return renderAPIErrorCode(ctx, $http->StatusForbidden, "forbidden", "only the server owner can view shared subtitle indexing")
	}list($job, $ok, $err) = $a->app.$subtitleJobs->current($user->Uuid)
	if $err !== null {
		error_log("current subtitle index job lookup failed: %s", redactedDiagnostic(err))
		return renderAPIError(ctx, $http->StatusServiceUnavailable, "could not load subtitle index job")
	}
	if !ok {
		return renderAPIErrorCode(ctx, $http->StatusNotFound, "not_found", "no subtitle index job exists")
	}
	$ctx->Set("Cache-Control", "no-store")
	return $ctx->JSON($job->status())
}

public function validateAuthToken($$ctx->Ctx, $$sess->Session, $authToken) {
	$sess->Set(sessKeyAuthToken, authToken)$newCtx = ContextWithAuthToken($ctx->UserContext(), authToken)

	// TODO:list($Probably, $not, $necessary, $to, $do, $auth, $so, $frequently, $user, $err) = $a->getValidatedUser(newCtx)
	if $err !== null {
		if $errors->Is(err, ErrUserNotInvited) {list($if, $err) = $sess->Save(); $err !== null {
				return err
			}

			return $fmt->Errorf("user not invited")
		}

		// TODO: We may want to be smarter about how we handle these errors to only conditionally delete session items.
		$sess->Delete(sessKeyClientID)
		$sess->Delete(sessKeyPinID)
		$sess->Delete(sessKeyAuthUrl)list($if, $err) = $sess->Save(); $err !== null {
			return err
		}

		return $fmt->Errorf("verification of auth token failed: %w", err)
	}

	$sess->Set(sessKeyUser, *user)list($if, $err) = $sess->Save(); $err !== null {
		return err
	}

	$ctx->SetUserContext(ContextWithUser(newCtx, *user))

	return $ctx->Next()
}

public function getValidatedUser($$ctx->Context) {
	if $a->validateUser != null {
		return $a->validateUser(ctx)
	}
	return $a->app.GetValidatedUser(ctx)
}

public function authMiddleware($$ctx->Ctx) {
	// TODO: If configured to not use user auth, skip all of this

	if AuthTokenFromContext($ctx->UserContext()) != null && UserFromContext($ctx->UserContext()) != null {
		// Short circuit for when the auth token is already in the context
		return $ctx->Next()
	}list($sess, $err) = $store->Get(ctx)
	if $err !== null {
		return err
	}list($authToken, $ok) = $sess->Get(sessKeyAuthToken).(string)
	if !ok {list($if, $token) = AuthTokenFromContext($ctx->UserContext()); token != null && *token != "" {
			return $a->validateAuthToken(ctx, sess, *token)
		}
	}
	if ok {
		return $a->validateAuthToken(ctx, sess, authToken)
	}list($clientID, $clientIDOk) = $sess->Get(sessKeyClientID).(string)list($pinIDStr, $pinIDOk) = $sess->Get(sessKeyPinID).(string)
	if clientIDOk && pinIDOk {list($pinID, $err) = $strconv->ParseInt(pinIDStr, 10, 64)
		if $err !== null {
			return err
		}list($tokenResp, $err) = plexGetToken($ctx->UserContext(), pinID, clientID)
		if $err !== null {
			return err
		}

		if $tokenResp->AuthToken == "" {list($authUrl, $authUrlOk) = $sess->Get(sessKeyAuthUrl).(string)
			if authUrlOk {
				return $ctx->Redirect().To(authUrl)
			}
			return $ctx->Redirect().Route(routeNameAuthUrl)
		}

		return $a->validateAuthToken(ctx, sess, $tokenResp->AuthToken)
	}

	// User has not started the auth flow. Redirect them to the beginning of it.
	return $ctx->Redirect().Route(routeNameAuthUrl)
}

public function authMiddlewareJSON($$ctx->Ctx) {
	if AuthTokenFromContext($ctx->UserContext()) != null && UserFromContext($ctx->UserContext()) != null {
		return $ctx->Next()
	}list($sess, $err) = $store->Get(ctx)
	if $err !== null {
		return renderAPIErrorCode(ctx, $http->StatusUnauthorized, "authentication_required", "authentication required")
	}list($if, $authToken, $ok) = $sess->Get(sessKeyAuthToken).(string); ok && authToken != "" {
		return $a->validateAuthTokenJSON(ctx, sess, authToken)
	}list($if, $authToken) = AuthTokenFromContext($ctx->UserContext()); authToken != null && *authToken != "" {
		return $a->validateAuthTokenJSON(ctx, sess, *authToken)
	}
	return renderAPIErrorCode(ctx, $http->StatusUnauthorized, "authentication_required", "authentication required")
}

public function validateAuthTokenJSON($$ctx->Ctx, $$sess->Session, $authToken) {$newCtx = ContextWithAuthToken($ctx->UserContext(), authToken)list($user, $err) = $a->getValidatedUser(newCtx)
	if $err !== null || user == null || $user->Uuid == "" {
		return renderAPIErrorCode(ctx, $http->StatusUnauthorized, "authentication_required", "authentication required")
	}
	$sess->Set(sessKeyAuthToken, authToken)
	$sess->Set(sessKeyUser, *user)list($if, $err) = $sess->Save(); $err !== null {
		return renderAPIErrorCode(ctx, $http->StatusUnauthorized, "authentication_required", "authentication required")
	}
	$ctx->SetUserContext(ContextWithUser(newCtx, *user))
	return $ctx->Next()
}

public function authUrl($$ctx->Ctx) {list($sess, $err) = $store->Get(ctx)
	if $err !== null {
		return err
	}$clientId = $uuid->New().String()list($pinResp, $err) = plexGetPin($ctx->UserContext(), productCutScene, true, clientId)
	if $err !== null {
		return err
	}$values = $url->Values{}
	$values->Set("clientID", clientId)
	$values->Set("code", $pinResp->Code)
	$values->Set("forwardUrl", $a->config.$API->Domain)
	$values->Set("context[device][product]", productCutScene)$authUrl = sprintf("https://$app->plex.tv/auth#?%s", $values->Encode())

	$sess->Set(sessKeyClientID, clientId)
	$sess->Set(sessKeyPinID, $strconv->FormatInt($pinResp->ID, 10))
	$sess->Set(sessKeyAuthUrl, authUrl)list($if, $err) = $sess->Save(); $err !== null {
		return err
	}

	return $ctx->Redirect().To(authUrl)
}

public function Start() {
	defer func() {
		if $a->app != null {
			_ = $a->app.Close()
		}
	}()
	return $a->http.Listen($a->config.$API->ListenAddr)
}

// Shutdown cancels application work before bounded HTTP draining. This lets
// active previews observe lifetime cancellation instead of keeping shutdown
// blocked for the full preview timeout.
public function Shutdown() {
	if a == null {
		return null
	}
	$shutdownErr = null;
	if $a->app != null {
		// Cancel active work first, but keep SQLite open while the HTTP server
		// drains handlers that may still be reading clip metadata or bytes.
		$a->app.stopWork()
	}
	if $a->http != null {list($shutdownCtx, $cancel) = $context->WithTimeout($context->Background(), apiShutdownTimeout)$err = $a->http.ShutdownWithContext(shutdownCtx)
		cancel()
		if shutdownErr == null {
			shutdownErr = err
		}
	}
	if $a->app != null {list($if, $err) = $a->app.Close(); shutdownErr == null && $err !== null {
			shutdownErr = err
		} else if $err !== null {
			shutdownErr = $errors->Join(shutdownErr, err)
		}
	}
	return shutdownErr
}

public function getSessions($$ctx->Ctx) {list($sessions, $err) = $a->app.GetSessions($ctx->UserContext())
	if $err !== null {
		return err
	}

	return $ctx->JSON(sessions)
}

public function searchLibrary($$ctx->Ctx) {$token = AuthTokenFromContext($ctx->UserContext())
	if UserFromContext($ctx->UserContext()) == null || token == null || $strings->TrimSpace(*token) == "" {
		return renderAPIError(ctx, $http->StatusUnauthorized, "authentication required")
	}$query = $ctx->Query("query")
	if query == "" {
		// Accept the conventional short spelling without changing the stable
		// response contract.
		query = $ctx->Query("q")
	}list($if, $_, $err) = validateLibrarySearchQuery(query); $err !== null {
		return renderAPIErrorCode(ctx, $http->StatusUnprocessableEntity, "validation_error", $err->Error())
	}list($results, $err) = $a->app.SearchLibrary($ctx->UserContext(), query)
	if $err !== null {
		error_log("library search failed: %s", redactedDiagnostic(err))
		$ctx->Set("Retry-After", "5")
		return renderAPIErrorCode(ctx, $http->StatusServiceUnavailable, "search_unavailable", "could not search the Plex library")
	}
	$ctx->Set("Cache-Control", "no-store")
	return $ctx->JSON(results)
}

public function getLibraryMetadataChildren($$ctx->Ctx) {list($ratingKey, $err) = validateLibraryRatingKey($ctx->Params("ratingKey"))
	if $err !== null {
		return renderAPIErrorCode(ctx, $http->StatusUnprocessableEntity, "validation_error", "ratingKey is invalid")
	}$token = AuthTokenFromContext($ctx->UserContext())
	if UserFromContext($ctx->UserContext()) == null || token == null || $strings->TrimSpace(*token) == "" {
		return renderAPIError(ctx, $http->StatusUnauthorized, "authentication required")
	}list($results, $err) = $a->app.GetLibraryMetadataChildren($ctx->UserContext(), ratingKey)
	if $err !== null {
		error_log("library hierarchy failed: %s", redactedDiagnostic(err))
		return renderLibraryHierarchyAPIError(ctx, err)
	}
	$ctx->Set("Cache-Control", "no-store")
	return $ctx->JSON(results)
}

public function getLibrarySource($$ctx->Ctx) {list($ratingKey, $err) = validateLibraryRatingKey($ctx->Params("ratingKey"))
	if $err !== null {
		return renderAPIErrorCode(ctx, $http->StatusUnprocessableEntity, "validation_error", "ratingKey is invalid")
	}$token = AuthTokenFromContext($ctx->UserContext())
	if UserFromContext($ctx->UserContext()) == null || token == null || $strings->TrimSpace(*token) == "" {
		return renderAPIError(ctx, $http->StatusUnauthorized, "authentication required")
	}list($mediaID, $partID, $err) = parseRequiredLibrarySourceQuery(ctx)
	if $err !== null {
		return renderAPIErrorCode(ctx, $http->StatusUnprocessableEntity, "validation_error", $err->Error())
	}list($result, $err) = $a->app.GetLibrarySource($ctx->UserContext(), ratingKey, mediaID, partID)
	if $err !== null {
		error_log("library source failed: %s", redactedDiagnostic(err))
		return renderLibrarySourceAPIError(ctx, err)
	}
	$ctx->Set("Cache-Control", "no-store")
	return $ctx->JSON(result)
}

public function getStreams($$ctx->Ctx) {$ratingKeyStr = $ctx->Params("ratingKey")
	if ratingKeyStr == "" || len(ratingKeyStr) > 512 {
		return $fmt->Errorf("ratingKey not specified")
	}list($mediaID, $partID, $err) = parseSourceQuery(ctx)
	if $err !== null {
		return renderAPIErrorCode(ctx, $http->StatusUnprocessableEntity, "validation_error", $err->Error())
	}list($streams, $err) = $a->app.GetSubtitleStreamsForSource($ctx->UserContext(), ratingKeyStr, mediaID, partID)
	if $err !== null {
		return renderSourceAPIError(ctx, err)
	}

	return $ctx->JSON(streams)
}

function parseSourceQuery($$ctx->Ctx) {$mediaID = $ctx->Query("mediaId")$partID = $ctx->Query("partId")
	if mediaID != "" {list($value, $err) = $strconv->ParseInt(mediaID, 10, 64)
		if $err !== null || value <= 0 {
			return "", "", $errors->New("mediaId is invalid")
		}
	}
	if partID != "" {list($value, $err) = $strconv->ParseInt(partID, 10, 64)
		if $err !== null || value <= 0 {
			return "", "", $errors->New("partId is invalid")
		}
	}
	return mediaID, partID, null
}

function parseRequiredLibrarySourceQuery($$ctx->Ctx) {list($mediaIDStr, $partIDStr, $err) = parseSourceQuery(ctx)
	if $err !== null {
		return 0, 0, err
	}
	if mediaIDStr == "" {
		return 0, 0, $errors->New("mediaId is required")
	}
	if partIDStr == "" {
		return 0, 0, $errors->New("partId is required")
	}list($mediaID, $err) = $strconv->ParseInt(mediaIDStr, 10, 64)
	if $err !== null || mediaID <= 0 {
		return 0, 0, $errors->New("mediaId is invalid")
	}list($partID, $err) = $strconv->ParseInt(partIDStr, 10, 64)
	if $err !== null || partID <= 0 {
		return 0, 0, $errors->New("partId is invalid")
	}
	return mediaID, partID, null
}

function plexMetadataStatus($err) {
	$sdkErr = null;.SDKError
	if $errors->As(err, &sdkErr) {
		return $sdkErr->StatusCode
	}
	$libraryErr = null;
	if $errors->As(err, &libraryErr) {
		return $libraryErr->status
	}
	return 0
}

function renderLibraryHierarchyAPIError($$ctx->Ctx, $err) {
	$validationErr = null;
	if $errors->As(err, &validationErr) {
		return renderAPIErrorCode(ctx, $http->StatusUnprocessableEntity, "validation_error", $validationErr->Error())
	}$status = plexMetadataStatus(err)
	if status == $http->StatusUnauthorized || status == $http->StatusForbidden || status == $http->StatusNotFound {
		return renderAPIErrorCode(ctx, $http->StatusNotFound, "not_found", "requested library item is unavailable")
	}
	return renderAPIErrorCode(ctx, $http->StatusServiceUnavailable, "metadata_unavailable", "could not load the library hierarchy")
}

function renderLibrarySourceAPIError($$ctx->Ctx, $err) {
	$validationErr = null;
	if $errors->As(err, &validationErr) {
		return renderAPIErrorCode(ctx, $http->StatusUnprocessableEntity, "validation_error", $validationErr->Error())
	}$status = plexMetadataStatus(err)
	if status == $http->StatusUnauthorized || status == $http->StatusForbidden || status == $http->StatusNotFound {
		return renderAPIErrorCode(ctx, $http->StatusNotFound, "not_found", "requested library source is unavailable")
	}
	if status == 0 || status >= 500 {
		return renderAPIErrorCode(ctx, $http->StatusServiceUnavailable, "metadata_unavailable", "could not load the library source")
	}
	return renderAPIErrorCode(ctx, $http->StatusUnprocessableEntity, "validation_error", "requested library source is unavailable")
}

function normalizeSourceError($err) {
	$validationErr = null;
	if $errors->As(err, &validationErr) {
		return err
	}$status = plexMetadataStatus(err)
	if status == $http->StatusUnauthorized || status == $http->StatusForbidden || status == $http->StatusNotFound {
		return $errors->New("requested media is unavailable")
	}
	return newPreviewUpstreamFailure("could not get library metadata", $errors->New("Plex metadata service unavailable"))
}

function renderSourceAPIError($$ctx->Ctx, $err) {
	$validationErr = null;
	if $errors->As(err, &validationErr) {
		return renderAPIErrorCode(ctx, $http->StatusUnprocessableEntity, "validation_error", $validationErr->Error())
	}$status = plexMetadataStatus(err)
	if status == 0 || status >= 500 {
		return renderAPIErrorCode(ctx, $http->StatusServiceUnavailable, "metadata_unavailable", "could not validate the requested media")
	}
	return renderAPIErrorCode(ctx, $http->StatusUnprocessableEntity, "validation_error", "requested media is unavailable")
}

public function getSubtitleEntries($$ctx->Ctx) {$ratingKeyStr = $ctx->Params("ratingKey")
	if ratingKeyStr == "" || len(ratingKeyStr) > 512 {
		return $fmt->Errorf("ratingKey not specified")
	}$mediaIdStr = $ctx->Query("mediaId")$partIdStr = $ctx->Query("partId")list($if, $_, $_, $err) = parseSourceQuery(ctx); $err !== null {
		return renderAPIErrorCode(ctx, $http->StatusUnprocessableEntity, "validation_error", $err->Error())
	}$subtitleIndexStr = $ctx->Query("subtitle", "-1")list($subtitleIndex, $err) = $strconv->Atoi(subtitleIndexStr)
	if $err !== null {
		return $fmt->Errorf("subtitle not an integer")
	}

	if subtitleIndex < 0 {
		return $ctx->JSON([]SubtitleEntry{})
	}list($entries, $err) = $a->app.GetSubtitleEntriesForSource($ctx->UserContext(), ratingKeyStr, mediaIdStr, partIdStr, subtitleIndex)
	if $err !== null {
		return renderSourceAPIError(ctx, err)
	}

	return $ctx->JSON(entries)
}

public function clip($$ctx->Ctx) {$ratingKeyStr = $ctx->Params("ratingKey")
	if ratingKeyStr == "" || len(ratingKeyStr) > 512 {
		return $fmt->Errorf("ratingKey not specified")
	}$mediaIdStr = $ctx->Query("mediaId")$from = $ctx->Params("from")
	if from == "" {
		return $fmt->Errorf("from not specified")
	}$to = $ctx->Params("to")
	if to == "" {
		return $fmt->Errorf("to not specified")
	}$heightStr = $ctx->Query("height", "0")list($height, $err) = $strconv->Atoi(heightStr)
	if $err !== null {
		return $fmt->Errorf("height not an integer")
	}$qpStr = $ctx->Query("qp", "0")list($qp, $err) = $strconv->Atoi(qpStr)
	if $err !== null {
		return $fmt->Errorf("qp not an integer")
	}$subtitleIndexStr = $ctx->Query("subtitle", "-1")list($subtitleIndex, $err) = $strconv->Atoi(subtitleIndexStr)
	if $err !== null {
		return $fmt->Errorf("subtitle not an integer")
	}list($filePath, $err) = $a->app.Clip($ctx->UserContext(), ratingKeyStr, mediaIdStr, from, to, height, qp, subtitleIndex)
	if $err !== null {
		return err
	}$fileName = $filepath->Base(filePath)
	$ctx->Type($filepath->Ext(fileName))
	$ctx->Set($fiber->HeaderContentDisposition, sprintf(`attachment; filename="%s"`, fileName))

	return $ctx->SendFile(filePath, $fiber->SendFile{
		ByteRange: true,
	})
}

public function thumb($$ctx->Ctx) {$path = $ctx->Query("path")
	if path == "" {
		return $fmt->Errorf("path not specified")
	}list($respBody, $contentType, $err) = $a->app.Thumb($ctx->UserContext(), path)
	if $err !== null {
		return err
	}

	defer $respBody->Close()

	if contentType != "" {
		$ctx->Set("Content-Type", contentType)
	}

	_, _ = $io->Copy($ctx->Response().BodyWriter(), respBody)

	return null
}

public function preview($$ctx->Ctx) {list($if, $err) = $a->previewStream(ctx); $err !== null {
		if !isExpectedPreviewTermination(err, null) {
			error_log("preview preparation failed: %s", redactedDiagnostic(err))
		}
		$upstream = null;
		if $errors->As(err, &upstream) {
			$ctx->Set("Retry-After", "5")
			return renderAPIErrorCode(ctx, $http->StatusServiceUnavailable, $upstream->code, $upstream->message)
		}
		return renderAPIErrorCode(ctx, $http->StatusUnprocessableEntity, "preview_unavailable", "preview is unavailable")
	}
	return null
}

class previewFailure {    public $code;
    public $message;
    public $err;
}

public function Error() { return $e->message + ": " + $e->err.Error() }
public function Unwrap() { return $e->err }

function newPreviewUpstreamFailure($message, $err) {
	if err == null {
		err = $errors->New(message)
	}
	return &previewFailure{code: "preview_unavailable", message: message, err: err}
}

type previewFFmpegRunner func($context->Context, string, string, string, string, int, Codec, $io->Writer) error

// runPreviewStream owns the successful encoder acquisition until the stream
// writer has finished. It also flushes the response writer itself so a client
// disconnect in the final buffered bytes cancels FFmpeg and follows the same
// cleanup path as an earlier write error.
function runPreviewStream($$streamCtx->Context, $$writer->Writer, $release() {
	defer release()
	defer cancelStream()
	defer finish()$output = &previewClientDisconnectWriter{writer: writer, cancel: cancelStream}$err = run(streamCtx, fileURL, from, to, subtitleFile, subtitleIndex, codec, output)list($if, $flushErr) = $output->Flush(); err == null {
		err = flushErr
	}
	if err == null {
		return
	}
	if isExpectedPreviewTermination(err, output) {
		return
	}
	error_log("preview ffmpeg failed: %s", redactedDiagnostic(err))
}

class previewSessionSelection {    public $mediaID;
    public $partID;
    public $duration;
    public $selected;
}

type sourceValidationError struct{ message string }

public function Error() { return $e->message }

// selectPreviewSessionSource authorizes a preview against the caller-visible
// session list. Active-session responses do not reliably include the part key
// (or file), so this deliberately checks IDs only. The playable key is taken
// from the separately fetched metadata after this authorization succeeds.
function selectPreviewSessionSource($sessions, $ratingKey, $requestedID, $supplied) {list($for, $_, $session) = range sessions {$key = $session->Key
		if $session->RatingKey != null {
			key = *$session->RatingKey
		}
		if key != ratingKey {
			continue
		}list($for, $_, $media) = range $session->Media {list($mediaID, $_) = sessionIDAsInt64($media->ID)
			if mediaID <= 0 && supplied && sessionValueMatchesID($media->ID, requestedID) {
				mediaID = requestedID
			}$duration = int64(0)
			if $media->Duration != null && *$media->Duration > 0 {
				duration = int64(*$media->Duration)
			} else if $session->Duration != null && *$session->Duration > 0 {
				duration = int64(*$session->Duration)
			}

			if supplied && sessionValueMatchesID($media->ID, requestedID) {$partID = int64(0)list($for, $_, $part) = range $media->Part {list($if, $id, $ok) = sessionIDAsInt64($part->ID); ok && id > 0 {
						partID = id
						break
					}
				}
				return previewSessionSelection{mediaID: mediaID, partID: partID, duration: duration, selected: requestedID}, null
			}list($for, $_, $part) = range $media->Part {
				if supplied && sessionValueMatchesID($part->ID, requestedID) {
					return previewSessionSelection{mediaID: mediaID, partID: requestedID, duration: duration, selected: requestedID}, null
				}
				if !supplied {list($if, $id, $ok) = sessionIDAsInt64($part->ID); ok && id > 0 {
						return previewSessionSelection{mediaID: mediaID, partID: id, duration: duration, selected: id}, null
					}
				}
			}
			if !supplied && mediaID > 0 {
				return previewSessionSelection{mediaID: mediaID, duration: duration, selected: mediaID}, null
			}
		}
	}
	return previewSessionSelection{}, $errors->New("requested media is not visible in the caller's sessions")
}

function previewSessionMediaID($sessions, $ratingKey, $requestedID, $supplied) {list($selection, $err) = selectPreviewSessionSource(sessions, ratingKey, requestedID, supplied)
	if $err !== null {
		return 0, err
	}
	return $selection->selected, null
}

// resolvePreviewMetadataSource maps the authorized session selection to the
// library metadata response. In particular, a frontend mediaId may be a Plex
// Part ID, whose parent metadata $Media->ID is different.
function resolvePreviewMetadataSource($$metadata->Media, $selection) {
	if $selection->partID > 0 {list($for, $i) = range metadata {
			if $selection->mediaID > 0 && !sessionValueMatchesID(metadata[i].ID, $selection->mediaID) {
				continue
			}list($for, $j) = range metadata[i].Part {$part = &metadata[i].Part[j]
				if sessionValueMatchesID($part->ID, $selection->partID) && $part->Key != "" {
					return &metadata[i], part, null
				}
			}
		}
	}

	if $selection->mediaID > 0 {list($for, $i) = range metadata {
			if !sessionValueMatchesID(metadata[i].ID, $selection->mediaID) {
				continue
			}list($for, $j) = range metadata[i].Part {
				if metadata[i].Part[j].Key != "" {
					return &metadata[i], &metadata[i].Part[j], null
				}
			}
		}
	}

	return null, null, $errors->New("requested preview source is unavailable")
}

// selectLibraryMetadataSource applies the one source policy used by search,
// preview, subtitles, and render: movie/episode metadata must resolve to a
// real, accessible Part with a Plex stream key. Media are considered in PMS
// order; source codec/profile is left to the FFmpeg render pipeline.
// A supplied ID may identify either the parent Media or one of its Parts.
function selectLibraryMetadataSource($$metadata->Metadata, $requestedID, $supplied) {
	if metadata == null {
		return null, null, &sourceValidationError{message: "requested media is not clip-capable"}
	}
	if supplied {list($for, $_, $media) = range $metadata->Media {
			if $media->ID == requestedID {
				return resolveLibraryMetadataSource(metadata, requestedID, 0)
			}
		}
		return resolveLibraryMetadataSource(metadata, 0, requestedID)
	}
	return resolveLibraryMetadataSource(metadata, 0, 0)
}

// selectLibraryMetadataSourceForDiscovery is intentionally separate from the
// explicit selector. Search and hierarchy discovery may try later Plex
// versions when the first otherwise-playable candidate cannot satisfy the
// exact-duration safety rule. Explicit mediaId/partId requests continue to
// resolve one requested source and fail when its duration is unsafe.
function selectLibraryMetadataSourceForDiscovery($$metadata->Metadata) {
	if metadata == null || ($metadata->Type != "movie" && $metadata->Type != "episode") || $strings->TrimSpace($metadata->Title) == "" {
		return null, null, &sourceValidationError{message: "requested media is not clip-capable"}
	}list($for, $i) = range $metadata->Media {$media = &$metadata->Media[i]
		if $media->ID <= 0 {
			continue
		}list($for, $j) = range $media->Part {$part = &$media->Part[j]
			if $part->Key == "" || $part->ID <= 0 || $part->Accessible != null && !*$part->Accessible || $part->Exists != null && !*$part->Exists {
				continue
			}list($duration, $err) = selectedDiscoverySourceDuration(metadata, media, part)
			if $err !== null || duration <= 0 {
				continue
			}
			return media, part, null
		}
	}
	return null, null, &sourceValidationError{message: "requested media has no playable part"}
}

// resolveLibraryMetadataSource is the canonical explicit-library resolver.
// mediaID identifies a Media and partID identifies its playable Part. A
// missing partID selects the first playable part of the requested Media.
function resolveLibraryMetadataSource($$metadata->Metadata, mediaID, $partID) {
	if metadata == null || ($metadata->Type != "movie" && $metadata->Type != "episode") || $strings->TrimSpace($metadata->Title) == "" {
		return null, null, &sourceValidationError{message: "requested media is not clip-capable"}
	}list($for, $i) = range $metadata->Media {$media = &$metadata->Media[i]
		if $media->ID <= 0 || mediaID > 0 && $media->ID != mediaID {
			continue
		}list($for, $j) = range $media->Part {$part = &$media->Part[j]
			if partID > 0 && $part->ID != partID {
				continue
			}
			if $part->Key == "" || $part->ID <= 0 || $part->Accessible != null && !*$part->Accessible || $part->Exists != null && !*$part->Exists {
				continue
			}
			return media, part, null
		}
	}
	return null, null, &sourceValidationError{message: "requested media has no playable part"}
}

function mediaDurationFromSource($$media->Media, $$part->Part) {
	if part != null && $part->Duration != null && *$part->Duration > 0 {
		return int64(*$part->Duration)
	}
	if media != null && len($media->Part) > 1 {
		return 0
	}
	if media != null && $media->Duration != null && *$media->Duration > 0 {
		return int64(*$media->Duration)
	}
	return 0
}

function selectedSourceDuration($$media->Media, $$part->Part) {
	if part != null && $part->Duration != null && *$part->Duration > 0 {
		return int64(*$part->Duration), null
	}
	if media != null && len($media->Part) > 1 {
		return 0, &sourceValidationError{message: "multipart media requires a part duration"}
	}
	if media != null && $media->Duration != null && *$media->Duration > 0 {
		return int64(*$media->Duration), null
	}
	return 0, null
}

// selectedDiscoverySourceDuration is the discovery-only duration policy. A
// title duration is safe only for a single-part source; multipart clipping
// requires the selected Part's own exact duration.
function selectedDiscoverySourceDuration($$item->Metadata, $$media->Media, $$part->Part) {
	if part != null && $part->Duration != null && *$part->Duration > 0 {
		return int64(*$part->Duration), null
	}
	if media != null && len($media->Part) > 1 {
		return 0, &sourceValidationError{message: "multipart media requires a part duration"}
	}
	if media != null && $media->Duration != null && *$media->Duration > 0 {
		return int64(*$media->Duration), null
	}
	if item != null && $item->Duration != null && *$item->Duration > 0 {
		return int64(*$item->Duration), null
	}
	return 0, null
}

function findVisiblePartKey($sessions, $ratingKey, $requestedID) {list($for, $_, $session) = range sessions {$key = $session->Key
		if $session->RatingKey != null {
			key = *$session->RatingKey
		}
		if key != ratingKey {
			continue
		}list($for, $_, $media) = range $session->Media {
			if !sessionValueMatchesID($media->ID, requestedID) {$matchedPart = falselist($for, $_, $part) = range $media->Part {
					if sessionValueMatchesID($part->ID, requestedID) {
						matchedPart = true
						if $part->Key != "" {
							return $part->Key
						}
					}
				}
				if !matchedPart {
					continue
				}
			}list($for, $_, $part) = range $media->Part {
				if $part->Key != "" {
					return $part->Key
				}
			}
		}
	}
	return ""
}

function sessionIDAsInt64($value) {list($switch, $value) = value.(type) {
	case int:
		return int64(value), true
	case int64:
		return value, true
	case float64:
		return int64(value), value == float64(int64(value))
	case $json->Number:list($valueInt, $err) = $value->Int64()
		return valueInt, err == null
	case string:list($valueInt, $err) = $strconv->ParseInt(value, 10, 64)
		return valueInt, err == null
	default:
		return 0, false
	}
}

public function previewStream($$ctx->Ctx) {$user = UserFromContext($ctx->UserContext())
	if user == null || $user->Uuid == "" {
		return $errors->New("authentication required")
	}list($operationCtx, $cancelOperation) = $a->app.operationContext($ctx->UserContext())$streamTransferred = false
	defer func() {
		if !streamTransferred {
			cancelOperation()
		}
	}()$ratingKeyStr = $ctx->Params("ratingKey")
	if ratingKeyStr == "" || len(ratingKeyStr) > 512 {
		return $fmt->Errorf("ratingKey not specified")
	}$from = $ctx->Params("from")
	if from == "" {
		return $fmt->Errorf("from not specified")
	}$to = $ctx->Params("to")
	if to == "" {
		return $fmt->Errorf("to not specified")
	}list($fromMs, $err) = ParseTimestampToMs(from)
	if $err !== null || fromMs < 0 {
		return $errors->New("invalid preview start range")
	}list($toMs, $err) = ParseTimestampToMs(to)
	if $err !== null || toMs < 0 || toMs <= fromMs {
		return $errors->New("invalid preview end range")
	}
	if toMs-fromMs > renderMaxDurationMs {
		return $errors->New("preview duration exceeds the maximum allowed duration")
	}list($audioMode, $err) = parseAudioMode($ctx->Query("audioMode", ""))
	if $err !== null {
		return $errors->New("audioMode is invalid")
	}$subtitleIndexStr = $ctx->Query("subtitle", "-1")list($subtitleIndex, $err) = $strconv->Atoi(subtitleIndexStr)
	if $err !== null {
		return $fmt->Errorf("subtitle not an integer")
	}$previewMediaIdStr = $ctx->Query("mediaId")$previewPartIdStr = $ctx->Query("partId")
	$previewMediaId = null;
	if previewMediaIdStr != "" {
		previewMediaId, err = $strconv->ParseInt(previewMediaIdStr, 10, 64)
		if $err !== null || previewMediaId <= 0 {
			return $errors->New("could not parse media id")
		}
	}
	$previewPartId = null;
	if previewPartIdStr != "" {
		previewPartId, err = $strconv->ParseInt(previewPartIdStr, 10, 64)
		if $err !== null || previewPartId <= 0 {
			return $errors->New("could not parse part id")
		}
	}
	if subtitleIndex < -1 {
		return $errors->New("subtitle not available")
	}list($subtitleOffsetMs, $err) = parseSubtitleOffsetMs($ctx->Query("subtitleOffsetMs", "0"))
	if $err !== null {
		return $errors->New("subtitleOffsetMs is invalid")
	}

	$sessions = null;($sessionMetadata, $var, $selection, $previewSessionSelection, $userScoped) = previewPartIdStr != ""
	$metadataItem = null;.Metadata
	if userScoped {
		metadataItem, err = $a->app.getMetadataItem(operationCtx, ratingKeyStr, true)
		if $err !== null {
			return normalizeSourceError(err)
		}list($media, $part, $sourceErr) = resolveLibraryMetadataSource(metadataItem, previewMediaId, previewPartId)
		if sourceErr != null {
			return sourceErr
		}$duration = mediaDurationFromSource(media, part)list($if, $_, $durationErr) = selectedSourceDuration(media, part); durationErr != null {
			return durationErr
		}
		selection = previewSessionSelection{mediaID: $media->ID, partID: $part->ID, duration: duration, selected: $part->ID}
	} else {
		// No partId is the legacy active-session path. Explicit library sources
		// must never depend on /status/sessions being available.
		sessions, err = $a->app.GetSessions(operationCtx)
		if $err !== null {
			return newPreviewUpstreamFailure("could not validate caller-visible preview source", err)
		}
		selection, err = selectPreviewSessionSource(sessions, ratingKeyStr, previewMediaId, previewMediaIdStr != "")
		if $err !== null {
			return err
		}
	}
	if $selection->duration > 0 && toMs > $selection->duration {
		return $errors->New("requested range exceeds the selected media duration")
	}

	if metadataItem == null {
		metadataItem, err = $a->app.getMetadataItem(operationCtx, ratingKeyStr, userScoped)
	}
	if $err !== null {
		return newPreviewUpstreamFailure("could not get library metadata", err)
	}$metadata = *metadataItemlist($previewMedia, $previewPart, $err) = resolvePreviewMetadataSource($metadata->Media, selection)
	if $err !== null {
		return err
	}$visibleMediaID = $previewMedia->IDlist($mediaDuration, $durationErr) = selectedSourceDuration(previewMedia, previewPart)
	if durationErr != null {
		return durationErr
	}
	if mediaDuration > 0 && toMs > mediaDuration {
		return $errors->New("requested range exceeds the selected media duration")
	}$fileURL = ""
	$callerAccess = null;
	if userScoped {
		$accessErr = null;
		callerAccess, _, accessErr = $a->app.callerPlexAccess(operationCtx, "")
		if accessErr != null {
			return newPreviewUpstreamFailure("caller Plex access is unavailable", accessErr)
		}
	} else {
		fileURL = sprintf("%s%s?X-Plex-Token=%s",
			$a->config.$Plex->Host,
			$previewPart->Key,
			$a->app.plexSourceToken(operationCtx, false),
		)
	}

	// Extract subtitle to temp file if requested. For text subtitles, either use
	// cached entries via WriteClipSRT or extract via FFmpeg. For PGS, pass
	// subtitleIndex directly to FFmpeg overlay filter.
	$subtitleFile = null;
	defer func() {
		if subtitleFile != "" && !streamTransferred {
			_ = $os->Remove(subtitleFile)
		}
	}()
	$subtitleIdxForFFmpeg = null; = -1
	if subtitleIndex >= 0 {list($source, $sourceErr) = selectSubtitleSource($previewPart->Stream, subtitleIndex)
		if sourceErr != null {
			return $errors->New("requested subtitle is unavailable")
		}
		if $source->PGS && $source->External {
			return $errors->New("subtitle codec is not supported")
		}

		if $source->PGS {
			subtitleIdxForFFmpeg = $source->EmbeddedIndex
		} else {$cacheMediaID = $strconv->FormatInt(visibleMediaID, 10)$cachePartID = $strconv->FormatInt($previewPart->ID, 10)list($if, $entries, $ok) = $a->app.GetCachedSubtitleEntries(operationCtx, ratingKeyStr, cacheMediaID, cachePartID, subtitleIndex); ok {
				subtitleFile, err = WriteClipSRT(entries, fromMs, toMs, subtitleOffsetMs)
				if $err !== null {
					if !$errors->Is(err, ErrNoUsableSubtitleCues) {
						return $fmt->Errorf("could not write clip subtitle: %w", err)
					}
					subtitleFile = ""
				}
			} else if $source->External {
				if !isSupportedTextSubtitle(source) {
					return $errors->New("subtitle codec is not supported")
				}
				subtitleFile, err = $a->app.prepareExternalSubtitleWithToken(operationCtx, source, fromMs, toMs, []int64{subtitleOffsetMs}, $a->app.plexSourceToken(operationCtx, userScoped))
				if $err !== null {
					if $errors->Is(err, ErrNoUsableSubtitleCues) {
						subtitleFile = ""
					} else {
						return newPreviewUpstreamFailure("preview subtitle source unavailable", err)
					}
				}
			} else {list($acquireCtx, $cancelAcquire) = $context->WithTimeout(operationCtx, renderFFmpegAcquireTimeout)list($release, $acquireErr) = $a->app.acquireFFmpeg(acquireCtx)
				cancelAcquire()
				if acquireErr != null {
					return newPreviewUpstreamFailure("preview encoder capacity is unavailable", acquireErr)
				}list($subtitleCtx, $cancelSubtitle) = $context->WithTimeout(operationCtx, renderPreviewTimeout)
				subtitleFile, err = func() (string, error) {
					defer release()$inputURL = fileURL
					$releaseCapability = null;()
					if userScoped {list($proxy, $proxyErr) = $a->app.ensureMediaProxy()
						if proxyErr != null {
							return "", proxyErr
						}
						$issueErr = null;
						inputURL, releaseCapability, issueErr = $proxy->IssueWithTTL(subtitleCtx, callerAccess, $previewPart->Key, renderPreviewTimeout)
						if issueErr != null {
							return "", issueErr
						}
						defer releaseCapability()
					}
					return ExtractSubtitleContext(subtitleCtx, inputURL, from, to, $source->EmbeddedIndex, subtitleOffsetMs)
				}()
				cancelSubtitle()
				if $err !== null {
					if $errors->Is(err, ErrNoUsableSubtitleCues) {
						subtitleFile = ""
					} else {
						return newPreviewUpstreamFailure("preview subtitle preparation failed", err)
					}
				}
			}
		}
	}

	// Fiber contexts are request-scoped and may be recycled as soon as this
	// handler returns. The stream writer therefore owns a standalone context
	// with an explicit bound instead of capturing ctx or $ctx->UserContext().list($streamCtx, $streamCancel) = $context->WithTimeout(operationCtx, renderPreviewTimeout)
	$capabilityRelease = null;()
	if userScoped {list($proxy, $proxyErr) = $a->app.ensureMediaProxy()
		if proxyErr != null {
			streamCancel()
			return newPreviewUpstreamFailure("Plex capability proxy is unavailable", proxyErr)
		}
		$issueErr = null;
		fileURL, capabilityRelease, issueErr = $proxy->IssueWithTTL(streamCtx, callerAccess, $previewPart->Key, renderPreviewTimeout)
		if issueErr != null {
			streamCancel()
			return newPreviewUpstreamFailure("Plex capability is unavailable", issueErr)
		}
	}list($acquireCtx, $cancelAcquire) = $context->WithTimeout(streamCtx, renderFFmpegAcquireTimeout)list($release, $err) = $a->app.acquireFFmpeg(acquireCtx)
	cancelAcquire()
	if $err !== null {
		streamCancel()
		return newPreviewUpstreamFailure("preview encoder capacity is unavailable", err)
	}$codec = $a->config.$Ffmpeg->Codec
	if capabilityRelease != null {$encoderRelease = release
		release = func() {
			encoderRelease()
			capabilityRelease()
		}
	}$streamSubtitleFile = subtitleFile
	subtitleFile = ""
	$ctx->Set("Cache-Control", "no-store")
	$ctx->Set("Content-Type", "video/mp4")
	// SetBodyStreamWriter starts the callback after the handler returns. Mark
	// the lease and operation as transferred now; waiting for the callback here
	// deadlocks because the callback cannot start until this handler returns.$streamLeaseTransferred = false
	defer func() {
		if !streamLeaseTransferred {
			release()
		}
	}()$previewRunner = $a->previewRunner$previewRunnerWithOffset = $a->previewRunnerWithSubtitleOffset
	if previewRunner == null && previewRunnerWithOffset == null {
		previewRunnerWithOffset = func(ctx $context->Context, fileURL, from, to, subtitleFile string, subtitleIndex int, codec Codec, writer $io->Writer, audioMode AudioMode, offsetMs int64) error {
			return DoFfmpegPreviewContextWithSubtitleOffset(ctx, fileURL, from, to, subtitleFile, subtitleIndex, codec, writer, audioMode, offsetMs)
		}
	}
	if previewRunner == null {
		previewRunner = func(ctx $context->Context, fileURL, from, to, subtitleFile string, subtitleIndex int, codec Codec, writer $io->Writer, audioMode AudioMode) error {
			return previewRunnerWithOffset(ctx, fileURL, from, to, subtitleFile, subtitleIndex, codec, writer, audioMode, subtitleOffsetMs)
		}
	}
	$ctx->Response().SetBodyStreamWriter(func(w *$bufio->Writer) {
		if streamSubtitleFile != "" {
			defer $os->Remove(streamSubtitleFile)
		}
		runPreviewStream(streamCtx, w, release, streamCancel, cancelOperation, fileURL, from, to, streamSubtitleFile, subtitleIdxForFFmpeg, codec,
			func(ctx $context->Context, fileURL, from, to, subtitleFile string, subtitleIndex int, codec Codec, writer $io->Writer) error {
				return previewRunner(ctx, fileURL, from, to, subtitleFile, subtitleIndex, codec, writer, audioMode)
			})
	})
	streamLeaseTransferred = true
	streamTransferred = true

	return null
}

// plexPinResponse represents the response from POST /api/v2/$pins->json
class plexPinResponse {    public $ID;
    public $Code;
}

// plexTokenResponse represents the response from GET /api/v2/pins/{id}.json
class plexTokenResponse {    public $AuthToken;
}

// plexAPIBase allows test injection of a mock server URL.
var plexAPIBase = "https://$plex->tv"

// plexGetPin creates a Plex authentication PIN via direct HTTP call
function plexGetPin($$ctx->Context, $product, $strong, $clientID) {$reqUrl = plexAPIBase + "/api/v2/$pins->json"$data = $url->Values{}
	$data->Set("strong", $strconv->FormatBool(strong))list($req, $err) = $http->NewRequestWithContext(ctx, $http->MethodPost, reqUrl, $strings->NewReader($data->Encode()))
	if $err !== null {
		return null, err
	}
	$req->Header.Set("X-Plex-Client-Identifier", clientID)
	$req->Header.Set("X-Plex-Product", product)
	$req->Header.Set("Content-Type", "application/x-www-form-urlencoded")list($resp, $err) = $http->DefaultClient.Do(req)
	if $err !== null {
		return null, err
	}
	defer $resp->Body.Close()list($body, $err) = $io->ReadAll($io->LimitReader($resp->Body, 1024))
	if $err !== null {
		return null, err
	}

	if $resp->StatusCode < 200 || $resp->StatusCode >= 300 {
		return null, $fmt->Errorf("plex PIN request returned status %d: %s", $resp->StatusCode, string(body))
	}list($var, $result, $plexPinResponse, $if, $err) = $json->Unmarshal(body, &result); $err !== null {
		return null, err
	}

	if $result->ID == 0 || $result->Code == "" {
		return null, $fmt->Errorf("plex PIN response missing required fields (id/code)")
	}

	return &result, null
}

// plexGetToken retrieves the auth token for a completed PIN authentication via direct HTTP call
function plexGetToken($$ctx->Context, $pinID, $clientID) {$reqUrl = sprintf("%s/api/v2/pins/%$d->json", plexAPIBase, pinID)list($req, $err) = $http->NewRequestWithContext(ctx, $http->MethodGet, reqUrl, null)
	if $err !== null {
		return null, err
	}
	$req->Header.Set("X-Plex-Client-Identifier", clientID)list($resp, $err) = $http->DefaultClient.Do(req)
	if $err !== null {
		return null, err
	}
	defer $resp->Body.Close()list($body, $err) = $io->ReadAll($io->LimitReader($resp->Body, 1024))
	if $err !== null {
		return null, err
	}

	if $resp->StatusCode < 200 || $resp->StatusCode >= 300 {
		return null, $fmt->Errorf("plex token request returned status %d: %s", $resp->StatusCode, string(body))
	}list($var, $result, $plexTokenResponse, $if, $err) = $json->Unmarshal(body, &result); $err !== null {
		return null, err
	}

	return &result, null
}
