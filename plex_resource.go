package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	plexResourcesEndpoint = "https://plex.tv/api/v2/resources"
	plexResourceClientID  = "cutscene-plex-resource-resolver"
	plexResourceProduct   = "CutScene"
	plexResourceVersion   = "1"

	plexResourceCacheTTL   = 2 * time.Minute
	plexResourceTimeout    = 10 * time.Second
	plexMediaDialTimeout   = 10 * time.Second
	plexMediaTLSTimeout    = 10 * time.Second
	plexMediaHeaderTimeout = 15 * time.Second
	maxPlexResourceBytes   = 1 << 20
	maxPlexIdentityBytes   = 64 << 10
	maxPlexAccessEntries   = 256
)

// plexResourcesURL is a test seam. Its production value is the fixed Plex.tv
// resource endpoint above; callers must not configure it from request data.
var plexResourcesURL = plexResourcesEndpoint

var (
	errPlexAvailability      = errors.New("Plex connection unavailable")
	errPlexIdentityMismatch  = errors.New("Plex identity mismatch")
	errPlexAccessDenied      = errors.New("Plex access denied")
	errPlexCandidateRejected = errors.New("Plex candidate rejected")
)

// PlexAccess is an immutable, caller-bound connection to one PMS. The token
// is intentionally private: it can only be copied into an outbound header by
// newPlexRequest and is never part of a URL or a persisted application value.
type PlexAccess struct {
	baseOrigin        string
	secretToken       string
	accountToken      string
	callerUUID        string
	machineIdentifier string
}

// String and GoString deliberately use value receivers so both PlexAccess and
// *PlexAccess satisfy fmt's redaction hooks. The Formatter below also makes
// the guarantee explicit for every supported formatting verb.
func (a PlexAccess) String() string   { return "PlexAccess{redacted}" }
func (a PlexAccess) GoString() string { return "PlexAccess{redacted}" }

func (a PlexAccess) Format(state fmt.State, verb rune) {
	_, _ = io.WriteString(state, "PlexAccess{redacted}")
}

func (a *PlexAccess) MarshalJSON() ([]byte, error) {
	return []byte(`{"redacted":true}`), nil
}

func (a *PlexAccess) BaseOrigin() string {
	if a == nil {
		return ""
	}
	return a.baseOrigin
}

func (a *PlexAccess) CallerUUID() string {
	if a == nil {
		return ""
	}
	return a.callerUUID
}

func (a *PlexAccess) MachineIdentifier() string {
	if a == nil {
		return ""
	}
	return a.machineIdentifier
}

type PlexConnection struct {
	Protocol string `json:"protocol"`
	Address  string `json:"address"`
	Port     int    `json:"port"`
	URI      string `json:"uri"`
	Local    bool   `json:"local"`
	Relay    bool   `json:"relay"`
}

type PlexResource struct {
	Name             string           `json:"name"`
	ClientIdentifier string           `json:"clientIdentifier"`
	Provides         string           `json:"provides"`
	AccessToken      string           `json:"accessToken"`
	HTTPSRequired    bool             `json:"httpsRequired"`
	Connections      []PlexConnection `json:"connections"`
}

type plexAccessCacheKey struct {
	callerUUID        string
	machineIdentifier string
	accountTokenHash  [sha256.Size]byte
}

type plexAccessCacheEntry struct {
	access  *PlexAccess
	expires time.Time
}

type plexAccessFlight struct {
	done   chan struct{}
	access *PlexAccess
	err    error
	epoch  uint64
}

// PlexResourceResolver discovers and verifies caller-specific PMS resources.
// It has no persistence; the cache contains only short-lived in-memory access.
type PlexResourceResolver struct {
	configuredOrigin *url.URL
	client           *http.Client
	mediaClient      *http.Client
	resourcesURL     string
	ttl              time.Duration
	now              func() time.Time
	trustedOrigins   map[string]map[string]struct{}

	mu       sync.Mutex
	cache    map[plexAccessCacheKey]plexAccessCacheEntry
	inFlight map[plexAccessCacheKey]*plexAccessFlight
	epochs   map[plexAccessScope]uint64
}

type plexAccessScope struct {
	callerUUID        string
	machineIdentifier string
}

