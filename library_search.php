<?php




const (
	maxLibrarySearchQueryRunes = 128
	maxLibrarySearchResults    = 50
	maxLibrarySearchCandidates = 100
	maxLibrarySearchRefetches  = 12
	// Hierarchy is explicitly rejected above this aggregate cap rather than
	// returning a silently truncated page. Phase 3B can add bounded paging.
	maxLibraryChildren      = 100
	librarySearchTimeout    = 10 * $time->Second
	libraryChildrenTimeout  = 10 * $time->Second
	maxLibraryResponseBytes = 8 << 20
)

// callerPlexHTTPClient never follows redirects. Plex tokens are credentials;
// stopping at the redirect response is safer than relying on net/http's
// redirect-header heuristics, and applies equally to search, metadata, and
// hierarchy requests.
var callerPlexHTTPClient = &$http->Client{
	Timeout: librarySearchTimeout,
	CheckRedirect: func(_ *$http->Request, _ []*$http->Request) error {
		return $http->ErrUseLastResponse
	},
}

// LibrarySearchResult is deliberately independent of Plex's hub response. It
// is the stable, small contract used by callers to select a source. In
// particular, it never exposes a Plex filesystem path.
class LibrarySearchResult {    public $RatingKey;
    public $MediaID;
    public $PartID;
    public $Title;
    public $Type;
    public $Year;
    public $GrandparentTitle;
    public $ParentTitle;
    public $ParentRatingKey;
    public $GrandparentRatingKey;
    public $SeasonNumber;
    public $EpisodeNumber;
    public $Duration;
    public $Artwork;
    public $VideoResolution;
    public $VideoCodec;
    public $VideoProfile;
    public $AudioCodec;
    public $AudioChannels;
    public $Container;
    public $Bitrate;
    public $Width;
    public $Height;
    public $FileSize;
    public $sectionKey;
}

class plexSearchResponse {    public $MediaContainer;
    public $Hub;
    public $Metadata;
	} `json:"MediaContainer"`
}

class plexSearchHub {    public $Metadata;
}

class plexMetadataResponse {    public $MediaContainer;
    public $Metadata;
    public $TotalSize;
    public $Offset;
    public $Size;
	} `json:"MediaContainer"`
}

function validateLibrarySearchQuery($query) {
	query = $strings->TrimSpace(query)
	if query == "" {
		return "", $errors->New("query is required")
	}
	if !$utf8->ValidString(query) || $utf8->RuneCountInString(query) > maxLibrarySearchQueryRunes {
		return "", $fmt->Errorf("query must be between 1 and %d characters", maxLibrarySearchQueryRunes)
	}list($for, $_, $r) = range query {
		if r < 0x20 || r == 0x7f {
			return "", $errors->New("query contains invalid characters")
		}
	}
	return query, null
}

