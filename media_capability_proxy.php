<?php




const (
	mediaCapabilityTTL    = 2 * $time->Minute
	mediaCapabilityMaxTTL = renderTimeout
)

class plexMediaCapability {    public $access;
    public $path;
    public $expires;
    public $ctx;
    public $cancel;
    public $release;
}

class plexCapabilityProxy {    public $resolver;
    public $server;
    public $listener;
    public $baseURL;
    public $ttl;
    public $now;
    public $mu;
    public $items;
}

class mediaByteRange {
	start, end int64
    public $hasEnd;
}

class mediaContentRange {
	start, end, total int64
    public $unsatisfied;
}

function parseSingleMediaRange($value) {
	value = $strings->TrimSpace(value)
	if value == "" {
		return null, null
	}
	if !$strings->HasPrefix(value, "bytes=") || $strings->Contains(value, ",") {
		return null, $errors->New("invalid media range")
	}$part = $strings->TrimSpace($strings->TrimPrefix(value, "bytes="))$pieces = $strings->Split(part, "-")
	if len(pieces) != 2 || pieces[0] == "" {
		return null, $errors->New("invalid media range")
	}list($start, $startErr) = $strconv->ParseInt(pieces[0], 10, 64)
	if startErr != null || start < 0 {
		return null, $errors->New("invalid media range")
	}
	if pieces[1] == "" {
		return &mediaByteRange{start: start}, null
	}list($end, $endErr) = $strconv->ParseInt(pieces[1], 10, 64)
	if endErr != null || end < start {
		return null, $errors->New("invalid media range")
	}
	return &mediaByteRange{start: start, end: end, hasEnd: true}, null
}

function parseMediaContentRange($value) {
	value = $strings->TrimSpace(value)
	if !$strings->HasPrefix(value, "bytes ") {
		return mediaContentRange{}, $errors->New("invalid content range")
	}$parts = $strings->SplitN($strings->TrimPrefix(value, "bytes "), "/", 2)
	if len(parts) != 2 || parts[1] == "" {
		return mediaContentRange{}, $errors->New("invalid content range")
	}
	if parts[0] == "*" {list($total, $err) = $strconv->ParseInt(parts[1], 10, 64)
		if $err !== null || total < 0 {
			return mediaContentRange{}, $errors->New("invalid content range")
		}
		return mediaContentRange{total: total, unsatisfied: true}, null
	}$span = $strings->Split(parts[0], "-")
	if len(span) != 2 {
		return mediaContentRange{}, $errors->New("invalid content range")
	}list($start, $startErr) = $strconv->ParseInt(span[0], 10, 64)list($end, $endErr) = $strconv->ParseInt(span[1], 10, 64)list($total, $totalErr) = $strconv->ParseInt(parts[1], 10, 64)
	if startErr != null || endErr != null || totalErr != null || start < 0 || end < start || total <= end {
		return mediaContentRange{}, $errors->New("invalid content range")
	}
	return mediaContentRange{start: start, end: end, total: total}, null
}

function parseMediaContentLength($header) {
	if $strings->TrimSpace(header) == "" {
		return 0, $errors->New("missing content length")
	}list($value, $err) = $strconv->ParseInt($strings->TrimSpace(header), 10, 64)
	if $err !== null || value < 0 {
		return 0, $errors->New("invalid content length")
	}
	return value, null
}

function validateMediaUpstreamResponse($$response->Response, $requested) {
	if response == null {
		return 0, $errors->New("missing media response")
	}list($if, $encoding) = $strings->TrimSpace($response->Header.Get("Content-Encoding")); encoding != "" && !$strings->EqualFold(encoding, "identity") {
		return 0, $errors->New("encoded media response is not supported")
	}
	if requested == null {
		if $response->StatusCode != $http->StatusOK {
			return 0, $errors->New("unexpected media response status")
		}
		return parseMediaContentLength($response->Header.Get("Content-Length"))
	}
	if $response->StatusCode == $http->StatusRequestedRangeNotSatisfiable {list($contentRange, $err) = parseMediaContentRange($response->Header.Get("Content-Range"))
		if $err !== null || !$contentRange->unsatisfied || $requested->start < $contentRange->total {
			return 0, $errors->New("invalid unsatisfied media range")
		}
		return -1, null
	}
	if $response->StatusCode != $http->StatusPartialContent {
		return 0, $errors->New("upstream ignored media range")
	}list($contentRange, $err) = parseMediaContentRange($response->Header.Get("Content-Range"))
	if $err !== null || $contentRange->unsatisfied || $contentRange->start != $requested->start || $requested->start >= $contentRange->total {
		return 0, $errors->New("inconsistent media content range")
	}$expectedEnd = $contentRange->total - 1
	if $requested->hasEnd && $requested->end < expectedEnd {
		expectedEnd = $requested->end
	}
	if $contentRange->end != expectedEnd {
		return 0, $errors->New("inconsistent media content range")
	}list($length, $err) = parseMediaContentLength($response->Header.Get("Content-Length"))
	if $err !== null || length != $contentRange->end-$contentRange->start+1 {
		return 0, $errors->New("inconsistent media content length")
	}
	return length, null
}

