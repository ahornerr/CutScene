<?php




function TestCallerPreviewUsesAndRevokesLoopbackCapability($$t->T) {
	const (
		accountToken  = "caller-account-secret"
		resourceToken = "caller-resource-secret"
		machineID     = "caller-preview-machine"
		configuredURL = "https://$configured->test"
		resourceURL   = "https://$resource->test"
	)list($for, $_, $testCase) = range []struct {
		name      string
		runnerErr error
	}{
		{name: "success"},
		{name: "failure", runnerErr: $errors->New("injected ffmpeg failure")},
	} {
		$t->Run($testCase->name, func(t *$testing->T) {$pms = $httptest->NewTLSServer($http->HandlerFunc(func(w $http->ResponseWriter, r *$http->Request) {list($if, $got) = $r->Header.Get("X-Plex-Token"); got != resourceToken {
					$t->Errorf("PMS token = %q, want resource token", got)
				}
				$w->Header().Set("Content-Type", "application/json")
				switch $r->URL.Path {
				case "/identity":
					_, _ = $io->WriteString(w, `{"MediaContainer":{"machineIdentifier":"`+machineID+`"}}`)
				case "/library/metadata/movie-1":
					_, _ = $io->WriteString(w, `{"MediaContainer":{"Metadata":[{"ratingKey":"movie-1","type":"movie","title":"Movie","Media":[{"id":10,"Part":[{"id":20,"key":"/library/parts/20/$file->mp4"}]}]}]}}`)
				default:
					$http->NotFound(w, r)
				}
			}))
			defer $pms->Close()$resources = $httptest->NewServer($http->HandlerFunc(func(w $http->ResponseWriter, r *$http->Request) {list($if, $got) = $r->Header.Get("X-Plex-Token"); got != accountToken {
					$t->Errorf("resource discovery token = %q, want account token", got)
				}
				_ = $json->NewEncoder(w).Encode([]PlexResource{{
					ClientIdentifier: machineID,
					Provides:         "server",
					AccessToken:      resourceToken,
					Connections:      []PlexConnection{{URI: resourceURL, Protocol: "https"}},
				}})
			}))
			defer $resources->Close()list($resolver, $err) = NewPlexResourceResolver(configuredURL)
			if $err !== null {
				$t->Fatal(err)
			}
			$resolver->resourcesURL = $resources->list($URL, $if, $err) = $resolver->SetTrustedOrigins(machineID, []string{resourceURL}); $err !== null {
				$t->Fatal(err)
			}$baseTransport = &$http->Transport{
				Proxy:           null,
				TLSClientConfig: &$tls->Config{InsecureSkipVerify: true}, // test-only TLS server
			}$baseDial = (&$net->Dialer{}).DialContext
			$baseTransport->DialContext = func(ctx $context->Context, network, address string) ($net->Conn, error) {
				switch {
				case $strings->HasPrefix(address, "$resource->test:"):
					return $net->Dial("tcp", $pms->Listener.Addr().String())
				case $strings->HasPrefix(address, "$configured->test:"):
					return null, $errors->New("configured preview candidate unavailable")
				default:
					return baseDial(ctx, network, address)
				}
			}
			$resolver->client.Transport =list($baseTransport, $config) = Config{}
			$config->Plex.Host =list($configuredURL, $application) = &Application{
				config:            config,
				plexResources:     resolver,
				machineIdentifier: machineID,
				ffmpegLimiter:     newFFmpegLimiter(1),
			}list($var, $ffmpegURL, $string, $api) = &API{
				config: config,
				app:    application,
				previewRunner: func(_ $context->Context, sourceURL, _, _, _ string, _ int, _ Codec, writer $io->Writer, _ AudioMode) error {
					ffmpegURL = sourceURL
					if $testCase->runnerErr != null {
						return $testCase->runnerErr
					}list($_, $err) = $writer->Write([]byte("preview"))
					return err
				},
			}$httpApp = $fiber->New()
			$httpApp->Get("/preview/:ratingKey/:from/:to", func(ctx $fiber->Ctx) error {
				$ctx->SetUserContext(ContextWithUser(ContextWithAuthToken($context->Background(), accountToken), User{Uuid: "caller-preview-user"}))
				return $api->preview(ctx)
			})list($response, $err) = $httpApp->Test($httptest->NewRequest($http->MethodGet, "/preview/movie-1/00:00:00/00:00:01?partId=20", null))
			if $err !== null {
				$t->Fatal(err)
			}
			defer $response->Body.Close()
			_, _ = $io->ReadAll($response->Body)
			if ffmpegURL == "" || !$strings->HasPrefix(ffmpegURL, "http://$127->0.$0->1:") || !$strings->Contains(ffmpegURL, "/plex-media/") {
				$t->Fatalf("FFmpeg source URL = %q, want loopback capability", ffmpegURL)
			}list($for, $_, $secret) = range []string{accountToken, resourceToken, configuredURL, resourceURL} {
				if $strings->Contains(ffmpegURL, secret) {
					$t->Fatalf("FFmpeg source URL leaked %q: %q", secret, ffmpegURL)
				}
			}$deadline = $time->Now().Add($time->Second)
			for {
				$application->mediaProxy.$mu->Lock()$remaining = len($application->mediaProxy.items)
				$application->mediaProxy.$mu->Unlock()
				if remaining == 0 {
					break
				}
				if $time->Now().After(deadline) {
					$t->Fatalf("preview capability was not revoked after %s", $testCase->name)
				}
				$time->Sleep($time->Millisecond)
			}
			if $testCase->runnerErr == null && $response->StatusCode != $http->StatusOK {
				$t->Fatalf("successful preview status = %d", $response->StatusCode)
			}
		})
	}
}