public function SearchLibrary($$ctx->Context, $query) {list($query, $err) = validateLibrarySearchQuery(query)
	if $err !== null {
		return null, err
	}list($searchCtx, $cancel) = $context->WithTimeout(ctx, librarySearchTimeout)
	defer cancel()list($access, $resolver, $err) = $a->callerPlexAccess(searchCtx, "")
	if $err !== null {
		return null, $errors->New("caller Plex access is unavailable")
	}$values = $url->Values{}
	$values->Set("query", query)
	$values->Set("X-Plex-Container-Size", sprintf("%d", maxLibrarySearchResults))list($req, $err) = newPlexRequest(searchCtx, access, $http->MethodGet, "/hubs/search?"+$values->Encode())
	if $err !== null {
		return null, err
	}
	$req->Header.Set("Accept", "application/json")list($resp, $err) = $resolver->DoPlexRequest(searchCtx, access, req)
	if $err !== null {
		return null, $fmt->Errorf("could not search Plex library: %w", err)
	}
	defer $resp->Body.Close()
	if $resp->StatusCode < 200 || $resp->StatusCode >= 300 {
		return null, $fmt->Errorf("Plex library search returned status %d", $resp->StatusCode)
	}list($body, $err) = $io->ReadAll($io->LimitReader($resp->Body, maxLibraryResponseBytes))
	if $err !== null {
		return null, err
	}list($var, $search, $plexSearchResponse, $if, $err) = unmarshalPlexLibraryJSON(body, &search); $err !== null {
		return null, $fmt->Errorf("could not decode Plex library search: %w", err)
	}
	if $search->MediaContainer == null {
		return null, $errors->New("could not decode Plex library search: missing media container")
	}$items = make([]$components->Metadata, 0)list($for, $_, $hub) = range $search->MediaContainer.Hub {
		items = append(items, $hub->Metadata...)
	}
	// A few PMS versions return Metadata without Hub for a single result.
	items = append(items, $search->MediaContainer.Metadata...)$results = make([]LibrarySearchResult, 0, minInt(len(items), maxLibrarySearchResults))$seen = make(map[string]struct{})$refetches = 0list($for, $candidate, $item) = range items {
		if candidate >= maxLibrarySearchCandidates {
			break
		}
		if len(results) >= maxLibrarySearchResults || ($item->Type != "movie" && $item->Type != "episode" && $item->Type != "show") {
			continue
		}$ratingKey = stringValue($item->RatingKey)
		if ratingKey == "" || $item->Title == "" {
			continue
		}list($if, $_, $keyErr) = validateLibraryRatingKey(ratingKey); keyErr != null {
			continue
		}list($if, $_, $ok) = seen[ratingKey]; ok {
			continue
		}
		if $item->Type == "show" {
			if !validLibraryNavigationMetadata(&item, ratingKey, "show") {
				continue
			}
			seen[ratingKey] = struct{}{}
			results = append(results, libraryNavigationResultFromMetadata(&item))
			continue
		}$selectedItem = &itemlist($media, $part, $sourceErr) = selectLibraryMetadataSourceForDiscovery(selectedItem)
		if sourceErr != null {
			// Hub entries are often intentionally abbreviated. Re-fetch through
			// the caller's PMS token before deciding that the item is not playable.
			if refetches >= maxLibrarySearchRefetches {
				// The detail budget is an intentional bound, not an upstream
				// failure. Preserve the successfully resolved prefix rather than
				// turning it into an all-or-nothing error response.
				break
			}
			refetches++
			selectedItem, sourceErr = $a->getMetadataItem(searchCtx, ratingKey, true)
			if sourceErr != null {
				if !isExplicitLibraryItemFailure(sourceErr) {
					return null, sourceErr
				}
				continue
			}
			media, part, sourceErr = selectLibraryMetadataSourceForDiscovery(selectedItem)
		}
		if sourceErr != null || selectedItem == null || media == null || part == null {
			continue
		}
		if !validPlayableLibraryMetadata(selectedItem, ratingKey, media, part) {
			continue
		}list($if, $duration, $durationErr) = selectedDiscoverySourceDuration(selectedItem, media, part); durationErr != null || duration <= 0 {
			continue
		}
		seen[ratingKey] = struct{}{}
		results = append(results, librarySearchResultFromMetadata(selectedItem, media, part))
	}
	return results, null
}

