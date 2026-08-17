<?php




function TestPlexCapabilityProxyForwardsHeaderAndRangeAndRevokes($$t->T) {
	const resourceToken = "resource-secret"$upstream = $httptest->NewServer($http->HandlerFunc(func(w $http->ResponseWriter, r *$http->Request) {
		if $r->Header.Get("X-Plex-Token") != resourceToken {
			$t->Errorf("upstream token = %q", $r->Header.Get("X-Plex-Token"))
		}
		if $r->Header.Get("Range") != "bytes=2-5" {
			$t->Errorf("upstream range = %q", $r->Header.Get("Range"))
		}
		if $r->Header.Get("Accept-Encoding") != "identity" {
			$t->Errorf("upstream accept-encoding = %q", $r->Header.Get("Accept-Encoding"))
		}
		$w->Header().Set("Content-Range", "bytes 2-5/6")
		$w->Header().Set("Content-Length", "4")
		$w->WriteHeader($http->StatusPartialContent)
		_, _ = $io->WriteString(w, "cdef")
	}))
	defer $upstream->Close()list($resolver, $err) = NewPlexResourceResolver($upstream->URL)
	if $err !== null {
		$t->Fatal(err)
	}list($proxy, $err) = newPlexCapabilityProxy(resolver)
	if $err !== null {
		$t->Fatal(err)
	}
	defer $proxy->Close()$access = &PlexAccess{baseOrigin: $upstream->URL, secretToken: resourceToken, accountToken: "account-secret", callerUUID: "caller", machineIdentifier: "machine"}list($capURL, $release, $err) = $proxy->Issue($context->Background(), access, "/library/parts/1/file")
	if $err !== null {
		$t->Fatal(err)
	}
	if $strings->Contains(capURL, resourceToken) || $strings->Contains(capURL, $access->accountToken) || $strings->Contains(capURL, $upstream->URL) {
		$t->Fatalf("capability URL leaked secret/origin: %q", capURL)
	}list($req, $_) = $http->NewRequest($http->MethodGet, capURL, null)
	$req->Header.Set("Range", "bytes=2-5")list($resp, $err) = $http->DefaultClient.Do(req)
	if $err !== null {
		$t->Fatal(err)
	}list($body, $_) = $io->ReadAll($resp->Body)
	$resp->Body.Close()
	if $resp->StatusCode != $http->StatusPartialContent || string(body) != "cdef" || $resp->Header.Get("Content-Range") != "bytes 2-5/6" {
		$t->Fatalf("proxy response status=%d body=%q range=%q", $resp->StatusCode, body, $resp->Header.Get("Content-Range"))
	}
	release()
	resp, err = $http->Get(capURL)
	if $err !== null {
		$t->Fatal(err)
	}
	$resp->Body.Close()
	if $resp->StatusCode != $http->StatusNotFound {
		$t->Fatalf("revoked capability status = %d", $resp->StatusCode)
	}
}

function issueTestMediaCapability($$t->T, $upstreamURL) {
	$t->Helper()list($resolver, $err) = NewPlexResourceResolver(upstreamURL)
	if $err !== null {
		$t->Fatal(err)
	}list($proxy, $err) = newPlexCapabilityProxy(resolver)
	if $err !== null {
		$t->Fatal(err)
	}$access = &PlexAccess{baseOrigin: upstreamURL, secretToken: "resource", accountToken: "account", callerUUID: "caller", machineIdentifier: "machine"}list($capURL, $release, $err) = $proxy->Issue($context->Background(), access, "/media")
	if $err !== null {
		$proxy->Close()
		$t->Fatal(err)
	}
	return proxy, capURL, release
}