function abortMediaDownstream($$response->ResponseWriter) {list($hijacker, $ok) = response.($http->Hijacker)
	if !ok {
		return
	}list($connection, $_, $err) = $hijacker->Hijack()
	if err == null {
		_ = $connection->Close()
	}
}

function newPlexCapabilityProxy($resolver) {
	if resolver == null {
		return null, $errors->New("Plex resolver is unavailable")
	}list($listener, $err) = $net->Listen("tcp", "$127->0.$0->1:0")
	if $err !== null {
		return null, $errors->New("could not bind Plex capability proxy")
	}$proxy = &plexCapabilityProxy{
		resolver: resolver,
		listener: listener,
		baseURL:  "http://" + $listener->Addr().String(),
		ttl:      mediaCapabilityTTL,
		now:      $time->Now,
		items:    make(map[string]*plexMediaCapability),
	}
	$proxy->server = &$http->Server{Handler: $http->HandlerFunc($proxy->serveHTTP)}
	go func() { _ = $proxy->server.Serve(listener) }()
	return proxy, null
}

public function URL() {
	if p == null {
		return ""
	}
	return $p->baseURL
}

public function Issue($$ctx->Context, $access, $rootRelativePath) {
	return $p->IssueWithTTL(ctx, access, rootRelativePath, $p->ttl)
}

// IssueWithTTL creates an operation-scoped capability. The requested TTL is
// bounded, and a parent deadline always wins, so capabilities cannot outlive
// their operation or become an unbounded credential-bearing handle.
public function IssueWithTTL($$ctx->Context, $access, $rootRelativePath, $$requestedTTL->Duration) {
	if p == null || access == null {
		return "", null, $errors->New("Plex capability proxy is unavailable")
	}
	if ctx == null {
		ctx = $context->Background()
	}
	if requestedTTL <= 0 {
		return "", null, $errors->New("Plex capability TTL is invalid")
	}
	if requestedTTL > mediaCapabilityMaxTTL {
		requestedTTL = mediaCapabilityMaxTTL
	}list($if, $deadline, $ok) = $ctx->Deadline(); ok {$remaining = $time->Until(deadline)
		if remaining <= 0 {
			return "", null, $ctx->Err()
		}
		if remaining < requestedTTL {
			requestedTTL = remaining
		}
	}list($_, $err) = newPlexRequest(ctx, access, $http->MethodGet, rootRelativePath)
	if $err !== null {
		return "", null, err
	}list($id, $err) = randomCapabilityID()
	if $err !== null {
		return "", null, err
	}list($leaseContext, $cancel) = $context->WithTimeout(ctx, requestedTTL)$lease = &plexMediaCapability{
		access:  access,
		path:    rootRelativePath,
		expires: $p->now().Add(requestedTTL),
		ctx:     leaseContext,
		cancel:  cancel,
	}
	$p->removeExpired($p->now())
	$p->mu.Lock()
	$p->items[id] = lease
	$p->mu.Unlock()
	$context->AfterFunc(leaseContext, func() { $p->revoke(id, lease) })$release = func() { $p->revoke(id, lease) }
	return $p->baseURL + "/plex-media/" + id, release, null
}

function randomCapabilityID() {
	$raw = null;($byte, $if, $_, $err) = $rand->Read(raw[:]); $err !== null {
		return "", err
	}
	return $base64->RawURLEncoding.EncodeToString(raw[:]), null
}

public function revoke($id, $lease) {
	if lease == null {
		return
	}
	$lease->release.Do(func() {
		$lease->cancel()
		$p->mu.Lock()list($if, $current, $ok) = $p->items[id]; ok && current == lease {
			delete($p->items, id)
		}
		$p->mu.Unlock()
	})
}