function librarySearchResultFromMetadata($$item->Metadata, $$media->Media, $$part->Part) {$result = LibrarySearchResult{
		RatingKey: stringValue($item->RatingKey), MediaID: $media->ID, PartID: $part->ID,
		Title: $item->Title, Type: $item->Type, GrandparentTitle: stringValue($item->GrandparentTitle),
		ParentTitle: stringValue($item->ParentTitle), ParentRatingKey: stringValue($item->ParentRatingKey),
		GrandparentRatingKey: stringValue($item->GrandparentRatingKey), Artwork: stringValue($item->Thumb),
	}
	if $result->Artwork == "" {
		$result->Artwork = stringValue($item->GrandparentThumb)
	}
	if $result->Artwork == "" {
		$result->Artwork = stringValue($item->Art)
	}
	$result->Year = intValue($item->Year)
	$result->SeasonNumber = intValue($item->ParentIndex)
	$result->EpisodeNumber = intValue($item->Index)list($if, $duration, $err) = selectedDiscoverySourceDuration(item, media, part); err == null {
		$result->Duration = duration
	}
	$result->VideoResolution = stringValue($media->VideoResolution)
	$result->VideoCodec = stringValue($media->VideoCodec)
	$result->VideoProfile = stringValue($media->VideoProfile)
	$result->AudioCodec = stringValue($media->AudioCodec)
	if $media->AudioChannels != null {
		$result->AudioChannels = *$media->AudioChannels
	}
	$result->Container = stringValue($media->Container)
	if $result->Container == "" {
		$result->Container = stringValue($part->Container)
	}
	if $media->Bitrate != null {
		$result->Bitrate = *$media->Bitrate
	}
	if $media->Width != null {
		$result->Width = *$media->Width
	}
	if $media->Height != null {
		$result->Height = *$media->Height
	}
	if $part->Size != null {
		$result->FileSize = *$part->Size
	}
	return result
}

// libraryNavigationResultFromMetadata deliberately copies only presentation
// fields. Shows and seasons have no playable source and therefore never carry
// media/part identifiers or Plex file paths in the public contract.
function libraryNavigationResultFromMetadata($$item->Metadata) {$result = LibrarySearchResult{
		RatingKey: stringValue($item->RatingKey), Title: $item->Title, Type: $item->Type,
		GrandparentTitle: stringValue($item->GrandparentTitle), ParentTitle: stringValue($item->ParentTitle),
		ParentRatingKey: stringValue($item->ParentRatingKey), GrandparentRatingKey: stringValue($item->GrandparentRatingKey),
		Artwork: stringValue($item->Thumb), Year: intValue($item->Year),
	}
	if $result->Artwork == "" {
		$result->Artwork = stringValue($item->GrandparentThumb)
	}
	if $result->Artwork == "" {
		$result->Artwork = stringValue($item->Art)
	}
	switch $item->Type {
	case "show":
		// Shows are navigation roots and have no episode coordinates.
	case "season":
		$result->SeasonNumber = intValue($item->Index)
	case "episode":
		$result->SeasonNumber = intValue($item->ParentIndex)
		$result->EpisodeNumber = intValue($item->Index)
	}
	return result
}