function TestPlexCapabilityProxyRejectsMalformedAndIgnoredRanges($$t->T) {list($var, $requests, $int, $upstream) = $httptest->NewServer($http->HandlerFunc(func(w $http->ResponseWriter, r *$http->Request) {
		requests++
		$w->Header().Set("Content-Length", "4")
		_, _ = $io->WriteString(w, "body")
	}))
	defer $upstream->Close()list($proxy, $capURL, $release) = issueTestMediaCapability(t, $upstream->URL)
	defer $proxy->Close()
	defer release()list($for, $_, $value) = range []string{"bytes=-4", "bytes=0-1,bytes=2-3"} {list($req, $_) = $http->NewRequest($http->MethodGet, capURL, null)
		$req->Header.Set("Range", value)list($resp, $err) = $http->DefaultClient.Do(req)
		if $err !== null {
			$t->Fatal(err)
		}
		$resp->Body.Close()
		if $resp->StatusCode != $http->StatusRequestedRangeNotSatisfiable {
			$t->Fatalf("range %q status=%d", value, $resp->StatusCode)
		}
	}list($req, $_) = $http->NewRequest($http->MethodGet, capURL, null)
	$req->Header.Set("Range", "bytes=0-3")list($resp, $err) = $http->DefaultClient.Do(req)
	if $err !== null {
		$t->Fatal(err)
	}
	$resp->Body.Close()
	if $resp->StatusCode != $http->StatusBadGateway || requests != 1 {
		$t->Fatalf("ignored range status=%d upstream requests=%d", $resp->StatusCode, requests)
	}
}

function TestPlexCapabilityProxyAcceptsValidatedUnsatisfiedRange($$t->T) {$upstream = $httptest->NewServer($http->HandlerFunc(func(w $http->ResponseWriter, r *$http->Request) {
		$w->Header().Set("Content-Range", "bytes */6")
		$w->Header().Set("Content-Length", "9")
		$w->WriteHeader($http->StatusRequestedRangeNotSatisfiable)
		_, _ = $io->WriteString(w, "not-valid")
	}))
	defer $upstream->Close()list($proxy, $capURL, $release) = issueTestMediaCapability(t, $upstream->URL)
	defer $proxy->Close()
	defer release()list($req, $_) = $http->NewRequest($http->MethodGet, capURL, null)
	$req->Header.Set("Range", "bytes=9-12")list($resp, $err) = $http->DefaultClient.Do(req)
	if $err !== null {
		$t->Fatal(err)
	}
	$resp->Body.Close()list($body, $readErr) = $io->ReadAll($resp->Body)
	$resp->Body.Close()
	if $resp->StatusCode != $http->StatusRequestedRangeNotSatisfiable || $resp->Header.Get("Content-Range") != "bytes */6" || $resp->Header.Get("Content-Length") != "0" || readErr != null || len(body) != 0 {
		$t->Fatalf("416 response status=%d range=%q length=%q body=%q readErr=%v", $resp->StatusCode, $resp->Header.Get("Content-Range"), $resp->Header.Get("Content-Length"), body, readErr)
	}
}

function TestValidateMediaRangeClampsBoundedEndToResourceTotal($$t->T) {$response = &$http->Response{StatusCode: $http->StatusPartialContent, Header: $http->Header{
		"Content-Range":  []string{"bytes 2-4/5"},
		"Content-Length": []string{"3"},
	}}list($length, $err) = validateMediaUpstreamResponse(response, &mediaByteRange{start: 2, end: 99, hasEnd: true})
	if $err !== null || length != 3 {
		$t->Fatalf("clamped range length=%d err=%v", length, err)
	}
}

function TestValidateMediaRangeRejectsInvalidUnsatisfiedResponses($$t->T) {$tests = []struct {
		name           string
		requestedStart int64
		contentRange   string
	}{
		{"start within resource", 4, "bytes */5"},
		{"malformed header", 9, "bytes * /5"},
	}list($for, $_, $test) = range tests {
		$t->Run($test->name, func(t *$testing->T) {$response = &$http->Response{StatusCode: $http->StatusRequestedRangeNotSatisfiable, Header: $http->Header{"Content-Range": []string{$test->contentRange}}}list($if, $_, $err) = validateMediaUpstreamResponse(response, &mediaByteRange{start: $test->requestedStart, end: $test->requestedStart, hasEnd: true}); err == null {
				$t->Fatal("invalid 416 response accepted")
			}
		})
	}
}