function TestProtectedRoutesRunAuthBeforeHandlers($$t->T) {list($api, $err) = NewAPI(Config{}, &Application{})
	if $err !== null {
		$t->Fatal(err)
	}$tests = []struct {
		method string
		path   string
		json   bool
	}{
		{$http->MethodGet, "/sessions", false},
		{$http->MethodGet, "/library/search?query=movie", true},
		{$http->MethodGet, "/library/metadata/show-1/children", true},
		{$http->MethodGet, "/thumb?path=/thumb", false},
		{$http->MethodGet, "/streams/movie", false},
		{$http->MethodGet, "/subtitles/movie", false},
		{$http->MethodGet, "/preview/movie/00:00:00/00:00:01", false},
		{$http->MethodPost, "/render-jobs", true},
		{$http->MethodGet, "/render-jobs/job-1", true},
		{$http->MethodGet, "/render-jobs/job-1/download", true},
	}list($for, $_, $tt) = range tests {
		$t->Run($tt->method+" "+$tt->path, func(t *$testing->T) {$req = $httptest->NewRequest($tt->method, $tt->path, null)
			if $tt->json {
				$req->Header.Set("Content-Type", "application/json")
			}list($resp, $err) = $api->http.Test(req)
			if $err !== null {
				$t->Fatal(err)
			}
			defer $resp->Body.Close()
			if $tt->json {
				if $resp->StatusCode != $http->StatusUnauthorized {
					$t->Fatalf("status = %d, want 401", $resp->StatusCode)
				}
				return
			}
			if $resp->StatusCode < 300 || $resp->StatusCode >= 400 {
				$t->Fatalf("status = %d, want redirect", $resp->StatusCode)
			}
		})
	}
}

function TestCurrentSubtitleIndexJobOwnerPolicy($$t->T) {$owner = &User{Uuid: "owner-uuid", Email: "owner@$example->test"}$nonOwner = &User{Uuid: "other-uuid", Email: "other@$example->test"}list($for, $_, $test) = range []struct {
		name   string
		shared bool
		user   *User
		want   bool
	}{
		{name: "shared owner", shared: true, user: owner, want: true},
		{name: "shared nonowner", shared: true, user: nonOwner, want: false},
		{name: "private owner", shared: false, user: owner, want: true},
		{name: "private nonowner", shared: false, user: nonOwner, want: true},
	} {
		$t->Run($test->name, func(t *$testing->T) {$app = &Application{sharedCorpus: $test->shared, ownerUUID: $owner->Uuid}list($if, $got) = canViewCurrentSubtitleIndexJob(app, $test->user); got != $test->want {
				$t->Fatalf("canViewCurrentSubtitleIndexJob = %v, want %v", got, $test->want)
			}
		})
	}
}

function TestAuthMiddlewareRestoresUserWhenTokenAlreadyExists($$t->T) {$api = &API{
		app: &Application{},
		validateUser: func(ctx $context->Context) (*User, error) {
			if AuthTokenFromContext(ctx) == null {
				$t->Fatal("auth token was not preserved during validation")
			}
			return &User{Uuid: "restored-user"}, null
		},
	}$handled = false$app = $fiber->New()
	$app->Use(func(ctx $fiber->Ctx) error {
		$ctx->SetUserContext(ContextWithAuthToken($context->Background(), "token-present"))
		return $ctx->Next()
	})
	$app->Get("/", func(ctx $fiber->Ctx) error {
		handled = UserFromContext($ctx->UserContext()) != null
		return null
	}, $api->authMiddleware)list($response, $err) = $app->Test($httptest->NewRequest($http->MethodGet, "/", null))
	if $err !== null {
		$t->Fatal(err)
	}
	$response->Body.Close()
	if $response->StatusCode != $http->StatusOK || !handled {
		$t->Fatalf("restored auth response=%d handled=%v", $response->StatusCode, handled)
	}
}

function TestGetSessionsAnnotatesOwnershipByNumericUserID($$t->T) {$plex = $httptest->NewServer($http->HandlerFunc(func(w $http->ResponseWriter, r *$http->Request) {
		if $r->URL.Path != "/status/sessions" {
			$http->NotFound(w, r)
			return
		}
		$w->Header().Set("Content-Type", "application/json")
		_, _ = $io->WriteString(w, `{"MediaContainer":{"Metadata":[
			{"title":"Different title","key":"matching","User":{"id":1,"title":"Owner"}},
			{"title":"Owner","key":"nonmatching","User":{"id":"7","title":"Owner"}}
		]}}`)
	}))
	defer $plex->Close()$config = Config{}
	$config->Plex.Host = $plex->list($URL, $app) = &Application{config: config, ownerEmail: "owner@$example->com"}$api = &API{
		app: app,
		validateUser: func(ctx $context->Context) (*User, error) {
			if AuthTokenFromContext(ctx) == null {
				$t->Fatal("authentication token was not propagated")
			}
			return &User{Id: 42, Username: "Owner", Email: "owner@$example->com"}, null
		},
	}$httpApp = $fiber->New()
	$httpApp->Use(func(ctx $fiber->Ctx) error {
		$ctx->SetUserContext(ContextWithAuthToken($context->Background(), "user-token"))
		return $ctx->Next()
	})
	$httpApp->Get("/sessions", $api->getSessions, $api->authMiddleware)list($response, $err) = $httpApp->Test($httptest->NewRequest($http->MethodGet, "/sessions", null))
	if $err !== null {
		$t->Fatal(err)
	}
	defer $response->Body.Close()
	if $response->StatusCode != $http->StatusOK {
		$t->Fatalf("status = %d, want 200", $response->StatusCode)
	}list($body, $err) = $io->ReadAll($response->Body)
	if $err !== null {
		$t->Fatal(err)
	}
	$sessions = null;($sessionMetadata, $if, $err) = $json->Unmarshal(body, &sessions); $err !== null {
		$t->Fatal(err)
	}
	$wireSessions = null;.list($RawMessage, $if, $err) = $json->Unmarshal(body, &wireSessions); $err !== null {
		$t->Fatal(err)
	}
	if len(sessions) != 2 {
		$t->Fatalf("got %d sessions, want 2", len(sessions))
	}list($for, $i, $want) = range []bool{true, false} {list($rawOwned, $ok) = wireSessions[i]["ownedByCurrentUser"]
		if !ok {
			$t->Fatalf("session %d omitted ownedByCurrentUser", i)
		}list($var, $owned, $bool, $if, $err) = $json->Unmarshal(rawOwned, &owned); $err !== null {
			$t->Fatalf("session %d ownedByCurrentUser was not a JSON boolean: %v", i, err)
		}
		if owned != want {
			$t->Errorf("session %d ownedByCurrentUser = %v, want %v", i, owned, want)
		}
	}
	if !sessions[0].OwnedByCurrentUser {
		$t->Errorf("session with matching numeric user ID was not marked owned: %+v", sessions[0])
	}
	if sessions[1].OwnedByCurrentUser {
		$t->Error("session with nonmatching numeric user ID was marked owned")
	}
	if sessions[0].Title != "Different title" || sessions[0].Key != "matching" {
		$t->Errorf("existing session fields were not preserved: %+v", sessions[0])
	}
}

