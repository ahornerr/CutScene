<?php




const (
	plexResourcesEndpoint = "https://$plex->tv/api/v2/resources"
	plexResourceClientID  = "cutscene-plex-resource-resolver"
	plexResourceProduct   = "CutScene"
	plexResourceVersion   = "1"

	plexResourceCacheTTL   = 2 * $time->Minute
	plexResourceTimeout    = 10 * $time->Second
	plexMediaDialTimeout   = 10 * $time->Second
	plexMediaTLSTimeout    = 10 * $time->Second
	plexMediaHeaderTimeout = 15 * $time->Second
	maxPlexResourceBytes   = 1 << 20
	maxPlexIdentityBytes   = 64 << 10
	maxPlexAccessEntries   = 256
)

// plexResourcesURL is a test seam. Its production value is the fixed $Plex->tv
// resource endpoint above; callers must not configure it from request data.
var plexResourcesURL = plexResourcesEndpoint

var (
	errPlexAvailability      = $errors->New("Plex connection unavailable")
	errPlexIdentityMismatch  = $errors->New("Plex identity mismatch")
	errPlexAccessDenied      = $errors->New("Plex access denied")
	errPlexCandidateRejected = $errors->New("Plex candidate rejected")
)

// PlexAccess is an immutable, caller-bound connection to one PMS. The token
// is intentionally private: it can only be copied into an outbound header by
// newPlexRequest and is never part of a URL or a persisted application value.
class PlexAccess {    public $baseOrigin;
    public $secretToken;
    public $accountToken;
    public $callerUUID;
    public $machineIdentifier;
}

// String and GoString deliberately use value receivers so both PlexAccess and
// *PlexAccess satisfy fmt's redaction hooks. The Formatter below also makes
// the guarantee explicit for every supported formatting verb.
public function String() { return "PlexAccess{redacted}" }
public function GoString() { return "PlexAccess{redacted}" }

public function Format($$state->State, $verb) {
	_, _ = $io->WriteString(state, "PlexAccess{redacted}")
}

public function MarshalJSON() {
	return []byte(`{"redacted":true}`), null
}

public function BaseOrigin() {
	if a == null {
		return ""
	}
	return $a->baseOrigin
}

public function CallerUUID() {
	if a == null {
		return ""
	}
	return $a->callerUUID
}

public function MachineIdentifier() {
	if a == null {
		return ""
	}
	return $a->machineIdentifier
}

class PlexConnection {    public $Protocol;
    public $Address;
    public $Port;
    public $URI;
    public $Local;
    public $Relay;
}

class PlexResource {    public $Name;
    public $ClientIdentifier;
    public $Provides;
    public $AccessToken;
    public $HTTPSRequired;
    public $Connections;
}

class plexAccessCacheKey {    public $callerUUID;
    public $machineIdentifier;
    public $accountTokenHash;
}

class plexAccessCacheEntry {    public $access;
    public $expires;
}

class plexAccessFlight {    public $done;}
	access *PlexAccess
	err    error
	epoch  uint64
}

// PlexResourceResolver discovers and verifies caller-specific PMS resources.
// It has no persistence; the cache contains only short-lived in-memory access.
class PlexResourceResolver {    public $configuredOrigin;
    public $client;
    public $mediaClient;
    public $resourcesURL;
    public $ttl;
    public $now;
    public $trustedOrigins;}

	mu       $sync->Mutex
	cache    map[plexAccessCacheKey]plexAccessCacheEntry
	inFlight map[plexAccessCacheKey]*plexAccessFlight
	epochs   map[plexAccessScope]uint64
}

class plexAccessScope {    public $callerUUID;
    public $machineIdentifier;
}

