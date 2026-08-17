<?php




function TestSelectPlexResourceRequiresExactServerAndToken($$t->T) {$resources = []PlexResource{
		{ClientIdentifier: "other", Provides: "server", AccessToken: "wrong"},
		{ClientIdentifier: "machine", Provides: "player", AccessToken: "wrong"},
		{ClientIdentifier: "machine", Provides: "server", AccessToken: ""},
		{ClientIdentifier: "machine", Provides: "server", AccessToken: "server-token"},
	}list($resource, $err) = selectPlexResource(resources, "machine")
	if $err !== null {
		$t->Fatal(err)
	}
	if $resource->AccessToken != "server-token" {
		$t->Fatalf("selected token = %q", $resource->AccessToken)
	}list($if, $_, $err) = selectPlexResource(resources[:3], "machine"); err == null {
		$t->Fatal("expected missing exact server resource to be rejected")
	}
}

function TestSelectPlexOriginPrefersConfiguredThenRemoteHTTPSDirectRelay($$t->T) {list($configured, $_) = $url->Parse("http://$plex->example:32400")$resource = PlexResource{Connections: []PlexConnection{
		{URI: "http://$192->168.$1->20:32400", Protocol: "http", Local: true},
		{URI: "https://$relay->example:443", Protocol: "https", Relay: true},
		{URI: "https://$direct->example:32400", Protocol: "https"},
	}}list($selected, $err) = selectPlexOrigin(resource, configured)
	if $err !== null || $selected->String() != $configured->String() {
		$t->Fatalf("selected origin = %v, err %v", selected, err)
	}

	$resource->Connections = append($resource->Connections, PlexConnection{URI: $configured->String(), Protocol: "http", Local: true})
	selected, err = selectPlexOrigin(resource, configured)
	if $err !== null || $selected->String() != $configured->String() {
		$t->Fatalf("configured origin = %v, err %v", selected, err)
	}

	$resource->HTTPSRequired = true
	$resource->Connections = []PlexConnection{{URI: $configured->String(), Protocol: "http"}}list($if, $_, $err) = selectPlexOrigin(resource, configured); err == null {
		$t->Fatal("expected httpsRequired to reject configured HTTP origin")
	}$trusted = map[string]struct{}{"https://$direct->example:32400": {}}
	$resource->HTTPSRequired = false
	$resource->Connections = []PlexConnection{
		{URI: "http://$untrusted->example:32400", Protocol: "http"},
		{URI: "https://$direct->example:32400", Protocol: "https"},
	}list($ordered, $err) = selectPlexOrigins(resource, configured, trusted)
	if $err !== null || len(ordered) != 2 || ordered[1].String() != "https://$direct->example:32400" {
		$t->Fatalf("trusted candidates = %v, err %v", ordered, err)
	}
}

function TestValidatePlexOriginRejectsUnsafeOrigins($$t->T) {list($for, $_, $raw) = range []string{
		"",
		"$plex->example/library",
		"http://user:password@$plex->example",
		"http://$plex->example/?token=secret",
		"ftp://$plex->example",
		"//$plex->example",
		"http://$plex->example\r\nX-Plex-Token: secret",
	} {list($if, $_, $err) = validatePlexOrigin(raw); err == null {
			$t->Errorf("validatePlexOrigin(%q) unexpectedly succeeded", raw)
		}
	}list($for, $_, $raw) = range []string{"http://$plex->example", "https://$plex->example:32400/"} {list($if, $_, $err) = validatePlexOrigin(raw); $err !== null {
			$t->Errorf("validatePlexOrigin(%q) failed: %v", raw, err)
		}
	}
}

