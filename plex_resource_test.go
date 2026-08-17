package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestSelectPlexResourceRequiresExactServerAndToken(t *testing.T) {
	resources := []PlexResource{
		{ClientIdentifier: "other", Provides: "server", AccessToken: "wrong"},
		{ClientIdentifier: "machine", Provides: "player", AccessToken: "wrong"},
		{ClientIdentifier: "machine", Provides: "server", AccessToken: ""},
		{ClientIdentifier: "machine", Provides: "server", AccessToken: "server-token"},
	}
	resource, err := selectPlexResource(resources, "machine")
	if err != nil {
		t.Fatal(err)
	}
	if resource.AccessToken != "server-token" {
		t.Fatalf("selected token = %q", resource.AccessToken)
	}
	if _, err := selectPlexResource(resources[:3], "machine"); err == nil {
		t.Fatal("expected missing exact server resource to be rejected")
	}
}

func TestSelectPlexOriginPrefersConfiguredThenRemoteHTTPSDirectRelay(t *testing.T) {
	configured, _ := url.Parse("http://plex.example:32400")
	resource := PlexResource{Connections: []PlexConnection{
		{URI: "http://192.168.1.20:32400", Protocol: "http", Local: true},
		{URI: "https://relay.example:443", Protocol: "https", Relay: true},
		{URI: "https://direct.example:32400", Protocol: "https"},
	}}
	selected, err := selectPlexOrigin(resource, configured)
	if err != nil || selected.String() != configured.String() {
		t.Fatalf("selected origin = %v, err %v", selected, err)
	}

	resource.Connections = append(resource.Connections, PlexConnection{URI: configured.String(), Protocol: "http", Local: true})
	selected, err = selectPlexOrigin(resource, configured)
	if err != nil || selected.String() != configured.String() {
		t.Fatalf("configured origin = %v, err %v", selected, err)
	}

	resource.HTTPSRequired = true
	resource.Connections = []PlexConnection{{URI: configured.String(), Protocol: "http"}}
	if _, err := selectPlexOrigin(resource, configured); err == nil {
		t.Fatal("expected httpsRequired to reject configured HTTP origin")
	}

	trusted := map[string]struct{}{"https://direct.example:32400": {}}
	resource.HTTPSRequired = false
	resource.Connections = []PlexConnection{
		{URI: "http://untrusted.example:32400", Protocol: "http"},
		{URI: "https://direct.example:32400", Protocol: "https"},
	}
	ordered, err := selectPlexOrigins(resource, configured, trusted)
	if err != nil || len(ordered) != 2 || ordered[1].String() != "https://direct.example:32400" {
		t.Fatalf("trusted candidates = %v, err %v", ordered, err)
	}
}

func TestValidatePlexOriginRejectsUnsafeOrigins(t *testing.T) {
	for _, raw := range []string{
		"",
		"plex.example/library",
		"http://user:password@plex.example",
		"http://plex.example/?token=secret",
		"ftp://plex.example",
		"//plex.example",
		"http://plex.example\r\nX-Plex-Token: secret",
	} {
		if _, err := validatePlexOrigin(raw); err == nil {
			t.Errorf("validatePlexOrigin(%q) unexpectedly succeeded", raw)
		}
	}
	for _, raw := range []string{"http://plex.example", "https://plex.example:32400/"} {
		if _, err := validatePlexOrigin(raw); err != nil {
			t.Errorf("validatePlexOrigin(%q) failed: %v", raw, err)
		}
	}
}