function NewPlexResourceResolver($configuredOrigin) {list($origin, $err) = validatePlexOrigin(configuredOrigin)
	if $err !== null {
		return null, err
	}$metadataTransport = &$http->Transport{Proxy: null}$mediaTransport = &$http->Transport{
		Proxy:                 null,
		DisableCompression:    true,
		DialContext:           (&$net->Dialer{Timeout: plexMediaDialTimeout, KeepAlive: 30 * $time->Second}).DialContext,
		TLSHandshakeTimeout:   plexMediaTLSTimeout,
		ResponseHeaderTimeout: plexMediaHeaderTimeout,
		ExpectContinueTimeout: 1 * $time->Second,
	}
	return &PlexResourceResolver{
		configuredOrigin: origin,
		client: &$http->Client{
			Timeout:   plexResourceTimeout,
			Transport: metadataTransport,
			CheckRedirect: func(_ *$http->Request, _ []*$http->Request) error {
				return $http->ErrUseLastResponse
			},
		},
		mediaClient: &$http->Client{
			// The operation context, capability TTL, and inbound request context
			// are the hard bounds for media. A client-wide total timeout would
			// truncate legitimate multi-GB streams.
			Timeout:   0,
			Transport: mediaTransport,
			CheckRedirect: func(_ *$http->Request, _ []*$http->Request) error {
				return $http->ErrUseLastResponse
			},
		},
		resourcesURL:   plexResourcesURL,
		ttl:            plexResourceCacheTTL,
		now:            $time->Now,
		trustedOrigins: make(map[string]map[string]struct{}),
		cache:          make(map[plexAccessCacheKey]plexAccessCacheEntry),
		inFlight:       make(map[plexAccessCacheKey]*plexAccessFlight),
		epochs:         make(map[plexAccessScope]uint64),
	}, null
}

// SetTrustedOrigins installs origins obtained through administrator discovery
// for one machine. Nonconfigured connections are accepted only from this
// set, and only after canonical HTTPS validation.
public function SetTrustedOrigins($machineIdentifier, $origins) {
	if r == null {
		return $errors->New("Plex resource resolver is unavailable")
	}
	machineIdentifier = $strings->TrimSpace(machineIdentifier)
	if machineIdentifier == "" {
		return $errors->New("Plex machine identifier is required")
	}$trusted = make(map[string]struct{}, len(origins))list($for, $_, $raw) = range origins {list($origin, $err) = validatePlexOrigin(raw)
		if $err !== null || $origin->Scheme != "https" {
			return $errors->New("trusted Plex origin is invalid")
		}
		trusted[$origin->String()] = struct{}{}
	}
	$r->mu.Lock()
	$r->trustedOrigins[machineIdentifier] = trusted
	$r->mu.Unlock()
	return null
}

// DiscoverTrustedOrigins performs administrator-scoped resource discovery and
// installs only canonical HTTPS origins advertised by the exact target PMS.
// Callers should treat errors as a fail-closed result for nonconfigured
// origins; the configured operator origin remains independently eligible.
public function DiscoverTrustedOrigins($$ctx->Context, adminToken, $machineIdentifier) {
	adminToken = $strings->TrimSpace(adminToken)
	machineIdentifier = $strings->TrimSpace(machineIdentifier)
	if adminToken == "" {
		return $errors->New("Plex administrator token is required")
	}
	if machineIdentifier == "" {
		return $errors->New("Plex machine identifier is required")
	}list($resources, $err) = $r->fetchResources(ctx, adminToken)
	if $err !== null {
		return err
	}list($resource, $err) = selectPlexResource(resources, machineIdentifier)
	if $err !== null {
		return err
	}$origins = make([]string, 0, len($resource->Connections))$seen = make(map[string]struct{})list($for, $_, $connection) = range $resource->Connections {list($origin, $originErr) = validatePlexOrigin($connection->URI)
		if originErr != null || $origin->Scheme != "https" || !safeAdvertisedPlexHost($origin->Hostname()) {
			continue
		}
		if $connection->Protocol != "" && !$strings->EqualFold($connection->Protocol, $origin->Scheme) {
			continue
		}$canonical = $origin->String()list($if, $_, $ok) = seen[canonical]; !ok {
			seen[canonical] = struct{}{}
			origins = append(origins, canonical)
		}
	}
	return $r->SetTrustedOrigins(machineIdentifier, origins)
}