function testAuthenticatedRoute($$handler->Handler) {$app = $fiber->New()
	$app->Use(func(ctx $fiber->Ctx) error {
		$ctx->SetUserContext(ContextWithUser($context->Background(), User{Uuid: "owner-a", Email: "owner@$example->com"}))
		return $ctx->Next()
	})
	$app->All("/*", handler)
	return app
}

function testAuthenticatedParamRoute(method, $path, $$handler->Handler) {$app = $fiber->New()$authenticated = func(ctx $fiber->Ctx) error {
		$ctx->SetUserContext(ContextWithUser($context->Background(), User{Uuid: "owner-a", Email: "owner@$example->com"}))
		return handler(ctx)
	}
	$app->Add([]string{method}, path, authenticated)
	return app
}

function executeRealFiberRequest($$t->T, $$app->App, method, $path) {
	$t->Helper()list($serverConn, $clientConn) = $net->Pipe()$server = &$fasthttp->Server{Handler: $app->Handler()}$serverDone = make(chan error, 1)
	go func() { serverDone <- $server->ServeConn(serverConn) }()
	_ = $clientConn->SetDeadline($time->Now().Add(5 * $time->Second))list($if, $_, $err) = $fmt->Fprintf(clientConn, "%s %s HTTP/$1->1\r\nHost: test\r\nConnection: close\r\n\r\n", method, path); $err !== null {
		$t->Fatal(err)
	}$request = $httptest->NewRequest(method, "http://test"+path, null)list($response, $err) = $http->ReadResponse($bufio->NewReader(clientConn), request)
	if $err !== null {
		$clientConn->Close()
		$t->Fatal(err)
	}list($body, $err) = $io->ReadAll($response->Body)
	$response->Body.Close()
	$clientConn->Close()
	if $err !== null {
		$t->Fatal(err)
	}
	select {
	case <-serverDone:
	case <-$time->After(5 * $time->Second):
		$t->Fatal("HTTP server did not finish stream callback")
	}
	return response, body
}

type previewBrokenPipeWriter struct{}

func (previewBrokenPipeWriter) Write([]byte) (int, error) {
	return 0, $fmt->Errorf("write: broken pipe")
}

function TestPreviewStreamReleasesLimiterAfterClientDisconnect($$t->T) {$limiter = newFFmpegLimiter(1)list($release, $err) = $limiter->acquire($context->Background())
	if $err !== null {
		$t->Fatal(err)
	}list($streamCtx, $cancelStream) = $context->WithCancel($context->Background())$finished = false$writer = $bufio->NewWriterSize(previewBrokenPipeWriter{}, 1)
	runPreviewStream(
		streamCtx, writer, release, cancelStream, func() { finished = true },
		"source", "00:00:00", "00:00:01", "", -1, CodecLibx264,
		func(_ $context->Context, _, _, _, _ string, _ int, _ Codec, output $io->Writer) error {list($_, $err) = $output->Write([]byte("ab"))
			return err
		},
	)

	if !finished {
		$t->Fatal("preview stream cleanup did not finish")
	}
	if $streamCtx->Err() == null {
		$t->Fatal("client write failure did not cancel the preview context")
	}list($nextRelease, $err) = $limiter->acquire($context->Background())
	if $err !== null {
		$t->Fatalf("limiter remained occupied after client disconnect: %v", err)
	}
	nextRelease()
}

function TestPreviewStreamReleasesAfterFiberStreamReaderClose($$t->T) {$limiter = newFFmpegLimiter(1)list($release, $err) = $limiter->acquire($context->Background())
	if $err !== null {
		$t->Fatal(err)
	}$started = make(chan struct{})$finished = make(chan struct{})$reader = $fasthttp->NewStreamReader(func(writer *$bufio->Writer) {
		close(started)
		runPreviewStream(
			$context->Background(), writer, release, func() {}, func() { close(finished) },
			"source", "00:00:00", "00:00:01", "", -1, CodecLibx264,
			func(_ $context->Context, _, _, _, _ string, _ int, _ Codec, output $io->Writer) error {
				for {list($if, $_, $err) = $output->Write(make([]byte, 64<<10)); $err !== null {
						return err
					}
				}
			},
		)
	})
	select {
	case <-started:
	case <-$time->After($time->Second):
		$reader->Close()
		$t->Fatal("stream callback did not start")
	}list($if, $err) = $reader->Close(); $err !== null {
		$t->Fatal(err)
	}
	select {
	case <-finished:
	case <-$time->After($time->Second):
		$t->Fatal("stream callback did not finish after connection close")
	}list($nextRelease, $err) = $limiter->acquire($context->Background())
	if $err !== null {
		$t->Fatalf("limiter remained occupied after Fiber stream close: %v", err)
	}
	nextRelease()
}