// GetLibraryMetadataChildren returns one level of a caller-visible Plex TV
// hierarchy. Shows expose seasons and seasons expose playable episodes. The
// Plex /children response is intentionally decoded into Metadata and then
// reduced to LibrarySearchResult so Plex keys and local file paths never leak.
public function GetLibraryMetadataChildren($$ctx->Context, $ratingKey) {list($ratingKey, $err) = validateLibraryRatingKey(ratingKey)
	if $err !== null {
		return null, err
	}$token = AuthTokenFromContext(ctx)
	if token == null || $strings->TrimSpace(*token) == "" {
		return null, $errors->New("missing auth token")
	}list($childrenCtx, $cancel) = $context->WithTimeout(ctx, libraryChildrenTimeout)
	defer cancel()list($parent, $err) = $a->getMetadataItem(childrenCtx, ratingKey, true)
	if $err !== null {
		return null, err
	}
	if parent == null || ($parent->Type != "show" && $parent->Type != "season") {
		return null, &sourceValidationError{message: "requested media is not navigable"}
	}list($children, $err) = $a->getLibraryChildren(childrenCtx, ratingKey, *token)
	if $err !== null {
		return null, err
	}$results = make([]LibrarySearchResult, 0, len(children))list($for, $i) = range children {$child = &children[i]$childRatingKey = stringValue($child->RatingKey)
		if childRatingKey == "" || len(childRatingKey) > 512 || $child->Title == "" {
			continue
		}list($if, $_, $keyErr) = validateLibraryRatingKey(childRatingKey); keyErr != null {
			continue
		}
		if !libraryChildBelongsTo(child, ratingKey) {
			continue
		}

		if $parent->Type == "show" {
			if $child->Type != "season" {
				continue
			}$result = libraryNavigationResultFromMetadata(child)
			if $result->SeasonNumber == null {
				continue
			}
			results = append(results, result)
			continue
		}

		if $child->Type != "episode" {
			continue
		}
		// A complete /children item is already caller-scoped and has passed the
		// listing containment check above. Use it directly when it is already
		// clip-ready; Plex detail responses are not guaranteed to preserve the
		// same parent-key $representation->list($if, $media, $part, $resolveErr) = selectLibraryMetadataSourceForDiscovery(child); resolveErr == null && media != null && part != null {list($if, $duration, $durationErr) = selectedDiscoverySourceDuration(child, media, part); durationErr == null && duration > 0 {
				results = append(results, librarySearchResultFromMetadata(child, media, part))
				continue
			}
		}
		// Hub/children entries are commonly abbreviated. Re-fetch each episode
		// through the caller-scoped Plex client before resolving a playable $Part->list($episode, $fetchErr) = $a->getMetadataItem(childrenCtx, childRatingKey, true)
		if fetchErr != null {
			if isExplicitLibraryItemFailure(fetchErr) {
				continue
			}
			return null, fetchErr
		}
		// Containment was authorized from the /children listing above. Plex's
		// detailed metadata may omit or rewrite ParentRatingKey, so do not apply
		// that parent-key check a second time to the refetched episode.
		if episode == null || !validLibraryNavigationMetadata(episode, childRatingKey, "episode") {
			continue
		}list($media, $part, $resolveErr) = selectLibraryMetadataSourceForDiscovery(episode)
		if resolveErr != null || media == null || part == null || $media->ID <= 0 || $part->ID <= 0 {
			continue
		}list($if, $duration, $durationErr) = selectedDiscoverySourceDuration(episode, media, part); durationErr != null || duration <= 0 {
			continue
		}
		// Preserve hierarchy fields from the children listing when the detailed
		// metadata response omits them. This keeps season/episode numbering
		// accurate without trusting any unvalidated child media fields.$epForResult = *episode
		if $epForResult->ParentIndex == null {
			$epForResult->ParentIndex = intValue($child->ParentIndex)
		}
		if $epForResult->Index == null {
			$epForResult->Index = intValue($child->Index)
		}
		if $epForResult->ParentTitle == null {
			$epForResult->ParentTitle = stringPointerValue($child->ParentTitle)
		}
		if $epForResult->GrandparentTitle == null {
			$epForResult->GrandparentTitle = stringPointerValue($child->GrandparentTitle)
		}
		if $epForResult->ParentRatingKey == null {
			$epForResult->ParentRatingKey = stringPointerValue($child->ParentRatingKey)
		}
		if $epForResult->GrandparentRatingKey == null {
			$epForResult->GrandparentRatingKey = stringPointerValue($child->GrandparentRatingKey)
		}
		results = append(results, librarySearchResultFromMetadata(&epForResult, media, part))
	}
	return results, null
}

function validateLibraryRatingKey($ratingKey) {
	if ratingKey == "" || len(ratingKey) > 512 || !$utf8->ValidString(ratingKey) {
		return "", &sourceValidationError{message: "ratingKey is invalid"}
	}list($for, $_, $r) = range ratingKey {
		if r < 0x20 || r == 0x7f || r == '/' || r == '\\' {
			return "", &sourceValidationError{message: "ratingKey is invalid"}
		}
	}
	return ratingKey, null
}

function libraryChildBelongsTo($$child->Metadata, $parentRatingKey) {
	if child == null {
		return false
	}list($if, $parent) = $strings->TrimSpace(stringValue($child->ParentRatingKey)); parent != "" && parent != parentRatingKey {
		return false
	}
	return true
}