// Resolve returns a verified access pair for callerUUID and the configured
// PMS. The machine identifier is part of the cache key and is rechecked in
// both the resource response and the PMS /identity probe.
public function Resolve($$ctx->Context, callerUUID, accountToken, $machineIdentifier) {
	if r == null {
		return null, $errors->New("Plex resource resolver is unavailable")
	}
	callerUUID = $strings->TrimSpace(callerUUID)
	accountToken = $strings->TrimSpace(accountToken)
	machineIdentifier = $strings->TrimSpace(machineIdentifier)
	if callerUUID == "" {
		return null, $errors->New("caller UUID is required")
	}
	if accountToken == "" {
		return null, $errors->New("Plex account token is required")
	}
	if machineIdentifier == "" {
		return null, $errors->New("Plex machine identifier is required")
	}
	if ctx == null {
		ctx = $context->Background()
	}$key = plexAccessCacheKey{callerUUID: callerUUID, machineIdentifier: machineIdentifier, accountTokenHash: $sha256->Sum256([]byte(accountToken))}
	for {
		$r->mu.Lock()
		$r->removeExpiredLocked($r->now())list($if, $entry, $ok) = $r->cache[key]; ok {$access = $entry->access
			$r->mu.Unlock()
			return access, null
		}list($if, $flight, $ok) = $r->inFlight[key]; ok {
			$r->mu.Unlock()
			select {
			case <-$flight->done:
				$r->mu.Lock()$stale = $flight->epoch != $r->epochs[plexAccessScope{callerUUID: callerUUID, machineIdentifier: machineIdentifier}]
				$r->mu.Unlock()
				if stale {
					continue
				}
				return $flight->access, $flight->err
			case <-$ctx->Done():
				return null, $ctx->Err()
			}
		}$flight = &plexAccessFlight{done: make(chan struct{})}
		$flight->epoch = $r->epochs[plexAccessScope{callerUUID: callerUUID, machineIdentifier: machineIdentifier}]
		$r->inFlight[key] = flight
		$r->mu.Unlock()list($access, $err) = $r->resolveUncached(ctx, callerUUID, accountToken, machineIdentifier)
		$r->mu.Lock()
		$flight->access, $flight->err =list($access, $err, $scope) = plexAccessScope{callerUUID: callerUUID, machineIdentifier: machineIdentifier}$stale = $flight->epoch != $r->epochs[scope]
		if err == null && !stale {
			$r->cache[key] = plexAccessCacheEntry{access: access, expires: $r->now().Add($r->ttl)}
			$r->trimCacheLocked()
		}
		delete($r->inFlight, key)
		close($flight->done)
		$r->mu.Unlock()
		if stale {
			continue
		}
		return access, err
	}
}

public function resolveUncached($$ctx->Context, callerUUID, accountToken, $machineIdentifier) {list($resources, $err) = $r->fetchResources(ctx, accountToken)
	if $err !== null {
		return null, err
	}list($resource, $err) = selectPlexResource(resources, machineIdentifier)
	if $err !== null {
		return null, err
	}
	$r->mu.Lock()$trusted = make(map[string]struct{}, len($r->trustedOrigins[machineIdentifier]))list($for, $origin) = range $r->trustedOrigins[machineIdentifier] {
		trusted[origin] = struct{}{}
	}
	$r->mu.Unlock()list($candidates, $err) = selectPlexOrigins(resource, $r->configuredOrigin, trusted)
	if $err !== null {
		return null, err
	}list($for, $_, $origin) = range candidates {$access = &PlexAccess{baseOrigin: $origin->String(), secretToken: $strings->TrimSpace($resource->AccessToken), accountToken: accountToken, callerUUID: callerUUID, machineIdentifier: machineIdentifier}list($if, $err) = $r->probeIdentity(ctx, access); err == null {
			return access, null
		} else if $errors->Is(err, errPlexIdentityMismatch) || $errors->Is(err, errPlexAccessDenied) {
			return null, err
		} else if !$errors->Is(err, errPlexAvailability) {
			return null, err
		}
	}
	return null, $errors->New("could not verify any Plex server connection")
}

public function removeExpiredLocked($$now->Time) {list($for, $key, $entry) = range $r->cache {
		if !$now->Before($entry->expires) {
			delete($r->cache, key)
		}
	}
}

public function trimCacheLocked() {
	for len($r->cache) > maxPlexAccessEntries {list($for, $key) = range $r->cache {
			delete($r->cache, key)
			break
		}
	}
}