func TestPlexResourceResolverCachesAndInvalidatesVerifiedAccess(t *testing.T) {
	var resourceRequests, identityRequests atomic.Int32
	const accountToken = "account-secret"
	const serverToken = "server-secret"
	const machineID = "machine-id"

	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/resources":
			resourceRequests.Add(1)
			if r.URL.Query().Get("includeHttps") != "1" || r.URL.Query().Get("includeRelay") != "1" {
				t.Errorf("resource query = %q", r.URL.RawQuery)
			}
			if resourceRequests.Load() <= 2 && r.Header.Get("X-Plex-Token") != accountToken {
				t.Errorf("resource token header = %q", r.Header.Get("X-Plex-Token"))
			}
			if r.Header.Get("X-Plex-Client-Identifier") != plexResourceClientID ||
				r.Header.Get("X-Plex-Product") != plexResourceProduct ||
				r.Header.Get("X-Plex-Version") != plexResourceVersion {
				t.Errorf("resource headers missing stable identity: %v", r.Header)
			}
			_ = json.NewEncoder(w).Encode([]PlexResource{{
				ClientIdentifier: machineID,
				Provides:         "server",
				AccessToken:      serverToken,
				Connections:      []PlexConnection{{URI: server.URL, Protocol: "http"}},
			}})
		case "/identity":
			identityRequests.Add(1)
			if r.Header.Get("X-Plex-Token") != serverToken {
				t.Errorf("identity token header = %q", r.Header.Get("X-Plex-Token"))
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"MediaContainer":{"machineIdentifier":"machine-id"}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	resolver, err := NewPlexResourceResolver(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	resolver.resourcesURL = server.URL + "/resources"
	resolver.ttl = time.Minute
	ctx := context.Background()
	first, err := resolver.Resolve(ctx, "caller-id", accountToken, machineID)
	if err != nil {
		t.Fatal(err)
	}
	second, err := resolver.Resolve(ctx, "caller-id", accountToken, machineID)
	if err != nil {
		t.Fatal(err)
	}
	if first != second || first.BaseOrigin() != server.URL || first.CallerUUID() != "caller-id" {
		t.Fatal("resolver did not return the cached immutable access")
	}
	if resourceRequests.Load() != 1 || identityRequests.Load() != 1 {
		t.Fatalf("requests before invalidation = resources %d identity %d", resourceRequests.Load(), identityRequests.Load())
	}
	resolver.Invalidate("caller-id", machineID)
	if _, err := resolver.Resolve(ctx, "caller-id", accountToken, machineID); err != nil {
		t.Fatal(err)
	}
	if resourceRequests.Load() != 2 || identityRequests.Load() != 2 {
		t.Fatalf("requests after invalidation = resources %d identity %d", resourceRequests.Load(), identityRequests.Load())
	}
	if _, err := resolver.Resolve(ctx, "caller-id", "different-account-token", machineID); err != nil {
		t.Fatal(err)
	}
	if resourceRequests.Load() != 3 || identityRequests.Load() != 3 {
		t.Fatal("different account token incorrectly shared cached access")
	}
}

func TestNewPlexRequestKeepsTokenOutOfURL(t *testing.T) {
	access := &PlexAccess{
		baseOrigin:        "https://plex.example:32400",
		secretToken:       "very-secret-token",
		accountToken:      "account-secret",
		callerUUID:        "caller",
		machineIdentifier: "machine",
	}
	request, err := newPlexRequest(context.Background(), access, http.MethodGet, "/library/metadata/1?includeGuids=1")
	if err != nil {
		t.Fatal(err)
	}
	if request.URL.Query().Get("X-Plex-Token") != "" || strings.Contains(request.URL.String(), access.secretToken) {
		t.Fatalf("token leaked into URL %q", request.URL.String())
	}
	if request.Header.Get("X-Plex-Token") != access.secretToken {
		t.Fatal("token was not placed in request header")
	}
	if strings.Contains(access.String(), access.secretToken) {
		t.Fatal("access formatter leaked secret")
	}
	for _, format := range []string{"%v", "%+v", "%#v"} {
		for _, value := range []any{*access, access} {
			formatted := fmt.Sprintf(format, value)
			if strings.Contains(formatted, access.secretToken) || strings.Contains(formatted, access.accountToken) || strings.Contains(formatted, access.baseOrigin) {
				t.Fatalf("format %s leaked access: %q", format, formatted)
			}
		}
	}
	encodedJSON, err := json.Marshal(access)
	if err != nil || strings.Contains(string(encodedJSON), access.secretToken) || strings.Contains(string(encodedJSON), access.baseOrigin) {
		t.Fatalf("access JSON leaked credentials: %s", encodedJSON)
	}
	for _, path := range []string{
		"https://other.example/identity", "//other.example/identity", "identity", "/../identity",
		"/identity?X-Plex-Token=secret", "/identity?token=very-secret-token",
		"/identity?x=very%2Dsecret%2Dtoken", "/identity/very%2Dsecret%2Dtoken",
		"/identity?x=very%252Dsecret%252Dtoken", "/identity/very%252Dsecret%252Dtoken",
		"/identity/account-secret", "/identity?x=account-secret", "/identity?x=account%2Dsecret", "/identity?x=account%252Dsecret",
	} {
		if _, err := newPlexRequest(context.Background(), access, http.MethodGet, path); err == nil {
			t.Errorf("unsafe path %q unexpectedly succeeded", path)
		}
	}
}

func TestDiscoverTrustedOriginsUsesExactMachineAndCanonicalHTTPSIntersection(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Plex-Token") != "admin-token" {
			t.Fatal("admin token was not used for discovery")
		}
		_ = json.NewEncoder(w).Encode([]PlexResource{
			{ClientIdentifier: "other", Provides: "server", AccessToken: "x", Connections: []PlexConnection{{URI: "https://other.example", Protocol: "https"}}},
			{ClientIdentifier: "machine", Provides: "server", AccessToken: "x", Connections: []PlexConnection{
				{URI: "https://trusted.example:443/", Protocol: "https"},
				{URI: "http://unsafe.example", Protocol: "http"},
				{URI: "https://127.0.0.1", Protocol: "https"},
			}},
		})
	}))
	defer server.Close()
	resolver, _ := NewPlexResourceResolver("http://configured.example")
	resolver.resourcesURL = server.URL
	if err := resolver.DiscoverTrustedOrigins(context.Background(), "admin-token", "machine"); err != nil {
		t.Fatal(err)
	}
	resolver.mu.Lock()
	trusted := resolver.trustedOrigins["machine"]
	resolver.mu.Unlock()
	if len(trusted) != 1 {
		t.Fatalf("trusted origins = %#v", trusted)
	}
	if _, ok := trusted["https://trusted.example"]; !ok {
		t.Fatalf("canonical trusted origin missing: %#v", trusted)
	}
	if _, ok := trusted["https://other.example"]; ok {
		t.Fatal("other machine origin was trusted")
	}
}