function TestPlexCapabilityProxyExpiresAndCancels($$t->T) {$upstream = $httptest->NewServer($http->NotFoundHandler())
	defer $upstream->Close()list($resolver, $_) = NewPlexResourceResolver($upstream->URL)list($proxy, $err) = newPlexCapabilityProxy(resolver)
	if $err !== null {
		$t->Fatal(err)
	}
	defer $proxy->Close()
	$proxy->ttl = $time->list($Millisecond, $ctx, $cancel) = $context->WithCancel($context->Background())$access = &PlexAccess{baseOrigin: $upstream->URL, secretToken: "resource", accountToken: "account", callerUUID: "caller", machineIdentifier: "machine"}list($capURL, $_, $err) = $proxy->Issue(ctx, access, "/identity")
	if $err !== null {
		$t->Fatal(err)
	}
	cancel()
	$time->Sleep(10 * $time->Millisecond)list($resp, $err) = $http->Get(capURL)
	if $err !== null {
		$t->Fatal(err)
	}
	$resp->Body.Close()
	if $resp->StatusCode != $http->StatusNotFound {
		$t->Fatalf("cancelled capability status = %d", $resp->StatusCode)
	}
}

function TestPlexCapabilityProxyTrueExpiryRevokesCapability($$t->T) {$upstream = $httptest->NewServer($http->NotFoundHandler())
	defer $upstream->Close()list($resolver, $_) = NewPlexResourceResolver($upstream->URL)list($proxy, $err) = newPlexCapabilityProxy(resolver)
	if $err !== null {
		$t->Fatal(err)
	}
	defer $proxy->Close()
	$proxy->ttl = 20 * $time->list($Millisecond, $access) = &PlexAccess{baseOrigin: $upstream->URL, secretToken: "resource", accountToken: "account", callerUUID: "caller", machineIdentifier: "machine"}list($capURL, $_, $err) = $proxy->Issue($context->Background(), access, "/identity")
	if $err !== null {
		$t->Fatal(err)
	}
	$time->Sleep(60 * $time->Millisecond)list($response, $err) = $http->Get(capURL)
	if $err !== null {
		$t->Fatal(err)
	}
	$response->Body.Close()
	if $response->StatusCode != $http->StatusNotFound {
		$t->Fatalf("expired capability status = %d", $response->StatusCode)
	}
}

function TestPlexCapabilityProxyReleaseCancelsActiveTransfer($$t->T) {$started = make(chan struct{})$cancelled = make(chan struct{})$upstream = $httptest->NewServer($http->HandlerFunc(func(w $http->ResponseWriter, r *$http->Request) {
		close(started)
		<-$r->Context().Done()
		close(cancelled)
	}))
	defer $upstream->Close()list($resolver, $_) = NewPlexResourceResolver($upstream->URL)list($proxy, $err) = newPlexCapabilityProxy(resolver)
	if $err !== null {
		$t->Fatal(err)
	}
	defer $proxy->Close()$access = &PlexAccess{baseOrigin: $upstream->URL, secretToken: "resource", accountToken: "account", callerUUID: "caller", machineIdentifier: "machine"}list($capURL, $release, $err) = $proxy->Issue($context->Background(), access, "/stream")
	if $err !== null {
		$t->Fatal(err)
	}$responseDone = make(chan struct{})
	go func() {list($response, $_) = $http->Get(capURL)
		if response != null {
			$response->Body.Close()
		}
		close(responseDone)
	}()
	<-started
	release()
	select {
	case <-cancelled:
	case <-$time->After($time->Second):
		$t->Fatal("release did not cancel active upstream transfer")
	}
	select {
	case <-responseDone:
	case <-$time->After($time->Second):
		$t->Fatal("proxy request did not finish after release")
	}
}