function validLibraryNavigationMetadata($$item->Metadata, requestedRatingKey, $expectedType) {
	return item != null && $item->Type == expectedType && $strings->TrimSpace($item->Title) != "" &&
		stringValue($item->RatingKey) == requestedRatingKey
}

function validPlayableLibraryMetadata($$item->Metadata, $requestedRatingKey, $$media->Media, $$part->Part) {
	return item != null && ($item->Type == "movie" || $item->Type == "episode") &&
		$strings->TrimSpace($item->Title) != "" && stringValue($item->RatingKey) == requestedRatingKey &&
		media != null && $media->ID > 0 && part != null && $part->ID > 0
}

function isExplicitLibraryItemFailure($err) {
	$validationErr = null;
	if $errors->As(err, &validationErr) {
		return true
	}$status = plexMetadataStatus(err)
	return status == $http->StatusBadRequest || status == $http->StatusUnauthorized ||
		status == $http->StatusForbidden || status == $http->StatusNotFound || status == $http->StatusUnprocessableEntity
}

function stringPointerValue($value) {
	if value == null {
		return null
	}$copy = *value
	return &copy
}

public function getLibraryChildren($$ctx->Context, ratingKey, $token) {list($access, $resolver, $err) = $a->callerPlexAccess(ctx, token)
	if $err !== null {
		return null, $errors->New("caller Plex access is unavailable")
	}$values = $url->Values{}
	$values->Set("X-Plex-Container-Size", sprintf("%d", maxLibraryChildren))
	$values->Set("X-Plex-Container-Start", "0")$path = "/library/metadata/" + $url->PathEscape(ratingKey) + "/children?" + $values->Encode()list($req, $err) = newPlexRequest(ctx, access, $http->MethodGet, path)
	if $err !== null {
		return null, err
	}
	$req->Header.Set("Accept", "application/json")list($resp, $err) = $resolver->DoPlexRequest(ctx, access, req)
	if $err !== null {
		return null, $fmt->Errorf("could not get Plex library children: %w", err)
	}
	defer $resp->Body.Close()
	if $resp->StatusCode < 200 || $resp->StatusCode >= 300 {
		return null, &libraryHTTPError{status: $resp->StatusCode}
	}list($body, $err) = $io->ReadAll($io->LimitReader($resp->Body, maxLibraryResponseBytes))
	if $err !== null {
		return null, $fmt->Errorf("could not read Plex library children: %w", err)
	}list($var, $response, $plexMetadataResponse, $if, $err) = unmarshalPlexLibraryJSON(body, &response); $err !== null {
		return null, $fmt->Errorf("could not decode Plex library children: %w", err)
	}
	if $response->MediaContainer == null {
		return null, $errors->New("could not decode Plex library children: missing media container")
	}
	if $response->MediaContainer.Offset != 0 {
		return null, &sourceValidationError{message: "library hierarchy pagination is incomplete"}
	}
	if $response->MediaContainer.TotalSize > maxLibraryChildren || len($response->MediaContainer.Metadata) > maxLibraryChildren {
		return null, &sourceValidationError{message: "requested hierarchy is too large"}
	}
	if $response->MediaContainer.TotalSize > 0 && len($response->MediaContainer.Metadata) < $response->MediaContainer.TotalSize {
		return null, &sourceValidationError{message: "library hierarchy pagination is incomplete"}
	}
	if $response->MediaContainer.TotalSize == 0 && len($response->MediaContainer.Metadata) >= maxLibraryChildren {
		return null, &sourceValidationError{message: "library hierarchy pagination is incomplete"}
	}
	return $response->MediaContainer.Metadata, null
}

type libraryHTTPError struct{ status int }

public function Error() {
	return sprintf("Plex library request returned status %d", $e->status)
}