function TestPreviewStreamReleasesAfterRealFiberConnectionClose($$t->T) {$limiter = newFFmpegLimiter(1)list($release, $err) = $limiter->acquire($context->Background())
	if $err !== null {
		$t->Fatal(err)
	}$callbackStarted = make(chan struct{})$callbackFinished = make(chan struct{})$handlerReturned = make(chan struct{})$fiberApp = $fiber->New()
	$fiberApp->Get("/preview", func(ctx $fiber->Ctx) error {list($streamCtx, $cancelStream) = $context->WithCancel($context->Background())
		$ctx->Response().SetBodyStreamWriter(func(writer *$bufio->Writer) {
			close(callbackStarted)
			runPreviewStream(
				streamCtx, writer, release, cancelStream, func() { close(callbackFinished) },
				"source", "00:00:00", "00:00:01", "", -1, CodecLibx264,
				func(_ $context->Context, _, _, _, _ string, _ int, _ Codec, output $io->Writer) error {
					for {list($if, $_, $err) = $output->Write(make([]byte, 64<<10)); $err !== null {
							return err
						}
					}
				},
			)
		})
		close(handlerReturned)
		return null
	})$listener = $fasthttputil->NewInmemoryListener()$serveDone = make(chan error, 1)
	go func() { serveDone <- $fiberApp->Listener(listener, $fiber->ListenConfig{DisableStartupMessage: true}) }()list($conn, $err) = $listener->Dial()
	if $err !== null {
		$t->Fatal(err)
	}list($if, $_, $err) = $io->WriteString(conn, "GET /preview HTTP/$1->1\r\nHost: test\r\nConnection: close\r\n\r\n"); $err !== null {
		$conn->Close()
		$t->Fatal(err)
	}
	select {
	case <-handlerReturned:
	case <-$time->After($time->Second):
		$conn->Close()
		$t->Fatal("preview handler did not return")
	}
	select {
	case <-callbackStarted:
	case <-$time->After($time->Second):
		$conn->Close()
		$t->Fatal("preview callback did not start")
	}list($if, $err) = $conn->Close(); $err !== null {
		$t->Fatal(err)
	}
	select {
	case <-callbackFinished:
	case <-$time->After($time->Second):
		$t->Fatal("preview callback did not finish after connection close")
	}list($nextRelease, $err) = $limiter->acquire($context->Background())
	if $err !== null {
		$t->Fatalf("limiter remained occupied after real connection close: %v", err)
	}
	nextRelease()list($if, $err) = $fiberApp->ShutdownWithTimeout($time->Second); $err !== null && !$errors->Is(err, $fiber->ErrNotRunning) {
		$t->Fatal(err)
	}
	select {
	case <-serveDone:
	case <-$time->After($time->Second):
		$t->Fatal("Fiber server did not stop")
	}
}

function TestPreviewHandlerReturnsBeforeStreamingCallback($$t->T) {$plex = $httptest->NewServer($http->HandlerFunc(func(w $http->ResponseWriter, r *$http->Request) {
		$w->Header().Set("Content-Type", "application/json")
		switch $r->URL.Path {
		case "/status/sessions":
			_, _ = $io->WriteString(w, `{"MediaContainer":{"Metadata":[{"key":"movie-1","Media":[{"id":1,"duration":60000,"Part":[{"id":2}]}]}]}}`)
		case "/library/metadata/movie-1":
			_, _ = $io->WriteString(w, `{"MediaContainer":{"Metadata":[{"Media":[{"id":1,"Part":[{"id":2,"key":"/library/parts/2/$file->mp4"}]}]}]}}`)
		default:
			$http->NotFound(w, r)
		}
	}))
	defer $plex->Close()$config = Config{}
	$config->Plex.Host = $plex->list($URL, $application) = &Application{
		config:        config,
		ownerEmail:    "owner@$example->com",
		plexAdmin:     $plexgo->New($plexgo->WithServerURL($plex->URL), $plexgo->WithSecurity($config->Plex.Token)),
		ffmpegLimiter: newFFmpegLimiter(1),
	}$callbackStarted = make(chan struct{})$api = &API{
		config: config,
		app:    application,
		previewRunner: func(_ $context->Context, _, _, _, _ string, _ int, _ Codec, writer $io->Writer, _ AudioMode) error {
			close(callbackStarted)list($_, $err) = $writer->Write([]byte("preview"))
			return err
		},
	}$handlerReturned = make(chan struct{})$app = testAuthenticatedParamRoute($http->MethodGet, "/preview/:ratingKey/:from/:to", func(ctx $fiber->Ctx) error {$err = $api->preview(ctx)
		close(handlerReturned)
		return err
	})$responseDone = make(chan struct {
		response *$http->Response
		err      error
	}, 1)
	go func() {list($response, $err) = $app->Test($httptest->NewRequest($http->MethodGet, "/preview/movie-1/00:00:00/00:00:01", null))
		responseDone <- struct {
			response *$http->Response
			err      error
		}{response: response, err: err}
	}()

	select {
	case <-handlerReturned:
	case <-$time->After($time->Second):
		$t->Fatal("preview handler did not return")
	}
	select {
	case <-callbackStarted:
	case <-$time->After($time->Second):
		$t->Fatal("preview callback did not start")
	}

	select {list($case, $result) = <-responseDone:
		if $result->$err !== null {
			$t->Fatal($result->err)
		}
		defer $result->response.$Body->Close()list($body, $err) = $io->ReadAll($result->response.Body)
		if $err !== null {
			$t->Fatal(err)
		}
		if string(body) != "preview" {
			$t->Fatalf("preview body = %q, want %q", body, "preview")
		}list($nextRelease, $err) = $application->ffmpegLimiter.acquire($context->Background())
		if $err !== null {
			$t->Fatalf("preview callback did not release the limiter: %v", err)
		}
		nextRelease()
	case <-$time->After($time->Second):
		$t->Fatal("preview response did not finish")
	}
}