func NewPlexResourceResolver(configuredOrigin string) (*PlexResourceResolver, error) {
	origin, err := validatePlexOrigin(configuredOrigin)
	if err != nil {
		return nil, err
	}
	metadataTransport := &http.Transport{Proxy: nil}
	mediaTransport := &http.Transport{
		Proxy:                 nil,
		DisableCompression:    true,
		DialContext:           (&net.Dialer{Timeout: plexMediaDialTimeout, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout:   plexMediaTLSTimeout,
		ResponseHeaderTimeout: plexMediaHeaderTimeout,
		ExpectContinueTimeout: 1 * time.Second,
	}
	return &PlexResourceResolver{
		configuredOrigin: origin,
		client: &http.Client{
			Timeout:   plexResourceTimeout,
			Transport: metadataTransport,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		mediaClient: &http.Client{
			// The operation context, capability TTL, and inbound request context
			// are the hard bounds for media. A client-wide total timeout would
			// truncate legitimate multi-GB streams.
			Timeout:   0,
			Transport: mediaTransport,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		resourcesURL:   plexResourcesURL,
		ttl:            plexResourceCacheTTL,
		now:            time.Now,
		trustedOrigins: make(map[string]map[string]struct{}),
		cache:          make(map[plexAccessCacheKey]plexAccessCacheEntry),
		inFlight:       make(map[plexAccessCacheKey]*plexAccessFlight),
		epochs:         make(map[plexAccessScope]uint64),
	}, nil
}

// SetTrustedOrigins installs origins obtained through administrator discovery
// for one machine. Nonconfigured connections are accepted only from this
// set, and only after canonical HTTPS validation.
func (r *PlexResourceResolver) SetTrustedOrigins(machineIdentifier string, origins []string) error {
	if r == nil {
		return errors.New("Plex resource resolver is unavailable")
	}
	machineIdentifier = strings.TrimSpace(machineIdentifier)
	if machineIdentifier == "" {
		return errors.New("Plex machine identifier is required")
	}
	trusted := make(map[string]struct{}, len(origins))
	for _, raw := range origins {
		origin, err := validatePlexOrigin(raw)
		if err != nil || origin.Scheme != "https" {
			return errors.New("trusted Plex origin is invalid")
		}
		trusted[origin.String()] = struct{}{}
	}
	r.mu.Lock()
	r.trustedOrigins[machineIdentifier] = trusted
	r.mu.Unlock()
	return nil
}

// DiscoverTrustedOrigins performs administrator-scoped resource discovery and
// installs only canonical HTTPS origins advertised by the exact target PMS.
// Callers should treat errors as a fail-closed result for nonconfigured
// origins; the configured operator origin remains independently eligible.
func (r *PlexResourceResolver) DiscoverTrustedOrigins(ctx context.Context, adminToken, machineIdentifier string) error {
	adminToken = strings.TrimSpace(adminToken)
	machineIdentifier = strings.TrimSpace(machineIdentifier)
	if adminToken == "" {
		return errors.New("Plex administrator token is required")
	}
	if machineIdentifier == "" {
		return errors.New("Plex machine identifier is required")
	}
	resources, err := r.fetchResources(ctx, adminToken)
	if err != nil {
		return err
	}
	resource, err := selectPlexResource(resources, machineIdentifier)
	if err != nil {
		return err
	}
	origins := make([]string, 0, len(resource.Connections))
	seen := make(map[string]struct{})
	for _, connection := range resource.Connections {
		origin, originErr := validatePlexOrigin(connection.URI)
		if originErr != nil || origin.Scheme != "https" || !safeAdvertisedPlexHost(origin.Hostname()) {
			continue
		}
		if connection.Protocol != "" && !strings.EqualFold(connection.Protocol, origin.Scheme) {
			continue
		}
		canonical := origin.String()
		if _, ok := seen[canonical]; !ok {
			seen[canonical] = struct{}{}
			origins = append(origins, canonical)
		}
	}
	return r.SetTrustedOrigins(machineIdentifier, origins)
}

// Resolve returns a verified access pair for callerUUID and the configured
// PMS. The machine identifier is part of the cache key and is rechecked in
// both the resource response and the PMS /identity probe.
func (r *PlexResourceResolver) Resolve(ctx context.Context, callerUUID, accountToken, machineIdentifier string) (*PlexAccess, error) {
	if r == nil {
		return nil, errors.New("Plex resource resolver is unavailable")
	}
	callerUUID = strings.TrimSpace(callerUUID)
	accountToken = strings.TrimSpace(accountToken)
	machineIdentifier = strings.TrimSpace(machineIdentifier)
	if callerUUID == "" {
		return nil, errors.New("caller UUID is required")
	}
	if accountToken == "" {
		return nil, errors.New("Plex account token is required")
	}
	if machineIdentifier == "" {
		return nil, errors.New("Plex machine identifier is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	key := plexAccessCacheKey{callerUUID: callerUUID, machineIdentifier: machineIdentifier, accountTokenHash: sha256.Sum256([]byte(accountToken))}
	for {
		r.mu.Lock()
		r.removeExpiredLocked(r.now())
		if entry, ok := r.cache[key]; ok {
			access := entry.access
			r.mu.Unlock()
			return access, nil
		}
		if flight, ok := r.inFlight[key]; ok {
			r.mu.Unlock()
			select {
			case <-flight.done:
				r.mu.Lock()
				stale := flight.epoch != r.epochs[plexAccessScope{callerUUID: callerUUID, machineIdentifier: machineIdentifier}]
				r.mu.Unlock()
				if stale {
					continue
				}
				return flight.access, flight.err
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		flight := &plexAccessFlight{done: make(chan struct{})}
		flight.epoch = r.epochs[plexAccessScope{callerUUID: callerUUID, machineIdentifier: machineIdentifier}]
		r.inFlight[key] = flight
		r.mu.Unlock()

		access, err := r.resolveUncached(ctx, callerUUID, accountToken, machineIdentifier)
		r.mu.Lock()
		flight.access, flight.err = access, err
		scope := plexAccessScope{callerUUID: callerUUID, machineIdentifier: machineIdentifier}
		stale := flight.epoch != r.epochs[scope]
		if err == nil && !stale {
			r.cache[key] = plexAccessCacheEntry{access: access, expires: r.now().Add(r.ttl)}
			r.trimCacheLocked()
		}
		delete(r.inFlight, key)
		close(flight.done)
		r.mu.Unlock()
		if stale {
			continue
		}
		return access, err
	}
}

func (r *PlexResourceResolver) resolveUncached(ctx context.Context, callerUUID, accountToken, machineIdentifier string) (*PlexAccess, error) {
	resources, err := r.fetchResources(ctx, accountToken)
	if err != nil {
		return nil, err
	}
	resource, err := selectPlexResource(resources, machineIdentifier)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	trusted := make(map[string]struct{}, len(r.trustedOrigins[machineIdentifier]))
	for origin := range r.trustedOrigins[machineIdentifier] {
		trusted[origin] = struct{}{}
	}
	r.mu.Unlock()
	candidates, err := selectPlexOrigins(resource, r.configuredOrigin, trusted)
	if err != nil {
		return nil, err
	}
	for _, origin := range candidates {
		access := &PlexAccess{baseOrigin: origin.String(), secretToken: strings.TrimSpace(resource.AccessToken), accountToken: accountToken, callerUUID: callerUUID, machineIdentifier: machineIdentifier}
		if err := r.probeIdentity(ctx, access); err == nil {
			return access, nil
		} else if errors.Is(err, errPlexIdentityMismatch) || errors.Is(err, errPlexAccessDenied) {
			return nil, err
		} else if !errors.Is(err, errPlexAvailability) {
			return nil, err
		}
	}
	return nil, errors.New("could not verify any Plex server connection")
}

func (r *PlexResourceResolver) removeExpiredLocked(now time.Time) {
	for key, entry := range r.cache {
		if !now.Before(entry.expires) {
			delete(r.cache, key)
		}
	}
}

func (r *PlexResourceResolver) trimCacheLocked() {
	for len(r.cache) > maxPlexAccessEntries {
		for key := range r.cache {
			delete(r.cache, key)
			break
		}
	}
}

func (r *PlexResourceResolver) fetchResources(ctx context.Context, accountToken string) ([]PlexResource, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, r.resourcesURL, nil)
	if err != nil {
		return nil, errors.New("could not create Plex resource request")
	}
	query := request.URL.Query()
	query.Set("includeHttps", "1")
	query.Set("includeRelay", "1")
	request.URL.RawQuery = query.Encode()
	request.Header.Set("X-Plex-Token", accountToken)
	request.Header.Set("X-Plex-Client-Identifier", plexResourceClientID)
	request.Header.Set("X-Plex-Product", plexResourceProduct)
	request.Header.Set("X-Plex-Version", plexResourceVersion)
	request.Header.Set("X-Plex-Platform", plexResourceProduct)
	request.Header.Set("Accept", "application/json")
	response, err := r.client.Do(request)
	if err != nil {
		return nil, errors.New("could not discover Plex resources")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("Plex resources returned status %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxPlexResourceBytes+1))
	if err != nil {
		return nil, errors.New("could not read Plex resources")
	}
	if len(body) > maxPlexResourceBytes {
		return nil, errors.New("Plex resources response is too large")
	}
	var resources []PlexResource
	if err := json.Unmarshal(body, &resources); err != nil {
		return nil, errors.New("could not decode Plex resources")
	}
	return resources, nil
}

func selectPlexResource(resources []PlexResource, machineIdentifier string) (PlexResource, error) {
	for _, resource := range resources {
		if resource.ClientIdentifier == machineIdentifier &&
			strings.EqualFold(strings.TrimSpace(resource.Provides), "server") &&
			strings.TrimSpace(resource.AccessToken) != "" {
			return resource, nil
		}
	}
	return PlexResource{}, errors.New("Plex server resource is unavailable")
}

func selectPlexOrigin(resource PlexResource, configured *url.URL) (*url.URL, error) {
	candidates, err := selectPlexOrigins(resource, configured, nil)
	if err != nil {
		return nil, err
	}
	return candidates[0], nil
}

func selectPlexOrigins(resource PlexResource, configured *url.URL, trusted map[string]struct{}) ([]*url.URL, error) {
	if configured == nil {
		return nil, errors.New("configured Plex origin is invalid")
	}
	type candidate struct {
		origin *url.URL
		rank   int
	}
	var candidates []candidate
	for _, connection := range resource.Connections {
		origin, err := validatePlexOrigin(connection.URI)
		if err != nil {
			continue
		}
		if connection.Protocol != "" && !strings.EqualFold(connection.Protocol, origin.Scheme) {
			continue
		}
		if resource.HTTPSRequired && origin.Scheme != "https" {
			continue
		}

		exactConfigured := samePlexOrigin(origin, configured)
		if exactConfigured {
			candidates = append(candidates, candidate{origin: origin, rank: 0})
			continue
		}
		// A connection marked local is not trusted merely because Plex.tv
		// advertised it. Only the operator-configured origin may be local.
		if origin.Scheme != "https" {
			continue
		}
		if _, ok := trusted[origin.String()]; !ok {
			continue
		}
		rank := 3
		if origin.Scheme == "https" && !connection.Relay {
			rank = 1
		} else if origin.Scheme == "https" {
			rank = 2
		} else if !connection.Relay {
			rank = 3
		} else {
			rank = 4
		}
		candidates = append(candidates, candidate{origin: origin, rank: rank})
	}
	ordered := make([]*url.URL, 0, len(candidates)+1)
	if !resource.HTTPSRequired || configured.Scheme == "https" {
		ordered = append(ordered, configured)
	}
	for rank := 1; rank <= 4; rank++ {
		for _, option := range candidates {
			if option.rank == rank {
				ordered = append(ordered, option.origin)
			}
		}
	}
	if len(ordered) == 0 {
		return nil, errors.New("Plex server has no safe connection")
	}
	return ordered, nil
}

func validatePlexOrigin(raw string) (*url.URL, error) {
	if strings.TrimSpace(raw) == "" || strings.ContainsAny(raw, "\r\n") {
		return nil, errors.New("Plex origin is invalid")
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.IsAbs() == false || parsed.Opaque != "" || parsed.User != nil || parsed.Host == "" {
		return nil, errors.New("Plex origin is invalid")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, errors.New("Plex origin is invalid")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Path != "" && parsed.Path != "/" {
		return nil, errors.New("Plex origin is invalid")
	}
	parsed.Path = ""
	parsed.RawPath = ""
	return canonicalPlexOrigin(parsed), nil
}

func samePlexOrigin(left, right *url.URL) bool {
	if left == nil || right == nil {
		return false
	}
	return canonicalPlexOrigin(left).String() == canonicalPlexOrigin(right).String()
}

func canonicalPlexOrigin(origin *url.URL) *url.URL {
	copy := *origin
	hostname := strings.ToLower(copy.Hostname())
	port := copy.Port()
	if hostname != "" {
		host := hostname
		if strings.Contains(hostname, ":") {
			host = "[" + hostname + "]"
		}
		if !((copy.Scheme == "http" && port == "80") || (copy.Scheme == "https" && port == "443")) && port != "" {
			host += ":" + port
		}
		copy.Host = host
	}
	copy.Scheme = strings.ToLower(copy.Scheme)
	return &copy
}

func safeAdvertisedPlexHost(host string) bool {
	host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	if host == "" || host == "localhost" || strings.HasSuffix(host, ".local") {
		return false
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return true
	}
	return !ip.IsLoopback() && !ip.IsPrivate() && !ip.IsLinkLocalUnicast() && !ip.IsUnspecified() && !ip.IsMulticast()
}

func (r *PlexResourceResolver) probeIdentity(ctx context.Context, access *PlexAccess) error {
	request, err := newPlexRequest(ctx, access, http.MethodGet, "/identity")
	if err != nil {
		return errors.New("could not create Plex identity request")
	}
	request.Header.Set("Accept", "application/json, application/xml")
	response, err := r.client.Do(request)
	if err != nil {
		return fmt.Errorf("%w: could not verify Plex server identity", errPlexAvailability)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
			return fmt.Errorf("%w: Plex identity returned status %d", errPlexAccessDenied, response.StatusCode)
		}
		if response.StatusCode >= 500 {
			return fmt.Errorf("%w: Plex identity returned status %d", errPlexAvailability, response.StatusCode)
		}
		return fmt.Errorf("%w: Plex identity returned status %d", errPlexCandidateRejected, response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxPlexIdentityBytes+1))
	if err != nil || len(body) > maxPlexIdentityBytes {
		return fmt.Errorf("%w: could not read Plex server identity", errPlexAvailability)
	}
	var identity string
	if len(bytesTrimSpace(body)) > 0 && bytesTrimSpace(body)[0] == '{' {
		var payload struct {
			MediaContainer struct {
				MachineIdentifier string `json:"machineIdentifier"`
			} `json:"MediaContainer"`
		}
		if err := json.Unmarshal(body, &payload); err == nil {
			identity = payload.MediaContainer.MachineIdentifier
		}
	} else {
		var payload struct {
			MachineIdentifier string `xml:"machineIdentifier,attr"`
		}
		if err := xml.Unmarshal(body, &payload); err == nil {
			identity = payload.MachineIdentifier
		}
	}
	if identity != access.machineIdentifier {
		return fmt.Errorf("%w: Plex server identity does not match configured server", errPlexIdentityMismatch)
	}
	return nil
}

// DoPlex executes one caller-bound PMS request. It owns the transport policy
// and performs at most one cache invalidation/refresh retry for idempotent
// methods after an authentication rejection.
func (r *PlexResourceResolver) DoPlex(ctx context.Context, access *PlexAccess, method, rootRelativePath string) (*http.Response, error) {
	if r == nil || access == nil {
		return nil, errors.New("Plex executor is unavailable")
	}
	response, err := r.doPlexOnce(ctx, access, method, rootRelativePath)
	if err != nil || !isPlexAuthFailure(response) || !isPlexIdempotentMethod(method) {
		return response, err
	}
	if response != nil && response.Body != nil {
		response.Body.Close()
	}
	if strings.TrimSpace(access.accountToken) == "" {
		return nil, errPlexAccessDenied
	}
	r.Invalidate(access.callerUUID, access.machineIdentifier)
	refreshed, refreshErr := r.Resolve(ctx, access.callerUUID, access.accountToken, access.machineIdentifier)
	if refreshErr != nil {
		return nil, refreshErr
	}
	return r.doPlexOnce(ctx, refreshed, method, rootRelativePath)
}

// DoPlexRequest executes a request already assembled with newPlexRequest,
// preserving endpoint-specific headers while retaining resolver retry policy.
func (r *PlexResourceResolver) DoPlexRequest(ctx context.Context, access *PlexAccess, request *http.Request) (*http.Response, error) {
	if request == nil {
		return nil, errors.New("Plex request is unavailable")
	}
	safeRequest, err := requestWithAccess(request, access)
	if err != nil {
		return nil, err
	}
	return r.doPlexRequestOnceWithClient(ctx, access, safeRequest, r.client)
}

// DoPlexMediaRequest uses the long-lived media transport. It retains the same
// caller binding and one auth-refresh retry as metadata requests, but leaves
// duration to the operation/capability context rather than imposing a total
// http.Client timeout.
func (r *PlexResourceResolver) DoPlexMediaRequest(ctx context.Context, access *PlexAccess, request *http.Request) (*http.Response, error) {
	if request == nil {
		return nil, errors.New("Plex media request is unavailable")
	}
	safeRequest, err := requestWithAccess(request, access)
	if err != nil {
		return nil, err
	}
	return r.doPlexRequestOnceWithClient(ctx, access, safeRequest, r.mediaClient)
}

func (r *PlexResourceResolver) doPlexRequestOnce(ctx context.Context, access *PlexAccess, request *http.Request) (*http.Response, error) {
	return r.doPlexRequestOnceWithClient(ctx, access, request, r.client)
}

func (r *PlexResourceResolver) doPlexRequestOnceWithClient(ctx context.Context, access *PlexAccess, request *http.Request, client *http.Client) (*http.Response, error) {
	if r == nil || access == nil {
		return nil, errors.New("Plex executor is unavailable")
	}
	if client == nil {
		return nil, errors.New("Plex transport is unavailable")
	}
	request = request.Clone(ctx)
	response, err := client.Do(request)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, fmt.Errorf("%w: PMS request failed", errPlexAvailability)
	}
	if !isPlexAuthFailure(response) || !isPlexIdempotentMethod(request.Method) {
		return response, nil
	}
	response.Body.Close()
	if strings.TrimSpace(access.accountToken) == "" {
		return nil, errPlexAccessDenied
	}
	r.Invalidate(access.callerUUID, access.machineIdentifier)
	refreshed, refreshErr := r.Resolve(ctx, access.callerUUID, access.accountToken, access.machineIdentifier)
	if refreshErr != nil {
		return nil, refreshErr
	}
	refreshedRequest, requestErr := requestWithAccess(request, refreshed)
	if requestErr != nil {
		return nil, requestErr
	}
	response, err = client.Do(refreshedRequest)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, fmt.Errorf("%w: PMS request failed", errPlexAvailability)
	}
	return response, nil
}

func requestWithAccess(original *http.Request, access *PlexAccess) (*http.Request, error) {
	if original == nil || original.URL == nil {
		return nil, errors.New("Plex request is unavailable")
	}
	request, err := newPlexRequest(original.Context(), access, original.Method, original.URL.RequestURI())
	if err != nil {
		return nil, err
	}
	for key, values := range original.Header {
		if key == "X-Plex-Token" {
			continue
		}
		request.Header[key] = append([]string(nil), values...)
	}
	return request, nil
}

func (r *PlexResourceResolver) doPlexOnce(ctx context.Context, access *PlexAccess, method, rootRelativePath string) (*http.Response, error) {
	request, err := newPlexRequest(ctx, access, method, rootRelativePath)
	if err != nil {
		return nil, err
	}
	response, err := r.client.Do(request)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, fmt.Errorf("%w: PMS request failed", errPlexAvailability)
	}
	return response, nil
}

func isPlexAuthFailure(response *http.Response) bool {
	return response != nil && (response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden)
}

func isPlexIdempotentMethod(method string) bool {
	switch strings.ToUpper(method) {
	case http.MethodGet, http.MethodHead, http.MethodPut, http.MethodDelete, http.MethodOptions, http.MethodTrace:
		return true
	default:
		return false
	}
}

// bytesTrimSpace avoids exposing another mutable body representation to the
// identity decoder while keeping the actual request body bounded.
func bytesTrimSpace(body []byte) []byte { return []byte(strings.TrimSpace(string(body))) }

func (r *PlexResourceResolver) Invalidate(callerUUID, machineIdentifier string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	scope := plexAccessScope{callerUUID: strings.TrimSpace(callerUUID), machineIdentifier: strings.TrimSpace(machineIdentifier)}
	r.epochs[scope]++
	for key := range r.cache {
		if key.callerUUID == strings.TrimSpace(callerUUID) && key.machineIdentifier == strings.TrimSpace(machineIdentifier) {
			delete(r.cache, key)
		}
	}
	r.mu.Unlock()
}

// newPlexRequest is the central caller-scoped PMS request constructor. Its
// path argument must be root-relative; the access token is always a header.
func newPlexRequest(ctx context.Context, access *PlexAccess, method, rootRelativePath string) (*http.Request, error) {
	if access == nil || strings.TrimSpace(access.secretToken) == "" || strings.TrimSpace(access.callerUUID) == "" || strings.TrimSpace(access.machineIdentifier) == "" {
		return nil, errors.New("Plex access is invalid")
	}
	base, err := validatePlexOrigin(access.baseOrigin)
	if err != nil {
		return nil, errors.New("Plex access origin is invalid")
	}
	if rootRelativePath == "" || strings.ContainsAny(rootRelativePath, "\r\n\\") || !strings.HasPrefix(rootRelativePath, "/") || strings.HasPrefix(rootRelativePath, "//") {
		return nil, errors.New("Plex request path must be root-relative")
	}
	parsed, err := url.Parse(rootRelativePath)
	if err != nil || parsed.IsAbs() || parsed.Host != "" || parsed.User != nil || parsed.Opaque != "" || parsed.Fragment != "" || strings.Contains(parsed.Path, "..") {
		return nil, errors.New("Plex request path is unsafe")
	}
	if _, err := url.ParseQuery(parsed.RawQuery); err != nil {
		return nil, errors.New("Plex request query is unsafe")
	}
	for key := range parsed.Query() {
		if strings.EqualFold(key, "X-Plex-Token") {
			return nil, errors.New("Plex token is not allowed in request query")
		}
	}
	for _, values := range parsed.Query() {
		for _, value := range values {
			if strings.Contains(value, access.secretToken) {
				return nil, errors.New("Plex token is not allowed in request query")
			}
		}
	}
	if plexSecretInEncodedText(access.secretToken, rootRelativePath) ||
		plexSecretInEncodedText(access.accountToken, rootRelativePath) ||
		plexSecretInEncodedText(access.secretToken, parsed.Path) ||
		plexSecretInEncodedText(access.accountToken, parsed.Path) ||
		plexSecretInEncodedText(access.secretToken, parsed.RawPath) ||
		plexSecretInEncodedText(access.accountToken, parsed.RawPath) ||
		plexSecretInEncodedText(access.secretToken, parsed.RawQuery) ||
		plexSecretInEncodedText(access.accountToken, parsed.RawQuery) {
		return nil, errors.New("Plex token is not allowed in request path")
	}

	requestURL := base.ResolveReference(parsed)
	request, err := http.NewRequestWithContext(ctx, method, requestURL.String(), nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("X-Plex-Token", access.secretToken)
	return request, nil
}

func plexSecretInEncodedText(secret, text string) bool {
	if secret == "" || text == "" {
		return false
	}
	for i := 0; i < 4; i++ {
		if strings.Contains(text, secret) {
			return true
		}
		decoded, err := url.PathUnescape(text)
		if err != nil || decoded == text {
			decoded, err = url.QueryUnescape(text)
		}
		if err != nil || decoded == text {
			return false
		}
		text = decoded
	}
	return strings.Contains(text, secret)
}

func (r *PlexResourceResolver) InvalidateCaller(callerUUID string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	callerUUID = strings.TrimSpace(callerUUID)
	for key := range r.cache {
		if key.callerUUID == callerUUID {
			delete(r.cache, key)
		}
	}
	for scope := range r.epochs {
		if scope.callerUUID == callerUUID {
			r.epochs[scope]++
		}
	}
}