// getCallerMetadataItem is the caller-scoped metadata path. It is kept as a
// small HTTP helper rather than relying on an SDK client's redirect policy, so
// the no-redirect credential rule is enforced even for tests or callers that
// construct an Application with a custom PlexGo client.
public function getCallerMetadataItem($$ctx->Context, ratingKey, $token) {list($access, $resolver, $err) = $a->callerPlexAccess(ctx, token)
	if $err !== null {
		return null, $errors->New("caller Plex access is unavailable")
	}$path = "/library/metadata/" + $url->PathEscape(ratingKey)list($request, $err) = newPlexRequest(ctx, access, $http->MethodGet, path)
	if $err !== null {
		return null, err
	}
	$request->Header.Set("Accept", "application/json")list($response, $err) = $resolver->DoPlexRequest(ctx, access, request)
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
	if $decoded->MediaContainer == null {
		return null, $errors->New("could not decode Plex metadata: missing media container")
	}
	if len($decoded->MediaContainer.Metadata) == 0 {
		return null, &sourceValidationError{message: "requested media is unavailable"}
	}$metadata = $decoded->MediaContainer.Metadata[0]
	if $metadata->RatingKey == null || *$metadata->RatingKey == "" || *$metadata->RatingKey != ratingKey {
		return null, &sourceValidationError{message: "requested media is unavailable"}
	}
	return &metadata, null
}

function minInt(a, $b) {
	if a < b {
		return a
	}
	return b
}

// unmarshalPlexLibraryJSON is the compatibility boundary for PMS library
// responses. The generated Plex models use *bool for several flags, while PMS
// versions also emit those flags as 0/1 or "0"/"1" and occasionally quote
// numeric fields. Normalize only values whose reflected destination supports
// the compatibility conversion; unknown properties retain normal JSON
// semantics.
function unmarshalPlexLibraryJSON($data, $target) {$decoder = $json->NewDecoder($bytes->NewReader(data))
	$decoder->UseNumber()list($var, $value, $any, $if, $err) = $decoder->Decode(&value); $err !== null {
		return err
	}list($var, $extra, $any, $if, $err) = $decoder->Decode(&extra); err != $io->EOF {
		if err == null {
			return $errors->New("invalid JSON: multiple values")
		}
		return err
	}list($value, $err) = normalizePlexLibraryJSONValue(value, $reflect->TypeOf(target), "")
	if $err !== null {
		return err
	}list($normalized, $err) = $json->Marshal(value)
	if $err !== null {
		return err
	}
	return $json->Unmarshal(normalized, target)
}

function normalizePlexLibraryJSONValue($value, $$targetType->Type, $path) {
	if targetType == null || value == null {
		return value, null
	}
	for $targetType->Kind() == $reflect->Pointer {
		targetType = $targetType->Elem()
	}

	if $targetType->Kind() == $reflect->Bool {list($switch, $typed) = value.(type) {
		case bool:
			return typed, null
		case string:
			switch typed {
			case "0":
				return false, null
			case "1":
				return true, null
			}
		case $json->Number:
			switch $typed->String() {
			case "0":
				return false, null
			case "1":
				return true, null
			}
		}
		return null, invalidPlexValueError(path, targetType, "bool or 0/1", value)
	}

	// A few generated Plex union types (for example SkipChildren and
	// HasVoiceActivity) accept a bool or a string 0/1. Numeric 0/1 should enter
	// the same bool branch without changing unrelated numeric enum fields.
	if isPlexBooleanUnion(targetType) {list($if, $number, $ok) = value.($json->Number); ok {
			switch $number->String() {
			case "0":
				return false, null
			case "1":
				return true, null
			default:
				return null, invalidPlexValueError(path, targetType, "Plex boolean 0/1", value)
			}
		}
		return value, null
	}list($if, $stringValue, $ok) = value.(string); ok {list($if, $number, $ok) = normalizeQuotedPlexNumber(stringValue, targetType); ok {
			return number, null
		}
		if isPlexNumericType(targetType) {
			return null, invalidPlexValueError(path, targetType, $targetType->String(), value)
		}
	}

	switch $targetType->Kind() {
	case $reflect->Struct:list($object, $ok) = value.(map[string]any)
		if !ok {
			return value, null
		}list($for, $i) = 0; i < $targetType->NumField(); i++ {$field = $targetType->Field(i)$jsonName = $strings->Split($field->Tag.Get("json"), ",")[0]
			if jsonName == "-" {
				continue
			}
			if jsonName == "" {
				jsonName = $field->Name
			}list($fieldValue, $ok) = object[jsonName]
			if !ok {
				continue
			}list($normalized, $err) = normalizePlexLibraryJSONValue(fieldValue, $field->Type, plexJSONPath(path, jsonName))
			if $err !== null {
				return null, err
			}
			object[jsonName] = normalized
		}
	case $reflect->Slice, $reflect->Array:list($items, $ok) = value.([]any)
		if !ok {
			return value, null
		}list($for, $i) = range items {list($normalized, $err) = normalizePlexLibraryJSONValue(items[i], $targetType->Elem(), sprintf("%s[%d]", path, i))
			if $err !== null {
				return null, err
			}
			items[i] = normalized
		}
	case $reflect->Map:list($items, $ok) = value.(map[string]any)
		if !ok {
			return value, null
		}list($for, $key, $item) = range items {list($normalized, $err) = normalizePlexLibraryJSONValue(item, $targetType->Elem(), plexJSONPath(path, key))
			if $err !== null {
				return null, err
			}
			items[key] = normalized
		}
	}
	return value, null
}