func TestDiscoverTrustedOriginsFailureLeavesConfiguredPathPossible(t *testing.T) {
	resolver, err := NewPlexResourceResolver("http://configured.example")
	if err != nil {
		t.Fatal(err)
	}
	resolver.resourcesURL = "http://127.0.0.1:1/resources"
	if err := resolver.DiscoverTrustedOrigins(context.Background(), "admin-token", "machine"); err == nil {
		t.Fatal("expected discovery failure")
	}
	resource := PlexResource{Connections: []PlexConnection{{URI: "http://configured.example", Protocol: "http"}, {URI: "https://untrusted.example", Protocol: "https"}}}
	ordered, err := selectPlexOrigins(resource, resolver.configuredOrigin, nil)
	if err != nil || len(ordered) != 1 || ordered[0].String() != "http://configured.example" {
		t.Fatalf("configured-only fallback = %v, err %v", ordered, err)
	}
}

func TestPlexResolverFailsOverToNextCandidateAndDoesNotFollowRedirects(t *testing.T) {
	var firstHits, secondHits atomic.Int32
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		firstHits.Add(1)
		http.Error(w, "unavailable", http.StatusBadGateway)
	}))
	defer first.Close()
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secondHits.Add(1)
		if r.URL.Path == "/identity" {
			_, _ = w.Write([]byte(`{"MediaContainer":{"machineIdentifier":"machine"}}`))
			return
		}
		http.Redirect(w, r, first.URL+"/identity", http.StatusFound)
	}))
	defer second.Close()
	resolver, err := NewPlexResourceResolver(second.URL)
	if err != nil {
		t.Fatal(err)
	}
	resolver.client = &http.Client{CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	access := &PlexAccess{baseOrigin: second.URL, secretToken: "server-secret", callerUUID: "caller", machineIdentifier: "machine"}
	if err := resolver.probeIdentity(context.Background(), access); err != nil {
		t.Fatal(err)
	}
	if firstHits.Load() != 0 || secondHits.Load() != 1 {
		t.Fatalf("unexpected probe hits first=%d second=%d", firstHits.Load(), secondHits.Load())
	}
}

type plexRoundTripFunc func(*http.Request) (*http.Response, error)