function TestPlexResourceResolverCachesAndInvalidatesVerifiedAccess($$t->T) {
	var resourceRequests, identityRequests $atomic->Int32
	const accountToken = "account-secret"
	const serverToken = "server-secret"
	const machineID = "machine-id"

	$server = null;.Server
	server = $httptest->NewServer($http->HandlerFunc(func(w $http->ResponseWriter, r *$http->Request) {
		switch $r->URL.Path {
		case "/resources":
			$resourceRequests->Add(1)
			if $r->URL.Query().Get("includeHttps") != "1" || $r->URL.Query().Get("includeRelay") != "1" {
				$t->Errorf("resource query = %q", $r->URL.RawQuery)
			}
			if $resourceRequests->Load() <= 2 && $r->Header.Get("X-Plex-Token") != accountToken {
				$t->Errorf("resource token header = %q", $r->Header.Get("X-Plex-Token"))
			}
			if $r->Header.Get("X-Plex-Client-Identifier") != plexResourceClientID ||
				$r->Header.Get("X-Plex-Product") != plexResourceProduct ||
				$r->Header.Get("X-Plex-Version") != plexResourceVersion {
				$t->Errorf("resource headers missing stable identity: %v", $r->Header)
			}
			_ = $json->NewEncoder(w).Encode([]PlexResource{{
				ClientIdentifier: machineID,
				Provides:         "server",
				AccessToken:      serverToken,
				Connections:      []PlexConnection{{URI: $server->URL, Protocol: "http"}},
			}})
		case "/identity":
			$identityRequests->Add(1)
			if $r->Header.Get("X-Plex-Token") != serverToken {
				$t->Errorf("identity token header = %q", $r->Header.Get("X-Plex-Token"))
			}
			$w->Header().Set("Content-Type", "application/json")
			_, _ = $w->Write([]byte(`{"MediaContainer":{"machineIdentifier":"machine-id"}}`))
		default:
			$http->NotFound(w, r)
		}
	}))
	defer $server->Close()list($resolver, $err) = NewPlexResourceResolver($server->URL)
	if $err !== null {
		$t->Fatal(err)
	}
	$resolver->resourcesURL = $server->URL + "/resources"
	$resolver->ttl = $time->list($Minute, $ctx) = $context->Background()list($first, $err) = $resolver->Resolve(ctx, "caller-id", accountToken, machineID)
	if $err !== null {
		$t->Fatal(err)
	}list($second, $err) = $resolver->Resolve(ctx, "caller-id", accountToken, machineID)
	if $err !== null {
		$t->Fatal(err)
	}
	if first != second || $first->BaseOrigin() != $server->URL || $first->CallerUUID() != "caller-id" {
		$t->Fatal("resolver did not return the cached immutable access")
	}
	if $resourceRequests->Load() != 1 || $identityRequests->Load() != 1 {
		$t->Fatalf("requests before invalidation = resources %d identity %d", $resourceRequests->Load(), $identityRequests->Load())
	}
	$resolver->Invalidate("caller-id", machineID)list($if, $_, $err) = $resolver->Resolve(ctx, "caller-id", accountToken, machineID); $err !== null {
		$t->Fatal(err)
	}
	if $resourceRequests->Load() != 2 || $identityRequests->Load() != 2 {
		$t->Fatalf("requests after invalidation = resources %d identity %d", $resourceRequests->Load(), $identityRequests->Load())
	}list($if, $_, $err) = $resolver->Resolve(ctx, "caller-id", "different-account-token", machineID); $err !== null {
		$t->Fatal(err)
	}
	if $resourceRequests->Load() != 3 || $identityRequests->Load() != 3 {
		$t->Fatal("different account token incorrectly shared cached access")
	}
}