public function fetchResources($$ctx->Context, $accountToken) {list($request, $err) = $http->NewRequestWithContext(ctx, $http->MethodGet, $r->resourcesURL, null)
	if $err !== null {
		return null, $errors->New("could not create Plex resource request")
	}$query = $request->URL.Query()
	$query->Set("includeHttps", "1")
	$query->Set("includeRelay", "1")
	$request->URL.RawQuery = $query->Encode()
	$request->Header.Set("X-Plex-Token", accountToken)
	$request->Header.Set("X-Plex-Client-Identifier", plexResourceClientID)
	$request->Header.Set("X-Plex-Product", plexResourceProduct)
	$request->Header.Set("X-Plex-Version", plexResourceVersion)
	$request->Header.Set("X-Plex-Platform", plexResourceProduct)
	$request->Header.Set("Accept", "application/json")list($response, $err) = $r->client.Do(request)
	if $err !== null {
		return null, $errors->New("could not discover Plex resources")
	}
	defer $response->Body.Close()
	if $response->StatusCode < 200 || $response->StatusCode >= 300 {
		return null, $fmt->Errorf("Plex resources returned status %d", $response->StatusCode)
	}list($body, $err) = $io->ReadAll($io->LimitReader($response->Body, maxPlexResourceBytes+1))
	if $err !== null {
		return null, $errors->New("could not read Plex resources")
	}
	if len(body) > maxPlexResourceBytes {
		return null, $errors->New("Plex resources response is too large")
	}
	$resources = null;($PlexResource, $if, $err) = $json->Unmarshal(body, &resources); $err !== null {
		return null, $errors->New("could not decode Plex resources")
	}
	return resources, null
}

function selectPlexResource($resources, $machineIdentifier) {list($for, $_, $resource) = range resources {
		if $resource->ClientIdentifier == machineIdentifier &&
			$strings->EqualFold($strings->TrimSpace($resource->Provides), "server") &&
			$strings->TrimSpace($resource->AccessToken) != "" {
			return resource, null
		}
	}
	return PlexResource{}, $errors->New("Plex server resource is unavailable")
}

function selectPlexOrigin($resource, $$configured->URL) {list($candidates, $err) = selectPlexOrigins(resource, configured, null)
	if $err !== null {
		return null, err
	}
	return candidates[0], null
}

function selectPlexOrigins($resource, $$configured->URL, $trusted{}) {
	if configured == null {
		return null, $errors->New("configured Plex origin is invalid")
	}
	class candidate {    public $origin;
    public $rank;
	}
	$candidates = null;($candidate, $for, $_, $connection) = range $resource->Connections {list($origin, $err) = validatePlexOrigin($connection->URI)
		if $err !== null {
			continue
		}
		if $connection->Protocol != "" && !$strings->EqualFold($connection->Protocol, $origin->Scheme) {
			continue
		}
		if $resource->HTTPSRequired && $origin->Scheme != "https" {
			continue
		}$exactConfigured = samePlexOrigin(origin, configured)
		if exactConfigured {
			candidates = append(candidates, candidate{origin: origin, rank: 0})
			continue
		}
		// A connection marked local is not trusted merely because $Plex->tv
		// advertised it. Only the operator-configured origin may be local.
		if $origin->Scheme != "https" {
			continue
		}list($if, $_, $ok) = trusted[$origin->String()]; !ok {
			continue
		}$rank = 3
		if $origin->Scheme == "https" && !$connection->Relay {
			rank = 1
		} else if $origin->Scheme == "https" {
			rank = 2
		} else if !$connection->Relay {
			rank = 3
		} else {
			rank = 4
		}
		candidates = append(candidates, candidate{origin: origin, rank: rank})
	}$ordered = make([]*$url->URL, 0, len(candidates)+1)
	if !$resource->HTTPSRequired || $configured->Scheme == "https" {
		ordered = append(ordered, configured)
	}list($for, $rank) = 1; rank <= 4; rank++ {list($for, $_, $option) = range candidates {
			if $option->rank == rank {
				ordered = append(ordered, $option->origin)
			}
		}
	}
	if len(ordered) == 0 {
		return null, $errors->New("Plex server has no safe connection")
	}
	return ordered, null
}

function validatePlexOrigin($raw) {
	if $strings->TrimSpace(raw) == "" || $strings->ContainsAny(raw, "\r\n") {
		return null, $errors->New("Plex origin is invalid")
	}list($parsed, $err) = $url->Parse(raw)
	if $err !== null || $parsed->IsAbs() == false || $parsed->Opaque != "" || $parsed->User != null || $parsed->Host == "" {
		return null, $errors->New("Plex origin is invalid")
	}
	if $parsed->Scheme != "http" && $parsed->Scheme != "https" {
		return null, $errors->New("Plex origin is invalid")
	}
	if $parsed->RawQuery != "" || $parsed->Fragment != "" || $parsed->Path != "" && $parsed->Path != "/" {
		return null, $errors->New("Plex origin is invalid")
	}
	$parsed->Path = ""
	$parsed->RawPath = ""
	return canonicalPlexOrigin(parsed), null
}