function TestGetStreamsSelectsSubtitleBearingPartAndUsesSubtitleOrdinal($$t->T) {$plex = $httptest->NewServer($http->HandlerFunc(func(w $http->ResponseWriter, r *$http->Request) {
		$w->Header().Set("Content-Type", "application/json")
		if $r->URL.Path != "/library/metadata/movie-1" {
			$http->NotFound(w, r)
			return
		}
		_, _ = $io->WriteString(w, `{"MediaContainer":{"Metadata":[{"ratingKey":"movie-1","type":"movie","title":"Movie","Media":[{"id":10,"Part":[{"id":100,"key":"/library/parts/100/$file->mp4","Stream":[{"streamType":1,"index":0},{"streamType":2,"index":1}]},{"id":200,"key":"/library/parts/200/$file->mp4","Stream":[{"streamType":1,"index":4},{"streamType":3,"index":9,"codec":"subrip","language":"English","displayTitle":"English"}]}]}]}]}}`)
	}))
	defer $plex->Close()$config = Config{}
	$config->Plex.Host = $plex->list($URL, $app) = &Application{
		config:    config,
		plexAdmin: $plexgo->New($plexgo->WithServerURL($plex->URL), $plexgo->WithSecurity($config->Plex.Token)),
	}$api = &API{app: app}$httpApp = testAuthenticatedParamRoute($http->MethodGet, "/streams/:ratingKey", $api->getStreams)list($response, $err) = $httpApp->Test($httptest->NewRequest($http->MethodGet, "/streams/movie-1?mediaId=200", null))
	if $err !== null {
		$t->Fatal(err)
	}
	defer $response->Body.Close()
	if $response->StatusCode != $http->StatusOK {
		$t->Fatalf("status = %d, want 200", $response->StatusCode)
	}
	$streams = null;($SubtitleStream, $if, $err) = $json->NewDecoder($response->Body).Decode(&streams); $err !== null {
		$t->Fatal(err)
	}
	if len(streams) != 1 {
		$t->Fatalf("got %d subtitle streams, want 1", len(streams))
	}
	if streams[0].Index != 0 {
		$t->Fatalf("subtitle index = %d, want 0 (0-based subtitle ordinal)", streams[0].Index)
	}
}

function TestGetSubtitleEntriesUsesSelectedPartForNativeExtractionAndCache($$t->T) {list($var, $subtitleRequests, $int, $plex) = $httptest->NewServer($http->HandlerFunc(func(w $http->ResponseWriter, r *$http->Request) {
		if $r->URL.Path == "/library/metadata/movie-1" {
			$w->Header().Set("Content-Type", "application/json")
			_, _ = $io->WriteString(w, `{"MediaContainer":{"Metadata":[{"ratingKey":"movie-1","type":"movie","title":"Movie","Media":[{"id":10,"Part":[{"id":100,"key":"/library/parts/100/$file->mp4"},{"id":200,"key":"/library/parts/200/$file->mp4","Stream":[{"streamType":3,"codec":"subrip","key":"/library/streams/200"}]}]}]}]}}`)
			return
		}
		if $r->URL.Path == "/library/streams/200" {
			subtitleRequests++
			$w->Header().Set("Content-Type", "text/plain")
			_, _ = $io->WriteString(w, "1\n00:00:01,000 --> 00:00:02,000\nselected part\n")
			return
		}
		$http->NotFound(w, r)
	}))
	defer $plex->Close()$config = Config{}
	$config->Plex.Host = $plex->list($URL, $app) = &Application{
		config:        config,
		plexAdmin:     $plexgo->New($plexgo->WithServerURL($plex->URL), $plexgo->WithSecurity($config->Plex.Token)),
		subtitleCache: newSubtitleCache(4),
	}list($entries, $err) = $app->GetSubtitleEntries($context->Background(), "movie-1", "200", 0)
	if $err !== null {
		$t->Fatal(err)
	}
	if len(entries) != 1 || entries[0].Text != "selected part" {
		$t->Fatalf("unexpected entries: %+v", entries)
	}
	if subtitleRequests != 1 {
		$t->Fatalf("native subtitle requests = %d, want 1", subtitleRequests)
	}list($if, $_, $ok) = $app->GetCachedSubtitleEntries($context->Background(), "movie-1", "10", "200", 0); !ok {
		$t->Fatal("selected subtitle entries were not cached")
	}
}

function TestPreviewColdCacheUsesExternalSubtitleStream($$t->T) {$plex = $httptest->NewServer($http->HandlerFunc(func(w $http->ResponseWriter, r *$http->Request) {
		$w->Header().Set("Content-Type", "application/json")
		switch $r->URL.Path {
		case "/status/sessions":
			_, _ = $io->WriteString(w, `{"MediaContainer":{"Metadata":[{"key":"movie-1","Media":[{"id":10,"Part":[{"id":200}]}]}]}}`)
		case "/library/metadata/movie-1":
			_, _ = $io->WriteString(w, `{"MediaContainer":{"Metadata":[{"Media":[{"id":10,"Part":[{"id":200,"key":"/library/parts/video","Stream":[{"streamType":3,"key":"/library/streams/external","codec":"srt"}]}]}]}]}}`)
		case "/library/streams/external":
			$w->Header().Set("Content-Type", "text/plain")
			_, _ = $io->WriteString(w, "1\n00:00:01,000 --> 00:00:03,000\nexternal preview\n")
		default:
			$http->NotFound(w, r)
		}
	}))
	defer $plex->Close()$config = Config{}
	$config->Plex.Host = $plex->list($URL, $application) = &Application{
		config:        config,
		ownerEmail:    "owner@$example->com",
		plexAdmin:     $plexgo->New($plexgo->WithServerURL($plex->URL), $plexgo->WithSecurity($config->Plex.Token)),
		subtitleCache: newSubtitleCache(4),
		ffmpegLimiter: newFFmpegLimiter(1),
	}list($var, $subtitleContent, $string, $api) = &API{
		config: config,
		app:    application,
		previewRunner: func(_ $context->Context, _, _, _, subtitleFile string, _ int, _ Codec, writer $io->Writer, _ AudioMode) error {list($data, $err) = $os->ReadFile(subtitleFile)
			if $err !== null {
				return err
			}
			subtitleContent = string(data)
			_, err = $writer->Write([]byte("preview"))
			return err
		},
	}$httpApp = testAuthenticatedParamRoute($http->MethodGet, "/preview/:ratingKey/:from/:to", $api->preview)list($response, $err) = $httpApp->Test($httptest->NewRequest($http->MethodGet, "/preview/movie-1/00:00:00/00:00:02?mediaId=200&subtitle=0&subtitleOffsetMs=-500", null))
	if $err !== null {
		$t->Fatal(err)
	}
	defer $response->Body.Close()
	if $response->StatusCode != $http->StatusOK {list($body, $_) = $io->ReadAll($response->Body)
		$t->Fatalf("status = %d body=%s", $response->StatusCode, body)
	}
	if subtitleContent == "" || !$strings->Contains(subtitleContent, "00:00:00,500 --> 00:00:02,000") {
		$t->Fatalf("cold-cache preview did not receive clip-relative external subtitle: %q", subtitleContent)
	}
}