func (f plexRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestPlexResolverActualFailoverUsesTrustedHTTPSCandidate(t *testing.T) {
	var configuredHits, relayHits atomic.Int32
	resources := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]PlexResource{{
			ClientIdentifier: "machine",
			Provides:         "server",
			AccessToken:      "server-secret",
			Connections: []PlexConnection{
				{URI: "https://configured.test", Protocol: "https"},
				{URI: "https://relay.test", Protocol: "https", Relay: true},
			},
		}})
	}))
	defer resources.Close()
	resolver, err := NewPlexResourceResolver("https://configured.test")
	if err != nil {
		t.Fatal(err)
	}
	resolver.resourcesURL = resources.URL
	if err := resolver.SetTrustedOrigins("machine", []string{"https://relay.test"}); err != nil {
		t.Fatal(err)
	}
	resolver.client.Transport = plexRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		host := r.URL.Host
		if host == "configured.test" {
			configuredHits.Add(1)
			return &http.Response{StatusCode: http.StatusBadGateway, Body: http.NoBody, Header: make(http.Header), Request: r}, nil
		}
		if host == "relay.test" {
			relayHits.Add(1)
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"MediaContainer":{"machineIdentifier":"machine"}}`)), Header: make(http.Header), Request: r}, nil
		}
		return http.DefaultTransport.RoundTrip(r)
	})
	access, err := resolver.Resolve(context.Background(), "caller", "account", "machine")
	if err != nil {
		t.Fatal(err)
	}
	if access.BaseOrigin() != "https://relay.test" || configuredHits.Load() != 1 || relayHits.Load() != 1 {
		t.Fatalf("failover access=%s configured=%d relay=%d", access.BaseOrigin(), configuredHits.Load(), relayHits.Load())
	}
}

func TestPlexExecutorRefreshesOnceAfter401AndInvalidates(t *testing.T) {
	var dataHits atomic.Int32
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/resources":
			_ = json.NewEncoder(w).Encode([]PlexResource{{ClientIdentifier: "machine", Provides: "server", AccessToken: "server-secret", Connections: []PlexConnection{{URI: server.URL, Protocol: "http"}}}})
		case "/identity":
			_, _ = w.Write([]byte(`{"MediaContainer":{"machineIdentifier":"machine"}}`))
		case "/library":
			if dataHits.Add(1) == 1 {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	resolver, err := NewPlexResourceResolver(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	resolver.resourcesURL = server.URL + "/resources"
	access := &PlexAccess{baseOrigin: server.URL, secretToken: "server-secret", accountToken: "account", callerUUID: "caller", machineIdentifier: "machine"}
	response, err := resolver.DoPlex(context.Background(), access, http.MethodGet, "/library")
	if err != nil || response.StatusCode != http.StatusOK || dataHits.Load() != 2 {
		t.Fatalf("executor response=%v err=%v hits=%d", response, err, dataHits.Load())
	}
	response.Body.Close()
}

func TestPlexResolverInvalidationDoesNotPublishStaleFlight(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var requests atomic.Int32
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/resources" {
			if requests.Add(1) == 1 {
				close(started)
				<-release
			}
			_ = json.NewEncoder(w).Encode([]PlexResource{{ClientIdentifier: "machine", Provides: "server", AccessToken: "secret", Connections: []PlexConnection{{URI: server.URL, Protocol: "http"}}}})
			return
		}
		_, _ = w.Write([]byte(`{"MediaContainer":{"machineIdentifier":"machine"}}`))
	}))
	defer server.Close()
	resolver, _ := NewPlexResourceResolver(server.URL)
	resolver.resourcesURL = server.URL + "/resources"
	result := make(chan *PlexAccess, 1)
	go func() {
		access, _ := resolver.Resolve(context.Background(), "caller", "account", "machine")
		result <- access
	}()
	<-started
	resolver.Invalidate("caller", "machine")
	close(release)
	select {
	case access := <-result:
		if access == nil {
			t.Fatal("stale flight failed to re-resolve")
		}
	case <-time.After(time.Second):
		t.Fatal("stale flight did not complete")
	}
	if requests.Load() < 2 {
		t.Fatalf("resource requests = %d, want refresh after invalidation", requests.Load())
	}
}

func TestPlexResolverCacheCapacityIsBounded(t *testing.T) {
	resolver, err := NewPlexResourceResolver("https://configured.test")
	if err != nil {
		t.Fatal(err)
	}
	resolver.mu.Lock()
	for i := 0; i < maxPlexAccessEntries+10; i++ {
		key := plexAccessCacheKey{callerUUID: string(rune(i + 1))}
		resolver.cache[key] = plexAccessCacheEntry{expires: time.Now().Add(time.Minute)}
	}
	resolver.trimCacheLocked()
	size := len(resolver.cache)
	resolver.mu.Unlock()
	if size != maxPlexAccessEntries {
		t.Fatalf("cache size = %d, want %d", size, maxPlexAccessEntries)
	}
}