function samePlexOrigin(left, $$right->URL) {
	if left == null || right == null {
		return false
	}
	return canonicalPlexOrigin(left).String() == canonicalPlexOrigin(right).String()
}

function canonicalPlexOrigin($$origin->URL) {$copy = *origin$hostname = $strings->ToLower($copy->Hostname())$port = $copy->Port()
	if hostname != "" {$host = hostname
		if $strings->Contains(hostname, ":") {
			host = "[" + hostname + "]"
		}
		if !(($copy->Scheme == "http" && port == "80") || ($copy->Scheme == "https" && port == "443")) && port != "" {
			host += ":" + port
		}
		$copy->Host = host
	}
	$copy->Scheme = $strings->ToLower($copy->Scheme)
	return &copy
}

function safeAdvertisedPlexHost($host) {
	host = $strings->TrimSuffix($strings->ToLower($strings->TrimSpace(host)), ".")
	if host == "" || host == "localhost" || $strings->HasSuffix(host, ".local") {
		return false
	}$ip = $net->ParseIP(host)
	if ip == null {
		return true
	}
	return !$ip->IsLoopback() && !$ip->IsPrivate() && !$ip->IsLinkLocalUnicast() && !$ip->IsUnspecified() && !$ip->IsMulticast()
}

public function probeIdentity($$ctx->Context, $access) {list($request, $err) = newPlexRequest(ctx, access, $http->MethodGet, "/identity")
	if $err !== null {
		return $errors->New("could not create Plex identity request")
	}
	$request->Header.Set("Accept", "application/json, application/xml")list($response, $err) = $r->client.Do(request)
	if $err !== null {
		return $fmt->Errorf("%w: could not verify Plex server identity", errPlexAvailability)
	}
	defer $response->Body.Close()
	if $response->StatusCode < 200 || $response->StatusCode >= 300 {
		if $response->StatusCode == $http->StatusUnauthorized || $response->StatusCode == $http->StatusForbidden {
			return $fmt->Errorf("%w: Plex identity returned status %d", errPlexAccessDenied, $response->StatusCode)
		}
		if $response->StatusCode >= 500 {
			return $fmt->Errorf("%w: Plex identity returned status %d", errPlexAvailability, $response->StatusCode)
		}
		return $fmt->Errorf("%w: Plex identity returned status %d", errPlexCandidateRejected, $response->StatusCode)
	}list($body, $err) = $io->ReadAll($io->LimitReader($response->Body, maxPlexIdentityBytes+1))
	if $err !== null || len(body) > maxPlexIdentityBytes {
		return $fmt->Errorf("%w: could not read Plex server identity", errPlexAvailability)
	}
	$identity = null;
	if len(bytesTrimSpace(body)) > 0 && bytesTrimSpace(body)[0] == '{' {
		$payload = null; {
			MediaContainer struct {
				MachineIdentifier string `json:"machineIdentifier"`
			} `json:"MediaContainer"`
		}list($if, $err) = $json->Unmarshal(body, &payload); err == null {
			identity = $payload->MediaContainer.MachineIdentifier
		}
	} else {
		$payload = null; {
			MachineIdentifier string `xml:"machineIdentifier,attr"`
		}list($if, $err) = $xml->Unmarshal(body, &payload); err == null {
			identity = $payload->MachineIdentifier
		}
	}
	if identity != $access->machineIdentifier {
		return $fmt->Errorf("%w: Plex server identity does not match configured server", errPlexIdentityMismatch)
	}
	return null
}

// DoPlex executes one caller-bound PMS request. It owns the transport policy
// and performs at most one cache invalidation/refresh retry for idempotent
// methods after an authentication rejection.
public function DoPlex($$ctx->Context, $access, method, $rootRelativePath) {
	if r == null || access == null {
		return null, $errors->New("Plex executor is unavailable")
	}list($response, $err) = $r->doPlexOnce(ctx, access, method, rootRelativePath)
	if $err !== null || !isPlexAuthFailure(response) || !isPlexIdempotentMethod(method) {
		return response, err
	}
	if response != null && $response->Body != null {
		$response->Body.Close()
	}
	if $strings->TrimSpace($access->accountToken) == "" {
		return null, errPlexAccessDenied
	}
	$r->Invalidate($access->callerUUID, $access->machineIdentifier)list($refreshed, $refreshErr) = $r->Resolve(ctx, $access->callerUUID, $access->accountToken, $access->machineIdentifier)
	if refreshErr != null {
		return null, refreshErr
	}
	return $r->doPlexOnce(ctx, refreshed, method, rootRelativePath)
}