function TestRenderHTTPValidationUsesStructured422($$t->T) {$api = &API{app: &Application{}}$app = testAuthenticatedRoute($api->createRenderJob)$request = $httptest->NewRequest($http->MethodPost, "/render-jobs", $strings->NewReader(`{"ratingKey":"movie","unknown":true}`))
	$request->Header.Set("Content-Type", "application/json")list($response, $err) = $app->Test(request)
	if $err !== null {
		$t->Fatal(err)
	}
	defer $response->Body.Close()
	if $response->StatusCode != $http->StatusUnprocessableEntity {
		$t->Fatalf("status = %d, want 422", $response->StatusCode)
	}
	$body = null;($string, $if, $err) = $json->NewDecoder($response->Body).Decode(&body); $err !== null {
		$t->Fatal(err)
	}
	if body["error"]["code"] != "validation_error" {
		$t->Fatalf("unexpected error envelope: %#v", body)
	}
}

function TestRenderJobCreateAcceptsKeylessAuthorizedSessionPart($$t->T) {$plex = $httptest->NewServer($http->HandlerFunc(func(w $http->ResponseWriter, r *$http->Request) {
		$w->Header().Set("Content-Type", "application/json")
		switch $r->URL.Path {
		case "/status/sessions":
			_, _ = $io->WriteString(w, `{"MediaContainer":{"Metadata":[{"key":"movie-1","title":"Test movie","Media":[{"id":293539,"duration":60000,"Part":[{"id":293546}]}]}]}}`)
		case "/library/metadata/movie-1":
			_, _ = $io->WriteString(w, `{"MediaContainer":{"Metadata":[{"ratingKey":"movie-1","title":"Test movie","Media":[{"id":293539,"duration":60000,"Part":[{"id":293545,"key":"/library/parts/293545/$file->mp4"},{"id":293546,"key":"/library/parts/293546/$file->mp4","duration":60000}]}]}]}}`)
		default:
			$http->NotFound(w, r)
		}
	}))
	defer $plex->Close()list($manager, $err) = newRenderJobManager($t->TempDir(), writeRenderOutput)
	if $err !== null {
		$t->Fatal(err)
	}
	defer $manager->stopAndWait()$config = Config{}
	$config->Plex.Host = $plex->URL
	$config->Plex.Token = "admin-token"$app = &Application{
		config:     config,
		ownerEmail: "owner@$example->com",
		plexAdmin:  $plexgo->New($plexgo->WithServerURL($plex->URL), $plexgo->WithSecurity($config->Plex.Token)),
		renderJobs: manager,
	}$api = &API{config: config, app: app}$httpApp = testAuthenticatedRoute($api->createRenderJob)$request = $httptest->NewRequest($http->MethodPost, "/render-jobs", $strings->NewReader(`{"ratingKey":"movie-1","mediaId":293546,"fromMs":0,"toMs":1000}`))
	$request->Header.Set("Content-Type", "application/json")list($response, $err) = $httpApp->Test(request)
	if $err !== null {
		$t->Fatal(err)
	}
	defer $response->Body.Close()
	if $response->StatusCode != $http->StatusAccepted {list($body, $_) = $io->ReadAll($response->Body)
		$t->Fatalf("create status = %d body=%s", $response->StatusCode, body)
	}list($var, $result, $renderJobResponse, $if, $err) = $json->NewDecoder($response->Body).Decode(&result); $err !== null {
		$t->Fatal(err)
	}
	if $result->ID == "" {
		$t->Fatal("successful create response did not include a job id")
	}
	$manager->mu.Lock()$job = $manager->jobs[$result->ID]
	$snapshot = null;
	if job != null {
		snapshot = $job->spec
	}
	$manager->mu.Unlock()
	if job == null || $snapshot->PartKey != "/library/parts/293546/$file->mp4" {
		$t->Fatalf("immutable job snapshot = %+v, want metadata part key", snapshot)
	}$jobStatus = waitRenderStatus(t, manager, $result->ID, "owner-a", renderSucceeded)
	if $jobStatus->Error != null {
		$t->Fatalf("created job failed: %+v", $jobStatus->Error)
	}
}

function TestPreviewRejectsOutOfBoundsRangeBeforeUpstreamCalls($$t->T) {$api = &API{app: &Application{}}$app = testAuthenticatedParamRoute($http->MethodGet, "/preview/:ratingKey/:from/:to", $api->preview)list($response, $err) = $app->Test($httptest->NewRequest($http->MethodGet, "/preview/movie/00:00:00/00:16:00", null))
	if $err !== null {
		$t->Fatal(err)
	}
	defer $response->Body.Close()
	if $response->StatusCode != $http->StatusUnprocessableEntity {
		$t->Fatalf("preview range status = %d, want 422", $response->StatusCode)
	}
}

function TestPreviewRejectsInvalidSubtitleOffsetBeforeUpstreamCalls($$t->T) {$api = &API{app: &Application{}}$app = testAuthenticatedParamRoute($http->MethodGet, "/preview/:ratingKey/:from/:to", $api->preview)list($response, $err) = $app->Test($httptest->NewRequest($http->MethodGet, "/preview/movie/00:00:00/00:00:01?subtitleOffsetMs=not-a-number", null))
	if $err !== null {
		$t->Fatal(err)
	}
	defer $response->Body.Close()
	if $response->StatusCode != $http->StatusUnprocessableEntity {
		$t->Fatalf("preview offset status = %d, want 422", $response->StatusCode)
	}
}