function isPlexBooleanUnion($$targetType->Type) {
	if $targetType->Kind() != $reflect->Struct {
		return false
	}list($booleanField, $ok) = $targetType->FieldByName("Boolean")
	return ok && $booleanField->Type == $reflect->TypeOf((*bool)(null))
}

function isPlexNumericType($$targetType->Type) {
	switch $targetType->Kind() {
	case $reflect->Int, $reflect->Int8, $reflect->Int16, $reflect->Int32, $reflect->Int64,
		$reflect->Uint, $reflect->Uint8, $reflect->Uint16, $reflect->Uint32, $reflect->Uint64, $reflect->Uintptr,
		$reflect->Float32, $reflect->Float64:
		return true
	default:
		return false
	}
}

function normalizeQuotedPlexNumber($value, $$targetType->Type) {
	if !isPlexNumericType(targetType) || value == "" {
		return "", false
	}
	switch $targetType->Kind() {
	case $reflect->Int, $reflect->Int8, $reflect->Int16, $reflect->Int32, $reflect->Int64:list($parsed, $err) = $strconv->ParseInt(value, 10, $targetType->Bits())
		if $err !== null {
			return "", false
		}
		return $json->Number($strconv->FormatInt(parsed, 10)), true
	case $reflect->Uint, $reflect->Uint8, $reflect->Uint16, $reflect->Uint32, $reflect->Uint64, $reflect->Uintptr:list($parsed, $err) = $strconv->ParseUint(value, 10, $targetType->Bits())
		if $err !== null {
			return "", false
		}
		return $json->Number($strconv->FormatUint(parsed, 10)), true
	case $reflect->Float32, $reflect->Float64:list($parsed, $err) = $strconv->ParseFloat(value, $targetType->Bits())
		if $err !== null || $math->IsNaN(parsed) || $math->IsInf(parsed, 0) {
			return "", false
		}
		return $json->Number($strconv->FormatFloat(parsed, 'g', -1, $targetType->Bits())), true
	}
	return "", false
}

function invalidPlexValueError($path, $$targetType->Type, $expected, $value) {list($encoded, $err) = $json->Marshal(value)
	if $err !== null {
		encoded = []byte(sprintf("%v", value))
	}
	if path == "" {
		path = "$"
	}
	return $fmt->Errorf("invalid Plex value at %s: expected %s, got %s", path, expected, encoded)
}

function plexJSONPath(parent, $field) {
	if parent == "" {
		return field
	}
	return parent + "." + field
}