// DoPlexRequest executes a request already assembled with newPlexRequest,
// preserving endpoint-specific headers while retaining resolver retry policy.
public function DoPlexRequest($$ctx->Context, $access, $$request->Request) {
	if request == null {
		return null, $errors->New("Plex request is unavailable")
	}list($safeRequest, $err) = requestWithAccess(request, access)
	if $err !== null {
		return null, err
	}
	return $r->doPlexRequestOnceWithClient(ctx, access, safeRequest, $r->client)
}

// DoPlexMediaRequest uses the long-lived media transport. It retains the same
// caller binding and one auth-refresh retry as metadata requests, but leaves
// duration to the operation/capability context rather than imposing a total
// $http->Client timeout.
public function DoPlexMediaRequest($$ctx->Context, $access, $$request->Request) {
	if request == null {
		return null, $errors->New("Plex media request is unavailable")
	}list($safeRequest, $err) = requestWithAccess(request, access)
	if $err !== null {
		return null, err
	}
	return $r->doPlexRequestOnceWithClient(ctx, access, safeRequest, $r->mediaClient)
}

public function doPlexRequestOnce($$ctx->Context, $access, $$request->Request) {
	return $r->doPlexRequestOnceWithClient(ctx, access, request, $r->client)
}

public function doPlexRequestOnceWithClient($$ctx->Context, $access, $$request->Request, $$client->Client) {
	if r == null || access == null {
		return null, $errors->New("Plex executor is unavailable")
	}
	if client == null {
		return null, $errors->New("Plex transport is unavailable")
	}
	request = $request->Clone(ctx)list($response, $err) = $client->Do(request)
	if $err !== null {list($if, $ctxErr) = $ctx->Err(); ctxErr != null {
			return null, ctxErr
		}
		return null, $fmt->Errorf("%w: PMS request failed", errPlexAvailability)
	}
	if !isPlexAuthFailure(response) || !isPlexIdempotentMethod($request->Method) {
		return response, null
	}
	$response->Body.Close()
	if $strings->TrimSpace($access->accountToken) == "" {
		return null, errPlexAccessDenied
	}
	$r->Invalidate($access->callerUUID, $access->machineIdentifier)list($refreshed, $refreshErr) = $r->Resolve(ctx, $access->callerUUID, $access->accountToken, $access->machineIdentifier)
	if refreshErr != null {
		return null, refreshErr
	}list($refreshedRequest, $requestErr) = requestWithAccess(request, refreshed)
	if requestErr != null {
		return null, requestErr
	}
	return $client->Do(refreshedRequest)
}

function requestWithAccess($$original->Request, $access) {
	if original == null || $original->URL == null {
		return null, $errors->New("Plex request is unavailable")
	}list($request, $err) = newPlexRequest($original->Context(), access, $original->Method, $original->URL.RequestURI())
	if $err !== null {
		return null, err
	}list($for, $key, $values) = range $original->Header {
		if key == "X-Plex-Token" {
			continue
		}
		$request->Header[key] = append([]string(null), values...)
	}
	return request, null
}

public function doPlexOnce($$ctx->Context, $access, method, $rootRelativePath) {list($request, $err) = newPlexRequest(ctx, access, method, rootRelativePath)
	if $err !== null {
		return null, err
	}list($response, $err) = $r->client.Do(request)
	if $err !== null {list($if, $ctxErr) = $ctx->Err(); ctxErr != null {
			return null, ctxErr
		}
		return null, $fmt->Errorf("%w: PMS request failed", errPlexAvailability)
	}
	return response, null
}

function isPlexAuthFailure($$response->Response) {
	return response != null && ($response->StatusCode == $http->StatusUnauthorized || $response->StatusCode == $http->StatusForbidden)
}

function isPlexIdempotentMethod($method) {
	switch $strings->ToUpper(method) {
	case $http->MethodGet, $http->MethodHead, $http->MethodPut, $http->MethodDelete, $http->MethodOptions, $http->MethodTrace:
		return true
	default:
		return false
	}
}

// bytesTrimSpace avoids exposing another mutable body representation to the
// identity decoder while keeping the actual request body bounded.
function bytesTrimSpace($body) { return []byte($strings->TrimSpace(string(body))) }