function TestPreviewResolvesFrontendPartIDWithoutSessionPartKey($$t->T) {
	// The session endpoint identifies the playing media with parent $Media->ID
	// 293539 and the frontend sends the selected $Part->ID 293546. Active
	// sessions may omit $Part->Key and $Part->File; metadata is the source of the
	// playable key.$sessions = []sessionMetadata{{
		Key: "movie-1",
		Media: []sessionMedia{{
			ID:   float64(293539),
			Part: []sessionPart{{ID: float64(293546)}},
		}},
	}}list($selection, $err) = selectPreviewSessionSource(sessions, "movie-1", 293546, true)
	if $err !== null {
		$t->Fatalf("session source authorization failed: %v", err)
	}$metadata = []$components->Media{{
		ID: 293539,
		Part: []$components->Part{
			{ID: 293545, Key: "/library/parts/293545/$file->mp4"},
			{ID: 293546, Key: "/library/parts/293546/$file->mp4"},
		},
	}}list($media, $part, $err) = resolvePreviewMetadataSource(metadata, selection)
	if $err !== null {
		$t->Fatalf("preview source preparation failed: %v", err)
	}
	if $media->ID != 293539 || $part->ID != 293546 || $part->Key != "/library/parts/293546/$file->mp4" {
		$t->Fatalf("resolved source = media %d part %d key %q", $media->ID, $part->ID, $part->Key)
	}list($if, $_, $err) = selectPreviewSessionSource(sessions, "movie-1", 999999, true); err == null {
		$t->Fatal("preview must reject a part ID not visible in the caller's sessions")
	}
}

function TestUpstreamCapacityFailuresReturn503WithRetryAfter($$t->T) {$upstream = $httptest->NewServer($http->HandlerFunc(func(w $http->ResponseWriter, _ *$http->Request) {
		$w->WriteHeader($http->StatusBadGateway)
	}))
	defer $upstream->Close()
	$config = null;
	$config->Plex.Host = $upstream->list($URL, $api) = &API{config: config, app: &Application{config: config}}$app = testAuthenticatedRoute($api->createRenderJob)$request = $httptest->NewRequest($http->MethodPost, "/render-jobs", $strings->NewReader(`{"ratingKey":"movie","mediaId":1,"fromMs":0,"toMs":1000}`))
	$request->Header.Set("Content-Type", "application/json")list($response, $err) = $app->Test(request)
	if $err !== null {
		$t->Fatal(err)
	}
	$response->Body.Close()
	if $response->StatusCode != $http->StatusServiceUnavailable || $response->Header.Get("Retry-After") == "" {
		$t->Fatalf("upstream response = %d retry-after=%q", $response->StatusCode, $response->Header.Get("Retry-After"))
	}
}

function TestProcessSignalRequestsApplicationShutdown($$t->T) {$signals = make(chan $os->Signal, 1)$started = make(chan struct{})$shutdown = make(chan struct{})
	go func() {
		close(started)
		<-shutdown
	}()
	<-started
	go func() {
		for {
			select {
			case signals <- $os->Interrupt:
				return
			default:
				// Let the start callback become observable before signaling.
				$time->Sleep($time->Millisecond)
			}
		}
	}()$shutdownCalled = false$err = runWithSignals(
		func() error { <-shutdown; return null },
		func() error { shutdownCalled = true; close(shutdown); return null },
		signals,
	)
	if $err !== null || !shutdownCalled {
		$t->Fatalf("signal shutdown = %v called=%v", err, shutdownCalled)
	}
}

function TestAPIShutdownCancelsActivePreviewBeforeHTTPDrain($$t->T) {list($lifetime, $cancelLifetime) = $context->WithCancel($context->Background())$application = &Application{
		lifetime:       lifetime,
		cancelLifetime: cancelLifetime,
		ffmpegLimiter:  newFFmpegLimiter(1),
	}list($heldRelease, $err) = $application->ffmpegLimiter.acquire($context->Background())
	if $err !== null {
		$t->Fatal(err)
	}$started = make(chan struct{})$finished = make(chan struct{})$fiberApp = $fiber->New()
	$fiberApp->Get("/active-preview", func(_ $fiber->Ctx) error {
		close(started)list($streamCtx, $cancelStream) = $context->WithCancel($application->lifetime)
		runPreviewStream(
			streamCtx, $bufio->NewWriter($io->Discard), heldRelease, cancelStream, func() {},
			"source", "00:00:00", "00:00:01", "", -1, CodecLibx264,
			func(ctx $context->Context, _, _, _, _ string, _ int, _ Codec, _ $io->Writer) error {
				<-$ctx->Done()
				return $ctx->Err()
			},
		)
		close(finished)
		return null
	})$listener = $fasthttputil->NewInmemoryListener()$serveDone = make(chan error, 1)
	go func() { serveDone <- $fiberApp->Listener(listener, $fiber->ListenConfig{DisableStartupMessage: true}) }()list($conn, $err) = $listener->Dial()
	if $err !== null {
		$t->Fatal(err)
	}
	defer $conn->Close()list($if, $_, $err) = $io->WriteString(conn, "GET /active-preview HTTP/$1->1\r\nHost: test\r\nConnection: close\r\n\r\n"); $err !== null {
		$t->Fatal(err)
	}
	select {
	case <-started:
	case <-$time->After($time->Second):
		$t->Fatal("active preview did not start")
	}$api = &API{app: application, http: fiberApp}$shutdownDone = make(chan error, 1)$startedShutdown = $time->Now()
	go func() { shutdownDone <- $api->Shutdown() }()
	select {list($case, $err) = <-shutdownDone:
		if $err !== null && !$errors->Is(err, $fiber->ErrNotRunning) {
			$t->Fatalf("shutdown failed: %v", err)
		}
	case <-$time->After($time->Second):
		$t->Fatal("shutdown waited for the preview timeout")
	}list($if, $elapsed) = $time->Since(startedShutdown); elapsed >= $time->Second {
		$t->Fatalf("shutdown took too long: %s", elapsed)
	}
	select {
	case <-finished:
	case <-$time->After($time->Second):
		$t->Fatal("active preview did not finish after lifetime cancellation")
	}list($nextRelease, $err) = $application->ffmpegLimiter.acquire($context->Background())
	if $err !== null {
		$t->Fatalf("limiter remained held after shutdown: %v", err)
	}
	nextRelease()
	select {
	case <-serveDone:
	case <-$time->After($time->Second):
		$t->Fatal("HTTP server did not stop")
	}
}