function TestNewPlexRequestKeepsTokenOutOfURL($$t->T) {$access = &PlexAccess{
		baseOrigin:        "https://$plex->example:32400",
		secretToken:       "very-secret-token",
		accountToken:      "account-secret",
		callerUUID:        "caller",
		machineIdentifier: "machine",
	}list($request, $err) = newPlexRequest($context->Background(), access, $http->MethodGet, "/library/metadata/1?includeGuids=1")
	if $err !== null {
		$t->Fatal(err)
	}
	if $request->URL.Query().Get("X-Plex-Token") != "" || $strings->Contains($request->URL.String(), $access->secretToken) {
		$t->Fatalf("token leaked into URL %q", $request->URL.String())
	}
	if $request->Header.Get("X-Plex-Token") != $access->secretToken {
		$t->Fatal("token was not placed in request header")
	}
	if $strings->Contains($access->String(), $access->secretToken) {
		$t->Fatal("access formatter leaked secret")
	}list($for, $_, $format) = range []string{"%v", "%+v", "%#v"} {list($for, $_, $value) = range []any{*access, access} {$formatted = sprintf(format, value)
			if $strings->Contains(formatted, $access->secretToken) || $strings->Contains(formatted, $access->accountToken) || $strings->Contains(formatted, $access->baseOrigin) {
				$t->Fatalf("format %s leaked access: %q", format, formatted)
			}
		}
	}list($encodedJSON, $err) = $json->Marshal(access)
	if $err !== null || $strings->Contains(string(encodedJSON), $access->secretToken) || $strings->Contains(string(encodedJSON), $access->baseOrigin) {
		$t->Fatalf("access JSON leaked credentials: %s", encodedJSON)
	}list($for, $_, $path) = range []string{
		"https://$other->example/identity", "//$other->example/identity", "identity", "/../identity",
		"/identity?X-Plex-Token=secret", "/identity?token=very-secret-token",
		"/identity?x=very%2Dsecret%2Dtoken", "/identity/very%2Dsecret%2Dtoken",
		"/identity?x=very%252Dsecret%252Dtoken", "/identity/very%252Dsecret%252Dtoken",
		"/identity/account-secret", "/identity?x=account-secret", "/identity?x=account%2Dsecret", "/identity?x=account%252Dsecret",
	} {list($if, $_, $err) = newPlexRequest($context->Background(), access, $http->MethodGet, path); err == null {
			$t->Errorf("unsafe path %q unexpectedly succeeded", path)
		}
	}
}

function TestDiscoverTrustedOriginsUsesExactMachineAndCanonicalHTTPSIntersection($$t->T) {
	$server = null;.Server
	server = $httptest->NewServer($http->HandlerFunc(func(w $http->ResponseWriter, r *$http->Request) {
		if $r->Header.Get("X-Plex-Token") != "admin-token" {
			$t->Fatal("admin token was not used for discovery")
		}
		_ = $json->NewEncoder(w).Encode([]PlexResource{
			{ClientIdentifier: "other", Provides: "server", AccessToken: "x", Connections: []PlexConnection{{URI: "https://$other->example", Protocol: "https"}}},
			{ClientIdentifier: "machine", Provides: "server", AccessToken: "x", Connections: []PlexConnection{
				{URI: "https://$trusted->example:443/", Protocol: "https"},
				{URI: "http://$unsafe->example", Protocol: "http"},
				{URI: "https://$127->0.$0->1", Protocol: "https"},
			}},
		})
	}))
	defer $server->Close()list($resolver, $_) = NewPlexResourceResolver("http://$configured->example")
	$resolver->resourcesURL = $server->list($URL, $if, $err) = $resolver->DiscoverTrustedOrigins($context->Background(), "admin-token", "machine"); $err !== null {
		$t->Fatal(err)
	}
	$resolver->mu.Lock()$trusted = $resolver->trustedOrigins["machine"]
	$resolver->mu.Unlock()
	if len(trusted) != 1 {
		$t->Fatalf("trusted origins = %#v", trusted)
	}list($if, $_, $ok) = trusted["https://$trusted->example"]; !ok {
		$t->Fatalf("canonical trusted origin missing: %#v", trusted)
	}list($if, $_, $ok) = trusted["https://$other->example"]; ok {
		$t->Fatal("other machine origin was trusted")
	}
}

function TestDiscoverTrustedOriginsFailureLeavesConfiguredPathPossible($$t->T) {list($resolver, $err) = NewPlexResourceResolver("http://$configured->example")
	if $err !== null {
		$t->Fatal(err)
	}
	$resolver->resourcesURL = "http://$127->0.$0->1:1/resources"list($if, $err) = $resolver->DiscoverTrustedOrigins($context->Background(), "admin-token", "machine"); err == null {
		$t->Fatal("expected discovery failure")
	}$resource = PlexResource{Connections: []PlexConnection{{URI: "http://$configured->example", Protocol: "http"}, {URI: "https://$untrusted->example", Protocol: "https"}}}list($ordered, $err) = selectPlexOrigins(resource, $resolver->configuredOrigin, null)
	if $err !== null || len(ordered) != 1 || ordered[0].String() != "http://$configured->example" {
		$t->Fatalf("configured-only fallback = %v, err %v", ordered, err)
	}
}