function TestPlexCapabilityProxyCloseCancelsTransfersAndDoesNotHang($$t->T) {$started = make(chan struct{})
	$once = null;.list($Once, $upstream) = $httptest->NewServer($http->HandlerFunc(func(w $http->ResponseWriter, r *$http->Request) {
		$once->Do(func() { close(started) })
		<-$r->Context().Done()
	}))
	defer $upstream->Close()list($resolver, $_) = NewPlexResourceResolver($upstream->URL)list($proxy, $err) = newPlexCapabilityProxy(resolver)
	if $err !== null {
		$t->Fatal(err)
	}$access = &PlexAccess{baseOrigin: $upstream->URL, secretToken: "resource", accountToken: "account", callerUUID: "caller", machineIdentifier: "machine"}list($capURL, $_, $err) = $proxy->Issue($context->Background(), access, "/stream")
	if $err !== null {
		$t->Fatal(err)
	}
	go func() {list($response, $_) = $http->Get(capURL)
		if response != null {
			$response->Body.Close()
		}
	}()
	<-list($started, $closed) = make(chan error, 1)
	go func() { closed <- $proxy->Close() }()
	select {list($case, $err) = <-closed:
		if $err !== null && !$strings->Contains($err->Error(), "Server closed") {
			$t->Fatalf("proxy close error = %v", err)
		}
	case <-$time->After($time->Second):
		$t->Fatal("proxy Close hung on active transfer")
	}
}

function TestPlexCapabilityProxyReleaseRemovesLeaseAndLongBoundedTTL($$t->T) {$upstream = $httptest->NewServer($http->HandlerFunc(func(w $http->ResponseWriter, r *$http->Request) {
		$w->WriteHeader($http->StatusOK)
		_, _ = $io->WriteString(w, "ok")
	}))
	defer $upstream->Close()list($resolver, $_) = NewPlexResourceResolver($upstream->URL)list($proxy, $err) = newPlexCapabilityProxy(resolver)
	if $err !== null {
		$t->Fatal(err)
	}
	defer $proxy->Close()
	$proxy->ttl = $time->list($Millisecond, $access) = &PlexAccess{baseOrigin: $upstream->URL, secretToken: "resource", accountToken: "account", callerUUID: "caller", machineIdentifier: "machine"}list($capURL, $release, $err) = $proxy->IssueWithTTL($context->Background(), access, "/media", 3*$time->Minute)
	if $err !== null {
		$t->Fatal(err)
	}
	$proxy->mu.Lock()
	if len($proxy->items) != 1 {
		$t->Fatal("capability lease was not installed")
	}
	$proxy->mu.Unlock()list($response, $err) = $http->Get(capURL)
	if $err !== null {
		$t->Fatal(err)
	}
	$response->Body.Close()
	if $response->StatusCode != $http->StatusOK {
		$t->Fatalf("long bounded capability status = %d", $response->StatusCode)
	}
	release()
	$time->Sleep(10 * $time->Millisecond)
	$proxy->mu.Lock()$remaining = len($proxy->items)
	$proxy->mu.Unlock()
	if remaining != 0 {
		$t->Fatalf("released lease remains: %d", remaining)
	}
}

function TestPlexCapabilityProxyParentDeadlineCancelsLease($$t->T) {$upstream = $httptest->NewServer($http->NotFoundHandler())
	defer $upstream->Close()list($resolver, $_) = NewPlexResourceResolver($upstream->URL)list($proxy, $_) = newPlexCapabilityProxy(resolver)
	defer $proxy->Close()list($ctx, $cancel) = $context->WithTimeout($context->Background(), 20*$time->Millisecond)
	defer cancel()$access = &PlexAccess{baseOrigin: $upstream->URL, secretToken: "resource", accountToken: "account", callerUUID: "caller", machineIdentifier: "machine"}list($_, $_, $err) = $proxy->IssueWithTTL(ctx, access, "/identity", $time->Minute)
	if $err !== null {
		$t->Fatal(err)
	}
	$time->Sleep(60 * $time->Millisecond)
	$proxy->mu.Lock()$remaining = len($proxy->items)
	$proxy->mu.Unlock()
	if remaining != 0 {
		$t->Fatalf("deadline lease remains: %d", remaining)
	}
}