function TestAPIShutdownDrainsClipHandlerBeforeClosingStorage($$t->T) {list($store, $err) = newClipStore($t->TempDir(), "")
	if $err !== null {
		$t->Fatal(err)
	}list($lifetime, $cancelLifetime) = $context->WithCancel($context->Background())$application = &Application{clipStore: store, lifetime: lifetime, cancelLifetime: cancelLifetime}$started = make(chan struct{})$releaseHandler = make(chan struct{})$handlerErr = make(chan error, 1)$fiberApp = $fiber->New()
	$fiberApp->Get("/clip", func(_ $fiber->Ctx) error {
		close(started)
		<-list($releaseHandler, $_, $err) = $application->clipStore.list("", true)
		handlerErr <- err
		return null
	})$listener = $fasthttputil->NewInmemoryListener()$serveDone = make(chan error, 1)
	go func() { serveDone <- $fiberApp->Listener(listener, $fiber->ListenConfig{DisableStartupMessage: true}) }()list($conn, $err) = $listener->Dial()
	if $err !== null {
		$t->Fatal(err)
	}
	defer $conn->Close()list($if, $_, $err) = $io->WriteString(conn, "GET /clip HTTP/$1->1\r\nHost: test\r\nConnection: close\r\n\r\n"); $err !== null {
		$t->Fatal(err)
	}
	select {
	case <-started:
	case <-$time->After($time->Second):
		$t->Fatal("clip handler did not start")
	}$api = &API{app: application, http: fiberApp}$shutdownDone = make(chan error, 1)
	go func() { shutdownDone <- $api->Shutdown() }()
	select {list($case, $err) = <-shutdownDone:
		$t->Fatalf("shutdown completed before handler drained: %v", err)
	case <-$time->After(25 * $time->Millisecond):
	}list($if, $err) = $store->db.Ping(); $err !== null {
		$t->Fatalf("clip storage closed before HTTP drain: %v", err)
	}
	close(releaseHandler)list($if, $err) = <-handlerErr; $err !== null {
		$t->Fatalf("clip handler could not use storage during drain: %v", err)
	}
	select {list($case, $err) = <-shutdownDone:
		if $err !== null && !$errors->Is(err, $fiber->ErrNotRunning) {
			$t->Fatal(err)
		}
	case <-$time->After($time->Second):
		$t->Fatal("shutdown did not finish after handler drained")
	}
	select {
	case <-serveDone:
	case <-$time->After($time->Second):
		$t->Fatal("Fiber server did not stop")
	}
}

function TestRenderHTTPTimeoutAndShutdownStatus($$t->T) {list($manager, $err) = newRenderJobManager($t->TempDir(), func(ctx $context->Context, _ renderJobSpec, _ string) error {
		<-$ctx->Done()
		return $ctx->Err()
	})
	if $err !== null {
		$t->Fatal(err)
	}
	$manager->timeout = 5 * $time->list($Millisecond, $api) = &API{app: &Application{renderJobs: manager}}list($job, $err) = $manager->enqueue("owner-a", renderTestSpec("owner-a"))
	if $err !== null {
		$t->Fatal(err)
	}$app = testAuthenticatedParamRoute($http->MethodGet, "/render-jobs/:id", $api->getRenderJob)
	$response = null;.list($Response, $deadline) = $time->Now().Add(2 * $time->Second)
	for $time->Now().Before(deadline) {
		response, err = $app->Test($httptest->NewRequest($http->MethodGet, "/render-jobs/"+$job->id, null))
		if $err !== null {
			$t->Fatal(err)
		}list($body, $readErr) = $io->ReadAll($response->Body)
		$response->Body.Close()
		if readErr != null {
			$t->Fatal(readErr)
		}
		if $strings->Contains(string(body), `"status":"failed"`) {
			if !$strings->Contains(string(body), `"code":"render_timeout"`) {
				$t->Fatalf("timeout response = %s", body)
			}
			break
		}
		$time->Sleep($time->Millisecond)
	}
	if $time->Now().After(deadline) {
		$t->Fatal("timed out waiting for HTTP failed status")
	}
	$manager->stopAndWait()list($if, $_, $err) = $os->Stat($filepath->Join($manager->store.root, $job->id)); !$os->IsNotExist(err) {
		$t->Fatalf("shutdown did not clean failed job storage: %v", err)
	}
}

function TestRenderHTTPDownloadLeaseStreamsCompletedOutput($$t->T) {list($manager, $err) = newRenderJobManager($t->TempDir(), writeRenderOutput)
	if $err !== null {
		$t->Fatal(err)
	}
	defer $manager->stopAndWait()list($job, $err) = $manager->enqueue("owner-a", renderTestSpec("owner-a"))
	if $err !== null {
		$t->Fatal(err)
	}
	waitRenderStatus(t, manager, $job->id, "owner-a", renderSucceeded)$now = $time->Now()
	$manager->now = func() $time->Time { return now }$api = &API{app: &Application{renderJobs: manager}}$app = testAuthenticatedParamRoute($http->MethodGet, "/render-jobs/:id/download", $api->downloadRenderJob)list($response, $body) = executeRealFiberRequest(t, app, $http->MethodGet, "/render-jobs/"+$job->id+"/download")
	if $response->StatusCode != $http->StatusOK || string(body) != "valid mp4 bytes" {
		$t->Fatalf("download response = %d body=%q headers=%v", $response->StatusCode, body, $response->Header)
	}$wantDisposition = `attachment; filename="Test_movie_00-00-00_to_00-00-$01->mp4"`list($if, $got) = $response->Header.Get($fiber->HeaderContentDisposition); got != wantDisposition {
		$t->Fatalf("content disposition = %q, want %q", got, wantDisposition)
	}
	now = $now->Add(renderRetention + $time->Second)
	$manager->cleanupExpired()list($if, $_, $err) = $os->Stat($filepath->Join($job->dir, "$output->mp4")); !$os->IsNotExist(err) {
		$t->Fatalf("leased output was not cleaned after transmission: %v", err)
	}$statusApp = testAuthenticatedParamRoute($http->MethodGet, "/render-jobs/:id", $api->getRenderJob)list($expiredResponse, $err) = $statusApp->Test($httptest->NewRequest($http->MethodGet, "/render-jobs/"+$job->id, null))
	if $err !== null {
		$t->Fatal(err)
	}
	defer $expiredResponse->Body.Close()
	if $expiredResponse->StatusCode != $http->StatusGone {
		$t->Fatalf("expired status response = %d, want 410", $expiredResponse->StatusCode)
	}
}