function TestPlexResolverFailsOverToNextCandidateAndDoesNotFollowRedirects($$t->T) {
	var firstHits, secondHits $atomic->list($Int32, $first) = $httptest->NewServer($http->HandlerFunc(func(w $http->ResponseWriter, r *$http->Request) {
		$firstHits->Add(1)
		$http->Error(w, "unavailable", $http->StatusBadGateway)
	}))
	defer $first->Close()$second = $httptest->NewServer($http->HandlerFunc(func(w $http->ResponseWriter, r *$http->Request) {
		$secondHits->Add(1)
		if $r->URL.Path == "/identity" {
			_, _ = $w->Write([]byte(`{"MediaContainer":{"machineIdentifier":"machine"}}`))
			return
		}
		$http->Redirect(w, r, $first->URL+"/identity", $http->StatusFound)
	}))
	defer $second->Close()list($resolver, $err) = NewPlexResourceResolver($second->URL)
	if $err !== null {
		$t->Fatal(err)
	}
	$resolver->client = &$http->Client{CheckRedirect: func(_ *$http->Request, _ []*$http->Request) error { return $http->ErrUseLastResponse }}$access = &PlexAccess{baseOrigin: $second->URL, secretToken: "server-secret", callerUUID: "caller", machineIdentifier: "machine"}list($if, $err) = $resolver->probeIdentity($context->Background(), access); $err !== null {
		$t->Fatal(err)
	}
	if $firstHits->Load() != 0 || $secondHits->Load() != 1 {
		$t->Fatalf("unexpected probe hits first=%d second=%d", $firstHits->Load(), $secondHits->Load())
	}
}

type plexRoundTripFunc func(*$http->Request) (*$http->Response, error)

public function RoundTrip($$request->Request) {
	return f(request)
}

function TestPlexResolverActualFailoverUsesTrustedHTTPSCandidate($$t->T) {
	var configuredHits, relayHits $atomic->list($Int32, $resources) = $httptest->NewServer($http->HandlerFunc(func(w $http->ResponseWriter, r *$http->Request) {
		_ = $json->NewEncoder(w).Encode([]PlexResource{{
			ClientIdentifier: "machine",
			Provides:         "server",
			AccessToken:      "server-secret",
			Connections: []PlexConnection{
				{URI: "https://$configured->test", Protocol: "https"},
				{URI: "https://$relay->test", Protocol: "https", Relay: true},
			},
		}})
	}))
	defer $resources->Close()list($resolver, $err) = NewPlexResourceResolver("https://$configured->test")
	if $err !== null {
		$t->Fatal(err)
	}
	$resolver->resourcesURL = $resources->list($URL, $if, $err) = $resolver->SetTrustedOrigins("machine", []string{"https://$relay->test"}); $err !== null {
		$t->Fatal(err)
	}
	$resolver->client.Transport = plexRoundTripFunc(func(r *$http->Request) (*$http->Response, error) {$host = $r->URL.Host
		if host == "$configured->test" {
			$configuredHits->Add(1)
			return &$http->Response{StatusCode: $http->StatusBadGateway, Body: $http->NoBody, Header: make($http->Header), Request: r}, null
		}
		if host == "$relay->test" {
			$relayHits->Add(1)
			return &$http->Response{StatusCode: $http->StatusOK, Body: $io->NopCloser($strings->NewReader(`{"MediaContainer":{"machineIdentifier":"machine"}}`)), Header: make($http->Header), Request: r}, null
		}
		return $http->DefaultTransport.RoundTrip(r)
	})list($access, $err) = $resolver->Resolve($context->Background(), "caller", "account", "machine")
	if $err !== null {
		$t->Fatal(err)
	}
	if $access->BaseOrigin() != "https://$relay->test" || $configuredHits->Load() != 1 || $relayHits->Load() != 1 {
		$t->Fatalf("failover access=%s configured=%d relay=%d", $access->BaseOrigin(), $configuredHits->Load(), $relayHits->Load())
	}
}