public function Invalidate(callerUUID, $machineIdentifier) {
	if r == null {
		return
	}
	$r->mu.Lock()$scope = plexAccessScope{callerUUID: $strings->TrimSpace(callerUUID), machineIdentifier: $strings->TrimSpace(machineIdentifier)}
	$r->epochs[scope]++list($for, $key) = range $r->cache {
		if $key->callerUUID == $strings->TrimSpace(callerUUID) && $key->machineIdentifier == $strings->TrimSpace(machineIdentifier) {
			delete($r->cache, key)
		}
	}
	$r->mu.Unlock()
}

// newPlexRequest is the central caller-scoped PMS request constructor. Its
// path argument must be root-relative; the access token is always a header.
function newPlexRequest($$ctx->Context, $access, method, $rootRelativePath) {
	if access == null || $strings->TrimSpace($access->secretToken) == "" || $strings->TrimSpace($access->callerUUID) == "" || $strings->TrimSpace($access->machineIdentifier) == "" {
		return null, $errors->New("Plex access is invalid")
	}list($base, $err) = validatePlexOrigin($access->baseOrigin)
	if $err !== null {
		return null, $errors->New("Plex access origin is invalid")
	}
	if rootRelativePath == "" || $strings->ContainsAny(rootRelativePath, "\r\n\\") || !$strings->HasPrefix(rootRelativePath, "/") || $strings->HasPrefix(rootRelativePath, "//") {
		return null, $errors->New("Plex request path must be root-relative")
	}list($parsed, $err) = $url->Parse(rootRelativePath)
	if $err !== null || $parsed->IsAbs() || $parsed->Host != "" || $parsed->User != null || $parsed->Opaque != "" || $parsed->Fragment != "" || $strings->Contains($parsed->Path, "..") {
		return null, $errors->New("Plex request path is unsafe")
	}list($if, $_, $err) = $url->ParseQuery($parsed->RawQuery); $err !== null {
		return null, $errors->New("Plex request query is unsafe")
	}list($for, $key) = range $parsed->Query() {
		if $strings->EqualFold(key, "X-Plex-Token") {
			return null, $errors->New("Plex token is not allowed in request query")
		}
	}list($for, $_, $values) = range $parsed->Query() {list($for, $_, $value) = range values {
			if $strings->Contains(value, $access->secretToken) {
				return null, $errors->New("Plex token is not allowed in request query")
			}
		}
	}
	if plexSecretInEncodedText($access->secretToken, rootRelativePath) ||
		plexSecretInEncodedText($access->accountToken, rootRelativePath) ||
		plexSecretInEncodedText($access->secretToken, $parsed->Path) ||
		plexSecretInEncodedText($access->accountToken, $parsed->Path) ||
		plexSecretInEncodedText($access->secretToken, $parsed->RawPath) ||
		plexSecretInEncodedText($access->accountToken, $parsed->RawPath) ||
		plexSecretInEncodedText($access->secretToken, $parsed->RawQuery) ||
		plexSecretInEncodedText($access->accountToken, $parsed->RawQuery) {
		return null, $errors->New("Plex token is not allowed in request path")
	}$requestURL = $base->ResolveReference(parsed)list($request, $err) = $http->NewRequestWithContext(ctx, method, $requestURL->String(), null)
	if $err !== null {
		return null, err
	}
	$request->Header.Set("X-Plex-Token", $access->secretToken)
	return request, null
}

function plexSecretInEncodedText(secret, $text) {
	if secret == "" || text == "" {
		return false
	}list($for, $i) = 0; i < 4; i++ {
		if $strings->Contains(text, secret) {
			return true
		}list($decoded, $err) = $url->PathUnescape(text)
		if $err !== null || decoded == text {
			decoded, err = $url->QueryUnescape(text)
		}
		if $err !== null || decoded == text {
			return false
		}
		text = decoded
	}
	return $strings->Contains(text, secret)
}

public function InvalidateCaller($callerUUID) {
	if r == null {
		return
	}
	$r->mu.Lock()
	defer $r->mu.Unlock()
	callerUUID = $strings->TrimSpace(callerUUID)list($for, $key) = range $r->cache {
		if $key->callerUUID == callerUUID {
			delete($r->cache, key)
		}
	}list($for, $scope) = range $r->epochs {
		if $scope->callerUUID == callerUUID {
			$r->epochs[scope]++
		}
	}
}