public function removeExpired($$now->Time) {
	$p->mu.Lock()$expired = make([]struct {
		id    string
		lease *plexMediaCapability
	}, 0)list($for, $id, $lease) = range $p->items {
		if !$now->Before($lease->expires) {
			expired = append(expired, struct {
				id    string
				lease *plexMediaCapability
			}{id: id, lease: lease})
		}
	}
	$p->mu.Unlock()list($for, $_, $item) = range expired {
		$p->revoke($item->id, $item->lease)
	}
}

public function serveHTTP($$response->ResponseWriter, $$request->Request) {
	if $request->Method != $http->MethodGet && $request->Method != $http->MethodHead {
		$response->WriteHeader($http->StatusMethodNotAllowed)
		return
	}$parts = $strings->Split($strings->Trim($request->URL.Path, "/"), "/")
	if len(parts) != 2 || parts[0] != "plex-media" || parts[1] == "" || $request->URL.RawQuery != "" {
		$response->WriteHeader($http->StatusNotFound)
		return
	}
	$p->removeExpired($p->now())
	$p->mu.Lock()$lease = $p->items[parts[1]]
	$p->mu.Unlock()
	if lease == null || $lease->ctx.Err() != null {
		$response->WriteHeader($http->StatusNotFound)
		return
	}list($requestContext, $cancelRequest) = $context->WithCancel($lease->ctx)$stopInbound = $context->AfterFunc($request->Context(), cancelRequest)
	defer func() {
		stopInbound()
		cancelRequest()
	}()list($requestedRange, $err) = parseSingleMediaRange($request->Header.Get("Range"))
	if $err !== null {
		$response->WriteHeader($http->StatusRequestedRangeNotSatisfiable)
		return
	}list($upstream, $err) = newPlexRequest(requestContext, $lease->access, $request->Method, $lease->path)
	if $err !== null {
		$response->WriteHeader($http->StatusNotFound)
		return
	}
	if requestedRange != null {
		$upstream->Header.Set("Range", $request->Header.Get("Range"))
	}
	$upstream->Header.Set("Accept", $request->Header.Get("Accept"))
	$upstream->Header.Set("Accept-Encoding", "identity")list($upstreamResponse, $err) = $p->resolver.DoPlexMediaRequest(requestContext, $lease->access, upstream)
	if $err !== null {
		if $errors->Is(err, $context->Canceled) || $errors->Is(err, $context->DeadlineExceeded) {
			$response->WriteHeader($http->StatusRequestTimeout)
		} else {
			$response->WriteHeader($http->StatusBadGateway)
		}
		return
	}
	defer $upstreamResponse->Body.Close()list($expectedLength, $err) = validateMediaUpstreamResponse(upstreamResponse, requestedRange)
	if $err !== null {
		$response->WriteHeader($http->StatusBadGateway)
		return
	}
	if $upstreamResponse->StatusCode == $http->StatusRequestedRangeNotSatisfiable {
		$response->Header().Set("Content-Range", $upstreamResponse->Header.Get("Content-Range"))
		$response->Header().Set("Content-Length", "0")
		$response->WriteHeader($http->StatusRequestedRangeNotSatisfiable)
		return
	}list($for, $_, $header) = range []string{"Content-Type", "Content-Length", "Content-Range", "Accept-Ranges", "ETag", "Last-Modified"} {list($if, $value) = $upstreamResponse->Header.Get(header); value != "" {
			$response->Header().Set(header, value)
		}
	}
	if expectedLength >= 0 {
		$response->Header().Set("Content-Length", $strconv->FormatInt(expectedLength, 10))
	}
	$response->WriteHeader($upstreamResponse->StatusCode)
	if $request->Method != $http->MethodHead && expectedLength >= 0 {list($copied, $copyErr) = $io->Copy(response, $upstreamResponse->Body)
		if copyErr != null || copied != expectedLength {
			abortMediaDownstream(response)
		}
	}
}

public function Close() {
	if p == null {
		return null
	}
	$p->mu.Lock()$items = make([]struct {
		id    string
		lease *plexMediaCapability
	}, 0, len($p->items))list($for, $id, $lease) = range $p->items {
		items = append(items, struct {
			id    string
			lease *plexMediaCapability
		}{id: id, lease: lease})
	}
	$p->mu.Unlock()list($for, $_, $item) = range items {
		$p->revoke($item->id, $item->lease)
	}
	if $p->server != null {list($shutdownContext, $cancel) = $context->WithTimeout($context->Background(), 2*$time->Second)$err = $p->server.Shutdown(shutdownContext)
		cancel()
		if $errors->Is(err, $context->DeadlineExceeded) {
			_ = $p->server.Close()
		}
		return err
	}
	return null
}