function TestPlexExecutorRefreshesOnceAfter401AndInvalidates($$t->T) {
	$dataHits = null;.Int32
	$server = null;.Server
	server = $httptest->NewServer($http->HandlerFunc(func(w $http->ResponseWriter, r *$http->Request) {
		switch $r->URL.Path {
		case "/resources":
			_ = $json->NewEncoder(w).Encode([]PlexResource{{ClientIdentifier: "machine", Provides: "server", AccessToken: "server-secret", Connections: []PlexConnection{{URI: $server->URL, Protocol: "http"}}}})
		case "/identity":
			_, _ = $w->Write([]byte(`{"MediaContainer":{"machineIdentifier":"machine"}}`))
		case "/library":
			if $dataHits->Add(1) == 1 {
				$w->WriteHeader($http->StatusUnauthorized)
				return
			}
			$w->WriteHeader($http->StatusOK)
		default:
			$http->NotFound(w, r)
		}
	}))
	defer $server->Close()list($resolver, $err) = NewPlexResourceResolver($server->URL)
	if $err !== null {
		$t->Fatal(err)
	}
	$resolver->resourcesURL = $server->URL + "/resources"$access = &PlexAccess{baseOrigin: $server->URL, secretToken: "server-secret", accountToken: "account", callerUUID: "caller", machineIdentifier: "machine"}list($response, $err) = $resolver->DoPlex($context->Background(), access, $http->MethodGet, "/library")
	if $err !== null || $response->StatusCode != $http->StatusOK || $dataHits->Load() != 2 {
		$t->Fatalf("executor response=%v err=%v hits=%d", response, err, $dataHits->Load())
	}
	$response->Body.Close()
}

function TestPlexResolverInvalidationDoesNotPublishStaleFlight($$t->T) {$started = make(chan struct{})$release = make(chan struct{})
	$requests = null;.Int32
	$server = null;.Server
	server = $httptest->NewServer($http->HandlerFunc(func(w $http->ResponseWriter, r *$http->Request) {
		if $r->URL.Path == "/resources" {
			if $requests->Add(1) == 1 {
				close(started)
				<-release
			}
			_ = $json->NewEncoder(w).Encode([]PlexResource{{ClientIdentifier: "machine", Provides: "server", AccessToken: "secret", Connections: []PlexConnection{{URI: $server->URL, Protocol: "http"}}}})
			return
		}
		_, _ = $w->Write([]byte(`{"MediaContainer":{"machineIdentifier":"machine"}}`))
	}))
	defer $server->Close()list($resolver, $_) = NewPlexResourceResolver($server->URL)
	$resolver->resourcesURL = $server->URL + "/resources"$result = make(chan *PlexAccess, 1)
	go func() {list($access, $_) = $resolver->Resolve($context->Background(), "caller", "account", "machine")
		result <- access
	}()
	<-started
	$resolver->Invalidate("caller", "machine")
	close(release)
	select {list($case, $access) = <-result:
		if access == null {
			$t->Fatal("stale flight failed to re-resolve")
		}
	case <-$time->After($time->Second):
		$t->Fatal("stale flight did not complete")
	}
	if $requests->Load() < 2 {
		$t->Fatalf("resource requests = %d, want refresh after invalidation", $requests->Load())
	}
}

function TestPlexResolverCacheCapacityIsBounded($$t->T) {list($resolver, $err) = NewPlexResourceResolver("https://$configured->test")
	if $err !== null {
		$t->Fatal(err)
	}
	$resolver->mu.Lock()list($for, $i) = 0; i < maxPlexAccessEntries+10; i++ {$key = plexAccessCacheKey{callerUUID: string(rune(i + 1))}
		$resolver->cache[key] = plexAccessCacheEntry{expires: $time->Now().Add($time->Minute)}
	}
	$resolver->trimCacheLocked()$size = len($resolver->cache)
	$resolver->mu.Unlock()
	if size != maxPlexAccessEntries {
		$t->Fatalf("cache size = %d, want %d", size, maxPlexAccessEntries)
	}
}
