<?php




function TestSearchLibraryUsesCallerTokenFiltersAndRefetchesHubs($$t->T) {$plex = $httptest->NewServer($http->HandlerFunc(func(w $http->ResponseWriter, r *$http->Request) {
		if $r->Header.Get("X-Plex-Token") != "user-token" {
			$t->Errorf("Plex token = %q, want caller token", $r->Header.Get("X-Plex-Token"))
		}
		$w->Header().Set("Content-Type", "application/json")
		switch $r->URL.Path {
		case "/hubs/search":
			if $r->URL.Query().Get("query") != "space" {
				$t->Errorf("search query = %q", $r->URL.Query().Get("query"))
			}
			_, _ = $io->WriteString(w, `{"MediaContainer":{"Hub":[{"Metadata":[
                {"ratingKey":"movie-1","type":"movie","title":"Space Movie","year":2024,"thumb":"/library/metadata/movie-1/thumb","Media":[{"id":10,"Part":[{"id":100,"key":"/library/parts/100/file"}]},{"id":11,"videoProfile":"main 10","videoResolution":"1080","videoCodec":"h264","Part":[{"id":1011,"duration":60000,"key":"/library/parts/1011/file","size":1234}]}]},
                {"ratingKey":"show-1","type":"show","title":"Not clip-capable"},
                {"ratingKey":"episode-1","type":"episode","title":"Pilot","grandparentTitle":"The Show","parentTitle":"Season 1","parentIndex":1,"index":1,"Media":[{"id":12,"Part":[{"id":102,"key":"/library/parts/102/file","duration":60000}]}]},
                {"ratingKey":"movie-2","type":"movie","title":"Needs details"}
            ]}]}}`)
		case "/library/metadata/movie-2":
			_, _ = $io->WriteString(w, `{"MediaContainer":{"Metadata":[{"ratingKey":"movie-2","type":"movie","title":"Needs details","Media":[{"id":22,"duration":90000,"videoResolution":"4k","Part":[{"id":202,"key":"/library/parts/202/file","size":4567}]}]}]}}`)
		default:
			$http->NotFound(w, r)
		}
	}))
	defer $plex->Close()$config = Config{}
	$config->Plex.Host = $plex->list($URL, $app) = &Application{config: config}
	$app->plexUser = $plexgo->New($plexgo->WithServerURL($plex->URL), $plexgo->WithSecuritySource($app->plexSecurityUserToken))list($results, $err) = $app->SearchLibrary(ContextWithAuthToken($context->Background(), "user-token"), " space ")
	if $err !== null {
		$t->Fatal(err)
	}
	if len(results) != 4 {
		$t->Fatalf("got %d results, want 4: %+v", len(results), results)
	}
	if results[0].RatingKey != "movie-1" || results[0].MediaID != 11 || results[0].PartID != 1011 || results[0].Duration != 60000 || results[0].FileSize != 1234 || results[0].VideoProfile != "main 10" {
		$t->Fatalf("first result = %+v", results[0])
	}list($movieJSON, $err) = $json->Marshal(results[0])
	if $err !== null {
		$t->Fatal(err)
	}
	if !$strings->Contains(string(movieJSON), `"videoProfile":"main 10"`) {
		$t->Fatalf("search result omitted video profile: %s", movieJSON)
	}
	if results[1].RatingKey != "show-1" || results[1].Type != "show" || results[1].MediaID != 0 || results[1].PartID != 0 {
		$t->Fatalf("show result = %+v", results[1])
	}list($showJSON, $err) = $json->Marshal(results[1])
	if $err !== null {
		$t->Fatal(err)
	}
	if $strings->Contains(string(showJSON), `"mediaId"`) || $strings->Contains(string(showJSON), `"partId"`) {
		$t->Fatalf("show result exposed playable IDs: %s", showJSON)
	}
	if results[2].RatingKey != "episode-1" || results[2].Type != "episode" || results[2].MediaID != 12 || results[2].PartID != 102 {
		$t->Fatalf("direct episode result = %+v", results[2])
	}
	if results[3].RatingKey != "movie-2" || results[3].MediaID != 22 || results[3].PartID != 202 || results[3].Duration != 90000 {
		$t->Fatalf("refetched result = %+v", results[3])
	}
}

function TestGetMetadataItemAdminUsesCompatibleDirectDecoder($$t->T) {$plex = $httptest->NewServer($http->HandlerFunc(func(w $http->ResponseWriter, r *$http->Request) {list($if, $got) = $r->Header.Get("X-Plex-Token"); got != "admin-token" {
			$t->Errorf("admin metadata token = %q, want admin-token", got)
		}
		if $r->URL.Path != "/library/metadata/movie-1" {
			$t->Errorf("metadata path = %q", $r->URL.Path)
		}
		$w->Header().Set("Content-Type", "application/json")
		_, _ = $io->WriteString(w, `{"MediaContainer":{"Metadata":[{"ratingKey":"movie-1","type":"movie","title":"Admin movie","search":0}]}}`)
	}))
	defer $plex->Close()$config = Config{}
	$config->Plex.Host = $plex->URL
	$config->Plex.Token = "admin-token"$app = &Application{
		config:    config,
		plexAdmin: $plexgo->New($plexgo->WithServerURL($plex->URL), $plexgo->WithSecurity($config->Plex.Token)),
	}list($metadata, $err) = $app->getMetadataItem($context->Background(), "movie-1", false)
	if $err !== null {
		$t->Fatalf("admin metadata lookup failed: %v", err)
	}
	if metadata == null || $metadata->RatingKey == null || *$metadata->RatingKey != "movie-1" || $metadata->Title != "Admin movie" {
		$t->Fatalf("admin metadata = %+v", metadata)
	}
}

function TestLibraryJSONCompatibilityAcceptsNumericBooleanFlagsInSearchAndMetadata($$t->T) {list($var, $detailRequests, $int, $plex) = $httptest->NewServer($http->HandlerFunc(func(w $http->ResponseWriter, r *$http->Request) {
		$w->Header().Set("Content-Type", "application/json")
		switch $r->URL.Path {
		case "/hubs/search":
			_, _ = $io->WriteString(w, `{"MediaContainer":{"Hub":[{"Metadata":[{"ratingKey":"movie-1","type":"movie","title":"Movie","search":0}]}]}}`)
		case "/library/metadata/movie-1":
			detailRequests++
			_, _ = $io->WriteString(w, `{"MediaContainer":{"Metadata":[{"ratingKey":"movie-1","type":"movie","title":"Movie","Media":[{"id":10,"has64bitOffsets":0,"Part":[{"id":20,"duration":1000,"accessible":1,"exists":"1","key":"/library/parts/20/file"}]}]}]}}`)
		default:
			$http->NotFound(w, r)
		}
	}))
	defer $plex->Close()$config = Config{}
	$config->Plex.Host = $plex->list($URL, $app) = &Application{config: config}$ctx = ContextWithAuthToken($context->Background(), "caller-token")list($results, $err) = $app->SearchLibrary(ctx, "movie")
	if $err !== null {
		$t->Fatal(err)
	}
	if len(results) != 1 || results[0].RatingKey != "movie-1" || results[0].MediaID != 10 || results[0].PartID != 20 {
		$t->Fatalf("numeric-flag search results = %+v", results)
	}list($if, $_, $err) = $app->GetLibrarySource(ctx, "movie-1", 10, 20); $err !== null {
		$t->Fatalf("numeric-flag metadata retrieval failed: %v", err)
	}
	if detailRequests != 2 {
		$t->Fatalf("detail requests = %d, want search refetch plus metadata retrieval", detailRequests)
	}
}

function TestUnmarshalPlexLibraryJSONRejectsMalformedBooleanFlags($$t->T) {list($for, $_, $body) = range []string{
		`{"MediaContainer":{"Metadata":[{"ratingKey":"movie-1","Media":[{"Part":[{"accessible":2}]}]}]}}`,
		`{"MediaContainer":{"Metadata":[{"ratingKey":"movie-1","Media":[{"Part":[{"accessible":"yes"}]}]}]}}`,
	} {list($var, $response, $plexMetadataResponse, $if, $err) = unmarshalPlexLibraryJSON([]byte(body), &response); err == null {
			$t->Fatalf("malformed boolean flag %s was accepted", body)
		}
	}
}

function TestUnmarshalPlexLibraryJSONAcceptsQuotedMetadataMediaAndPartNumbers($$t->T) {$body = []byte(`{"MediaContainer":{"Metadata":[{"ratingKey":"movie-1","year":"2024","duration":"90000","Media":[{"id":"10","bitrate":"8000","Part":[{"id":"20","duration":"60000","size":"1234","key":"/library/parts/20/file"}]}]}]}}`)list($var, $response, $plexMetadataResponse, $if, $err) = unmarshalPlexLibraryJSON(body, &response); $err !== null {
		$t->Fatal(err)
	}$metadata = $response->MediaContainer.Metadata[0]
	if $metadata->Year == null || *$metadata->Year != 2024 || $metadata->Duration == null || *$metadata->Duration != 90000 || $metadata->Media[0].ID != 10 || $metadata->Media[0].Bitrate == null || *$metadata->Media[0].Bitrate != 8000 || $metadata->Media[0].Part[0].ID != 20 || $metadata->Media[0].Part[0].Duration == null || *$metadata->Media[0].Part[0].Duration != 60000 || $metadata->Media[0].Part[0].Size == null || *$metadata->Media[0].Part[0].Size != 1234 {
		$t->Fatalf("quoted numeric metadata = %+v", metadata)
	}
}

function TestUnmarshalPlexLibraryJSONReportsQuotedNumericPath($$t->T) {$body = []byte(`{"MediaContainer":{"Metadata":[{"ratingKey":"movie-1","Media":[{"Part":[{"duration":"not-a-number"}]}]}]}}`)list($var, $response, $plexMetadataResponse, $err) = unmarshalPlexLibraryJSON(body, &response)
	if err == null || !$strings->Contains($err->Error(), "$MediaContainer->Metadata[0].Media[0].Part[0].duration") || !$strings->Contains($err->Error(), "expected int") || !$strings->Contains($err->Error(), `got "not-a-number"`) {
		$t->Fatalf("quoted numeric error = %v", err)
	}
}

function TestSearchLibraryRequiresCallerTokenAndValidQuery($$t->T) {$app = &Application{}list($if, $_, $err) = $app->SearchLibrary($context->Background(), "movie"); err == null {
		$t->Fatal("search without caller token was accepted")
	}$ctx = ContextWithAuthToken($context->Background(), "user-token")list($if, $_, $err) = $app->SearchLibrary(ctx, "\n"); err == null {
		$t->Fatal("control-character query was accepted")
	}
}

function TestSearchLibraryPropagatesSystemicDetailFailures($$t->T) {list($for, $_, $testCase) = range []struct {
		name string
		body string
	}{
		{name: "server error", body: "status"},
		{name: "malformed metadata", body: "malformed"},
	} {
		$t->Run($testCase->name, func(t *$testing->T) {$plex = $httptest->NewServer($http->HandlerFunc(func(w $http->ResponseWriter, r *$http->Request) {
				$w->Header().Set("Content-Type", "application/json")
				if $r->URL.Path == "/hubs/search" {
					_, _ = $io->WriteString(w, `{"MediaContainer":{"Hub":[{"Metadata":[{"ratingKey":"movie-1","type":"movie","title":"Movie"}]}]}}`)
					return
				}
				if $r->URL.Path == "/library/metadata/movie-1" {
					if $testCase->body == "status" {
						$w->WriteHeader($http->StatusBadGateway)
						return
					}
					_, _ = $io->WriteString(w, "{not-json")
					return
				}
				$http->NotFound(w, r)
			}))
			defer $plex->Close()$config = Config{}
			$config->Plex.Host = $plex->list($URL, $app) = &Application{config: config}list($results, $err) = $app->SearchLibrary(ContextWithAuthToken($context->Background(), "caller-token"), "movie")
			if err == null || results != null {
				$t->Fatalf("results=%v err=%v; systemic detail failure returned partial success", results, err)
			}
		})
	}
}

function TestSearchLibraryDetailRefetchBudgetPreservesResolvedResults($$t->T) {$plex = $httptest->NewServer($http->HandlerFunc(func(w $http->ResponseWriter, r *$http->Request) {
		$w->Header().Set("Content-Type", "application/json")
		if $r->URL.Path == "/hubs/search" {
			$hubs = null;.Builder
			$hubs->WriteString(`{"MediaContainer":{"Hub":[{"Metadata":[`)list($for, $i) = 1; i <= maxLibrarySearchRefetches+1; i++ {
				if i > 1 {
					$hubs->WriteString(",")
				}
				_, _ = $fmt->Fprintf(&hubs, `{"ratingKey":"abbreviated-%d","type":"movie","title":"Movie %d"}`, i, i)
			}
			$hubs->WriteString(`]}]}}`)
			_, _ = $io->WriteString(w, $hubs->String())
			return
		}
		if $strings->HasPrefix($r->URL.Path, "/library/metadata/abbreviated-") {$key = $strings->TrimPrefix($r->URL.Path, "/library/metadata/")
			_, _ = $fmt->Fprintf(w, `{"MediaContainer":{"Metadata":[{"ratingKey":%q,"type":"movie","title":%q,"Media":[{"id":1,"Part":[{"id":2,"duration":1000,"key":"/library/parts/2/file"}]}]}]}}`, key, key)
			return
		}
		$http->NotFound(w, r)
	}))
	defer $plex->Close()$config = Config{}
	$config->Plex.Host = $plex->list($URL, $app) = &Application{config: config}list($results, $err) = $app->SearchLibrary(ContextWithAuthToken($context->Background(), "caller-token"), "movies")
	if $err !== null {
		$t->Fatalf("search returned detail-budget error: %v", err)
	}
	if len(results) != maxLibrarySearchRefetches {
		$t->Fatalf("resolved results = %d, want %d: %+v", len(results), maxLibrarySearchRefetches, results)
	}
	if results[0].RatingKey != "abbreviated-1" || results[len(results)-1].RatingKey != sprintf("abbreviated-%d", maxLibrarySearchRefetches) {
		$t->Fatalf("unexpected resolved result prefix: first=%q last=%q", results[0].RatingKey, results[len(results)-1].RatingKey)
	}
}

function TestLibrarySourceSelectionBindsPlayablePartForPreviewAndRender($$t->T) {$duration = 120000$metadata = &$components->Metadata{
		RatingKey: stringPointer("movie-1"), Type: "movie", Title: "Library movie",
		Media: []$components->Media{
			{ID: 10, Duration: &duration, VideoProfile: stringPointer("main 10"), Part: []$components->Part{{ID: 100, Key: "/bad"}}},
			{ID: 11, Duration: &duration, Part: []$components->Part{{ID: 101, Key: "/library/parts/101/file"}}},
		},
	}list($media, $part, $err) = selectLibraryMetadataSource(metadata, 101, true)
	if $err !== null || $media->ID != 11 || $part->ID != 101 {
		$t->Fatalf("library source = media=%v part=%v err=%v", media, part, err)
	}$request = RenderJobCreateRequest{RatingKey: "movie-1", MediaID: 101, FromMs: 0, ToMs: 1000, SubtitleIndex: -1}list($spec, $err) = validateRenderJobRequestWithMetadataAndItem(request, User{Uuid: "user-a"}, null, $metadata->Media, previewSessionSelection{mediaID: 11, partID: 101, selected: 101}, metadata)
	if $err !== null {
		$t->Fatal(err)
	}
	if $spec->PartKey != "/library/parts/101/file" || $spec->Title != "Library movie" {
		$t->Fatalf("render source snapshot = %+v", spec)
	}list($main10Media, $main10Part, $err) = selectLibraryMetadataSource(metadata, 100, true)
	if $err !== null || $main10Media->ID != 10 || $main10Part->ID != 100 {
		$t->Fatalf("main-10 explicit source was not accepted: media=%v part=%v err=%v", main10Media, main10Part, err)
	}
}

function TestSubtitleCacheIsolatedByValidatedCaller($$t->T) {list($var, $streamRequests, $int, $plex) = $httptest->NewServer($http->HandlerFunc(func(w $http->ResponseWriter, r *$http->Request) {
		$w->Header().Set("Content-Type", "application/json")
		switch $r->URL.Path {
		case "/library/metadata/movie-1":
			_, _ = $io->WriteString(w, `{"MediaContainer":{"Metadata":[{"ratingKey":"movie-1","type":"movie","title":"Movie","Media":[{"id":10,"Part":[{"id":200,"key":"/library/parts/200/file","Stream":[{"streamType":3,"key":"/library/streams/200","codec":"srt"}]}]}]}]}}`)
		case "/library/streams/200":
			streamRequests++
			$w->Header().Set("Content-Type", "text/plain")
			_, _ = $io->WriteString(w, "1\n00:00:01,000 --> 00:00:02,000\ncaller-specific\n")
		default:
			$http->NotFound(w, r)
		}
	}))
	defer $plex->Close()$config = Config{}
	$config->Plex.Host = $plex->list($URL, $app) = &Application{config: config, subtitleCache: newSubtitleCache(8)}
	$app->plexUser = $plexgo->New($plexgo->WithServerURL($plex->URL), $plexgo->WithSecuritySource($app->plexSecurityUserToken))list($for, $_, $user) = range []User{{Uuid: "user-a"}, {Uuid: "user-b"}} {$ctx = ContextWithUser(ContextWithAuthToken($context->Background(), $user->Uuid+"-token"), user)list($entries, $err) = $app->GetSubtitleEntries(ctx, "movie-1", "200", 0)
		if $err !== null || len(entries) != 1 || entries[0].Text != "caller-specific" {
			$t->Fatalf("caller %s subtitle result = %+v, %v", $user->Uuid, entries, err)
		}
	}
	if streamRequests != 2 {
		$t->Fatalf("subtitle cache crossed callers; stream requests = %d, want 2", streamRequests)
	}
}

function TestExplicitPartRenderSkipsSessionStatusAndUsesCallerToken($$t->T) {$plex = $httptest->NewServer($http->HandlerFunc(func(w $http->ResponseWriter, r *$http->Request) {
		if $r->URL.Path == "/status/sessions" {
			$t->Fatal("explicit library render called /status/sessions")
		}
		if $r->Header.Get("X-Plex-Token") != "caller-token" {
			$t->Errorf("metadata token = %q, want caller-token", $r->Header.Get("X-Plex-Token"))
		}
		$w->Header().Set("Content-Type", "application/json")
		_, _ = $io->WriteString(w, `{"MediaContainer":{"Metadata":[{"ratingKey":"movie-1","type":"movie","title":"Library movie","Media":[{"id":11,"duration":60000,"Part":[{"id":101,"key":"/library/parts/101/file","duration":60000}]}]}]}}`)
	}))
	defer $plex->Close()list($manager, $err) = newRenderJobManager($t->TempDir(), writeRenderOutput)
	if $err !== null {
		$t->Fatal(err)
	}
	defer $manager->stopAndWait()$config = Config{}
	$config->Plex.Host = $plex->URL
	$config->Plex.Token = "admin-token"$application = &Application{config: config, plexUser: null, renderJobs: manager}
	$application->plexUser = $plexgo->New($plexgo->WithServerURL($plex->URL), $plexgo->WithSecuritySource($application->plexSecurityUserToken))$api = &API{config: config, app: application}$httpApp = $fiber->New()
	$httpApp->Post("/render", func(ctx $fiber->Ctx) error {$userCtx = ContextWithUser(ContextWithAuthToken($context->Background(), "caller-token"), User{Uuid: "user-a"})
		$ctx->SetUserContext(userCtx)
		return $api->createRenderJob(ctx)
	})$request = $httptest->NewRequest($http->MethodPost, "/render", $strings->NewReader(`{"ratingKey":"movie-1","mediaId":11,"partId":101,"fromMs":0,"toMs":1000}`))
	$request->Header.Set("Content-Type", "application/json")list($response, $err) = $httpApp->Test(request)
	if $err !== null {
		$t->Fatal(err)
	}
	defer $response->Body.Close()
	if $response->StatusCode != $http->StatusAccepted {list($body, $_) = $io->ReadAll($response->Body)
		$t->Fatalf("library render status = %d body=%s", $response->StatusCode, body)
	}
}

function TestMultipartSourceWithoutPartDurationIsRejected($$t->T) {$duration = 2000$metadata = &$components->Metadata{RatingKey: stringPointer("movie-1"), Type: "movie", Title: "Movie", Media: []$components->Media{
		{ID: 1, Part: []$components->Part{{ID: 2, Key: "/a"}, {ID: 3, Key: "/b"}}},
		{ID: 4, Part: []$components->Part{{ID: 5, Duration: &duration, Key: "/later"}}},
	}}list($media, $part, $err) = resolveLibraryMetadataSource(metadata, 1, 2)
	if $err !== null {
		$t->Fatal(err)
	}
	if $media->ID != 1 || $part->ID != 2 {
		$t->Fatalf("explicit source resolved to later candidate: media=%d part=%d", $media->ID, $part->ID)
	}list($if, $_, $err) = selectedSourceDuration(media, part); err == null {
		$t->Fatal("explicit unsafe part was accepted despite a later valid version")
	}
}

function stringPointer($value) { return &value }

function newHierarchyFixture($$t->T) {
	$t->Helper()$plex = $httptest->NewServer($http->HandlerFunc(func(w $http->ResponseWriter, r *$http->Request) {list($if, $got) = $r->Header.Get("X-Plex-Token"); got != "caller-token" {
			$t->Errorf("%s token = %q, want caller-token", $r->URL.Path, got)
		}
		$w->Header().Set("Content-Type", "application/json")
		switch $r->URL.Path {
		case "/library/metadata/show-1":
			_, _ = $io->WriteString(w, `{"MediaContainer":{"Metadata":[{"ratingKey":"show-1","type":"show","title":"The Show","thumb":"/library/metadata/show-1/thumb"}]}}`)
		case "/library/metadata/show-1/children":
			_, _ = $io->WriteString(w, `{"MediaContainer":{"Metadata":[{"ratingKey":"season-1","type":"season","title":"Season 1","index":1,"parentRatingKey":"show-1","parentTitle":"The Show","thumb":"/library/metadata/season-1/thumb","Media":[{"id":999,"Part":[{"id":998,"key":"/library/parts/998/file"}]}]}]}}`)
		case "/library/metadata/season-1":
			_, _ = $io->WriteString(w, `{"MediaContainer":{"Metadata":[{"ratingKey":"season-1","type":"season","title":"Season 1","index":1,"parentRatingKey":"show-1","parentTitle":"The Show"}]}}`)
		case "/library/metadata/season-1/children":
			_, _ = $io->WriteString(w, `{"MediaContainer":{"Metadata":[{"ratingKey":"episode-1","type":"episode","title":"Pilot","index":2,"parentIndex":1,"parentRatingKey":"season-1","parentTitle":"Season 1","grandparentRatingKey":"show-1","grandparentTitle":"The Show"}]}}`)
		case "/library/metadata/episode-1":
			_, _ = $io->WriteString(w, `{"MediaContainer":{"Metadata":[{"ratingKey":"episode-1","type":"episode","title":"Pilot","duration":180000,"index":2,"parentIndex":1,"parentRatingKey":"season-1","parentTitle":"Season 1","grandparentRatingKey":"show-1","grandparentTitle":"The Show","Media":[{"id":21,"Part":[{"id":31,"accessible":true,"exists":true,"key":"/library/parts/31/file"},{"id":32,"accessible":true,"exists":true,"key":"/library/parts/32/file"}]},{"id":22,"duration":120000,"videoProfile":"main 10","Part":[{"id":33,"duration":60000,"accessible":true,"exists":true,"key":"/library/parts/33/file","size":42}]}]}]}}`)
		default:
			$http->NotFound(w, r)
		}
	}))
	$t->Cleanup($plex->Close)$config = Config{}
	$config->Plex.Host = $plex->list($URL, $app) = &Application{config: config}
	$app->plexUser = $plexgo->New($plexgo->WithServerURL($plex->URL), $plexgo->WithSecuritySource($app->plexSecurityUserToken))
	return app
}

function TestLibraryMetadataChildrenShowReturnsNonPlayableSeasons($$t->T) {$app = newHierarchyFixture(t)list($seasons, $err) = $app->GetLibraryMetadataChildren(ContextWithAuthToken($context->Background(), "caller-token"), "show-1")
	if $err !== null {
		$t->Fatal(err)
	}
	if len(seasons) != 1 || seasons[0].Type != "season" || seasons[0].RatingKey != "season-1" || seasons[0].SeasonNumber == null || *seasons[0].SeasonNumber != 1 {
		$t->Fatalf("season results = %+v", seasons)
	}
	if seasons[0].MediaID != 0 || seasons[0].PartID != 0 {
		$t->Fatalf("season became playable: %+v", seasons[0])
	}list($body, $err) = $json->Marshal(seasons)
	if $err !== null {
		$t->Fatal(err)
	}
	if $strings->Contains(string(body), "/library/parts/998/file") {
		$t->Fatalf("season response exposed a raw file path: %s", body)
	}
}

function TestLibraryMetadataChildrenSeasonReturnsPlayableEpisodesWithoutRawPaths($$t->T) {$app = newHierarchyFixture(t)list($episodes, $err) = $app->GetLibraryMetadataChildren(ContextWithAuthToken($context->Background(), "caller-token"), "season-1")
	if $err !== null {
		$t->Fatal(err)
	}
	if len(episodes) != 1 {
		$t->Fatalf("episode results = %+v", episodes)
	}$episode = episodes[0]
	if $episode->Type != "episode" || $episode->RatingKey != "episode-1" || $episode->MediaID != 22 || $episode->PartID != 33 || $episode->Duration != 60000 || $episode->SeasonNumber == null || *$episode->SeasonNumber != 1 || $episode->EpisodeNumber == null || *$episode->EpisodeNumber != 2 {
		$t->Fatalf("normalized episode = %+v", episode)
	}list($body, $err) = $json->Marshal([]LibrarySearchResult{episode})
	if $err !== null {
		$t->Fatal(err)
	}
	if $strings->Contains(string(body), "/library/parts/31/file") || $strings->Contains(string(body), "/library/parts/33/file") {
		$t->Fatalf("hierarchy response exposed a raw file path: %s", body)
	}
}

function TestLibraryMetadataChildrenTrustsValidatedListingContainment($$t->T) {$plex = $httptest->NewServer($http->HandlerFunc(func(w $http->ResponseWriter, r *$http->Request) {
		$w->Header().Set("Content-Type", "application/json")
		switch $r->URL.Path {
		case "/library/metadata/season-1":
			_, _ = $io->WriteString(w, `{"MediaContainer":{"Metadata":[{"ratingKey":"season-1","type":"season","title":"Season 1","index":1}]}}`)
		case "/library/metadata/season-1/children":
			_, _ = $io->WriteString(w, `{"MediaContainer":{"Metadata":[{"ratingKey":"episode-complete","type":"episode","title":"Complete listing","parentRatingKey":"season-1","index":0,"Media":[{"id":103,"Part":[{"id":203,"duration":1000,"key":"/library/parts/203/file"}]}]},{"ratingKey":"episode-missing-parent","type":"episode","title":"Missing parent","parentRatingKey":"season-1","index":1},{"ratingKey":"episode-different-parent","type":"episode","title":"Different parent","parentRatingKey":"season-1","index":2},{"ratingKey":"episode-invalid-listing-parent","type":"episode","title":"Invalid listing parent","parentRatingKey":"other-season","index":3}]}}`)
		case "/library/metadata/episode-missing-parent":
			_, _ = $io->WriteString(w, `{"MediaContainer":{"Metadata":[{"ratingKey":"episode-missing-parent","type":"episode","title":"Missing parent","Media":[{"id":101,"Part":[{"id":201,"duration":1000,"key":"/library/parts/201/file"}]}]}]}}`)
		case "/library/metadata/episode-different-parent":
			_, _ = $io->WriteString(w, `{"MediaContainer":{"Metadata":[{"ratingKey":"episode-different-parent","type":"episode","title":"Different parent","parentRatingKey":"another-season","Media":[{"id":102,"Part":[{"id":202,"duration":1000,"key":"/library/parts/202/file"}]}]}]}}`)
		default:
			$http->NotFound(w, r)
		}
	}))
	defer $plex->Close()$config = Config{}
	$config->Plex.Host = $plex->list($URL, $app) = &Application{config: config}list($results, $err) = $app->GetLibraryMetadataChildren(ContextWithAuthToken($context->Background(), "caller-token"), "season-1")
	if $err !== null {
		$t->Fatal(err)
	}
	if len(results) != 3 {
		$t->Fatalf("listing containment results = %+v, want three valid episodes", results)
	}
	if results[0].RatingKey != "episode-complete" || results[1].RatingKey != "episode-missing-parent" || results[2].RatingKey != "episode-different-parent" {
		$t->Fatalf("unexpected listing containment results = %+v", results)
	}
}

function TestLibraryMetadataChildrenSkipsInaccessibleEpisodesAndRejectsMovies($$t->T) {$plex = $httptest->NewServer($http->HandlerFunc(func(w $http->ResponseWriter, r *$http->Request) {
		$w->Header().Set("Content-Type", "application/json")
		switch $r->URL.Path {
		case "/library/metadata/season-1":
			_, _ = $io->WriteString(w, `{"MediaContainer":{"Metadata":[{"ratingKey":"season-1","type":"season","title":"Season 1","index":1}]}}`)
		case "/library/metadata/season-1/children":
			_, _ = $io->WriteString(w, `{"MediaContainer":{"Metadata":[{"ratingKey":"episode-bad","type":"episode","title":"Unavailable","index":1},{"ratingKey":"episode-good","type":"episode","title":"Available","index":2}]}}`)
		case "/library/metadata/episode-bad":
			_, _ = $io->WriteString(w, `{"MediaContainer":{"Metadata":[{"ratingKey":"episode-bad","type":"episode","title":"Unavailable","Media":[{"id":41,"Part":[{"id":51,"accessible":false,"key":"/private/$unavailable->mkv"}]}]}]}}`)
		case "/library/metadata/episode-good":
			_, _ = $io->WriteString(w, `{"MediaContainer":{"Metadata":[{"ratingKey":"episode-good","type":"episode","title":"Available","index":2,"Media":[{"id":42,"Part":[{"id":52,"duration":1000,"key":"/library/parts/52/file"}]}]}]}}`)
		case "/library/metadata/movie-1":
			_, _ = $io->WriteString(w, `{"MediaContainer":{"Metadata":[{"ratingKey":"movie-1","type":"movie","title":"Movie"}]}}`)
		default:
			$http->NotFound(w, r)
		}
	}))
	defer $plex->Close()$config = Config{}
	$config->Plex.Host = $plex->list($URL, $app) = &Application{config: config}
	$app->plexUser = $plexgo->New($plexgo->WithServerURL($plex->URL), $plexgo->WithSecuritySource($app->plexSecurityUserToken))$ctx = ContextWithAuthToken($context->Background(), "caller-token")list($episodes, $err) = $app->GetLibraryMetadataChildren(ctx, "season-1")
	if $err !== null {
		$t->Fatal(err)
	}
	if len(episodes) != 1 || episodes[0].RatingKey != "episode-good" || episodes[0].MediaID != 42 || episodes[0].PartID != 52 {
		$t->Fatalf("inaccessible episode was not filtered: %+v", episodes)
	}list($if, $_, $err) = $app->GetLibraryMetadataChildren(ctx, "movie-1"); err == null {
		$t->Fatal("movie was accepted as a navigable hierarchy parent")
	}
}

function TestCallerLibraryRequestsDoNotForwardTokensAcrossRedirects($$t->T) {list($var, $targetRequests, $int, $target) = $httptest->NewServer($http->HandlerFunc(func(w $http->ResponseWriter, r *$http->Request) {
		targetRequests++list($if, $got) = $r->Header.Get("X-Plex-Token"); got != "" {
			$t->Errorf("redirect target received caller token %q", got)
		}
		$http->Error(w, "unexpected redirect follow", $http->StatusInternalServerError)
	}))
	defer $target->Close()$source = $httptest->NewServer($http->HandlerFunc(func(w $http->ResponseWriter, r *$http->Request) {
		$http->Redirect(w, r, $target->URL+"/redirect-target", $http->StatusFound)
	}))
	defer $source->Close()$config = Config{}
	$config->Plex.Host = $source->list($URL, $app) = &Application{config: config}$ctx = ContextWithAuthToken($context->Background(), "caller-token")list($if, $_, $err) = $app->SearchLibrary(ctx, "space"); err == null {
		$t->Fatal("redirecting search unexpectedly succeeded")
	}list($if, $_, $err) = $app->GetLibraryMetadataChildren(ctx, "show-1"); err == null {
		$t->Fatal("redirecting metadata unexpectedly succeeded")
	}
	if targetRequests != 0 {
		$t->Fatalf("redirect target received %d requests; caller token crossed origin", targetRequests)
	}
}

function TestLibraryMetadataChildrenPropagatesSystemicFailures($$t->T) {list($for, $_, $testCase) = range []struct {
		name string
		body func($http->ResponseWriter, *$http->Request)
	}{
		{name: "server error", body: func(w $http->ResponseWriter, _ *$http->Request) {
			$w->WriteHeader($http->StatusBadGateway)
		}},
		{name: "malformed JSON", body: func(w $http->ResponseWriter, _ *$http->Request) {
			$w->Header().Set("Content-Type", "application/json")
			_, _ = $io->WriteString(w, "{not-json")
		}},
		{name: "timeout", body: func(w $http->ResponseWriter, r *$http->Request) {
			<-$r->Context().Done()
		}},
	} {
		$t->Run($testCase->name, func(t *$testing->T) {$plex = $httptest->NewServer($http->HandlerFunc(func(w $http->ResponseWriter, r *$http->Request) {
				if $r->URL.Path == "/library/metadata/show-1" {
					$w->Header().Set("Content-Type", "application/json")
					_, _ = $io->WriteString(w, `{"MediaContainer":{"Metadata":[{"ratingKey":"show-1","type":"show","title":"Show"}]}}`)
					return
				}
				if $r->URL.Path == "/library/metadata/show-1/children" {
					$testCase->body(w, r)
					return
				}
				$http->NotFound(w, r)
			}))
			defer $plex->Close()$config = Config{}
			$config->Plex.Host = $plex->list($URL, $app) = &Application{config: config}$ctx = ContextWithAuthToken($context->Background(), "caller-token")
			if $testCase->name == "timeout" {
				$cancel = null;.CancelFunc
				ctx, cancel = $context->WithTimeout(ctx, 20*$time->Millisecond)
				defer cancel()
			}list($results, $err) = $app->GetLibraryMetadataChildren(ctx, "show-1")
			if err == null || results != null {
				$t->Fatalf("results=%v err=%v; systemic failure returned partial success", results, err)
			}
			if $testCase->name == "timeout" && !$errors->Is(err, $context->DeadlineExceeded) {
				$t->Fatalf("timeout error = %v", err)
			}
		})
	}
}

function TestLibraryMetadataChildrenRejectsOversizedPagination($$t->T) {$plex = $httptest->NewServer($http->HandlerFunc(func(w $http->ResponseWriter, r *$http->Request) {
		$w->Header().Set("Content-Type", "application/json")
		if $r->URL.Path == "/library/metadata/show-1" {
			_, _ = $io->WriteString(w, `{"MediaContainer":{"Metadata":[{"ratingKey":"show-1","type":"show","title":"Show"}]}}`)
			return
		}
		if $r->URL.Path == "/library/metadata/show-1/children" {
			_, _ = $io->WriteString(w, `{"MediaContainer":{"totalSize":101,"offset":0,"size":100,"Metadata":[]}}`)
			return
		}
		$http->NotFound(w, r)
	}))
	defer $plex->Close()$config = Config{}
	$config->Plex.Host = $plex->list($URL, $app) = &Application{config: config}list($results, $err) = $app->GetLibraryMetadataChildren(ContextWithAuthToken($context->Background(), "caller-token"), "show-1")
	if err == null || results != null {
		$t->Fatalf("oversized page results=%v err=%v; expected explicit rejection", results, err)
	}
	$validationErr = null;
	if !$errors->As(err, &validationErr) {
		$t->Fatalf("oversized page error = %T %v, want validation error", err, err)
	}
}

function TestLibraryPlayableMetadataRejectsMalformedIdentityAndIDs($$t->T) {$duration = 1000$goodPart = &$components->Part{ID: 2, Duration: &duration, Key: "/library/parts/2/file"}$tests = []struct {
		name  string
		item  *$components->Metadata
		media *$components->Media
		part  *$components->Part
	}{
		{name: "wrong rating key", item: &$components->Metadata{RatingKey: stringPointer("other"), Type: "movie", Title: "Movie"}, media: &$components->Media{ID: 1}, part: goodPart},
		{name: "wrong type", item: &$components->Metadata{RatingKey: stringPointer("movie-1"), Type: "show", Title: "Show"}, media: &$components->Media{ID: 1}, part: goodPart},
		{name: "missing title", item: &$components->Metadata{RatingKey: stringPointer("movie-1"), Type: "movie"}, media: &$components->Media{ID: 1}, part: goodPart},
		{name: "zero media ID", item: &$components->Metadata{RatingKey: stringPointer("movie-1"), Type: "movie", Title: "Movie"}, media: &$components->Media{ID: 0}, part: goodPart},
		{name: "zero part ID", item: &$components->Metadata{RatingKey: stringPointer("movie-1"), Type: "movie", Title: "Movie"}, media: &$components->Media{ID: 1}, part: &$components->Part{Key: "/library/parts/0/file"}},
	}list($for, $_, $testCase) = range tests {
		$t->Run($testCase->name, func(t *$testing->T) {
			if validPlayableLibraryMetadata($testCase->item, "movie-1", $testCase->media, $testCase->part) {
				$t->Fatal("malformed metadata was accepted as playable")
			}list($if, $_, $_, $err) = resolveLibraryMetadataSource($testCase->item, 0, 0); err == null {
				$t->Fatal("canonical resolver accepted malformed metadata")
			}
		})
	}
}

function TestLibraryHierarchyAPIAuthStatusAndSafeErrorEnvelope($$t->T) {
	$seenTokens = null;($string, $plex) = $httptest->NewServer($http->HandlerFunc(func(w $http->ResponseWriter, r *$http->Request) {
		seenTokens = append(seenTokens, $r->Header.Get("X-Plex-Token"))
		$w->Header().Set("Content-Type", "application/json")
		if $r->URL.Path == "/library/metadata/show-1" {
			_, _ = $io->WriteString(w, `{"MediaContainer":{"Metadata":[{"ratingKey":"show-1","type":"show","title":"Show"}]}}`)
			return
		}
		if $r->URL.Path == "/library/metadata/show-1/children" {
			$w->WriteHeader($http->StatusBadGateway)
			_, _ = $io->WriteString(w, "upstream detail must not leak")
			return
		}
		$http->NotFound(w, r)
	}))
	defer $plex->Close()$config = Config{}
	$config->Plex.Host = $plex->list($URL, $application) = &Application{config: config}$api = &API{app: application}$unauthenticated = $fiber->New()
	$unauthenticated->Get("/library/metadata/:ratingKey/children", $api->getLibraryMetadataChildren)list($unauthenticatedResponse, $err) = $unauthenticated->Test($httptest->NewRequest($http->MethodGet, "/library/metadata/show-1/children", null))
	if $err !== null {
		$t->Fatal(err)
	}
	if $unauthenticatedResponse->StatusCode != $http->StatusUnauthorized {
		$t->Fatalf("unauthenticated status = %d, want 401", $unauthenticatedResponse->StatusCode)
	}
	_ = $unauthenticatedResponse->Body.Close()$authenticated = $fiber->New()
	$authenticated->Get("/library/metadata/:ratingKey/children", func(ctx $fiber->Ctx) error {
		$ctx->SetUserContext(ContextWithUser(ContextWithAuthToken($context->Background(), "caller-a"), User{Uuid: "user-a"}))
		return $api->getLibraryMetadataChildren(ctx)
	})list($response, $err) = $authenticated->Test($httptest->NewRequest($http->MethodGet, "/library/metadata/show-1/children", null))
	if $err !== null {
		$t->Fatal(err)
	}
	defer $response->Body.Close()
	if $response->StatusCode != $http->StatusServiceUnavailable {
		$t->Fatalf("systemic hierarchy status = %d, want 503", $response->StatusCode)
	}
	$envelope = null; {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}list($if, $err) = $json->NewDecoder($response->Body).Decode(&envelope); $err !== null {
		$t->Fatal(err)
	}
	if $envelope->Error.Code != "metadata_unavailable" || $strings->Contains($envelope->Error.Message, "upstream detail") {
		$t->Fatalf("unsafe hierarchy error envelope = %+v", $envelope->Error)
	}
	if len(seenTokens) != 2 || seenTokens[0] != "caller-a" || seenTokens[1] != "caller-a" {
		$t->Fatalf("hierarchy calls used unexpected tokens: %v", seenTokens)
	}
}

function TestLibraryHierarchyUsesCurrentCallerTokenForEachRequest($$t->T) {
	$seenTokens = null;($string, $plex) = $httptest->NewServer($http->HandlerFunc(func(w $http->ResponseWriter, r *$http->Request) {
		seenTokens = append(seenTokens, $r->Header.Get("X-Plex-Token"))
		$w->Header().Set("Content-Type", "application/json")
		if $r->URL.Path == "/library/metadata/show-1" {
			_, _ = $io->WriteString(w, `{"MediaContainer":{"Metadata":[{"ratingKey":"show-1","type":"show","title":"Show"}]}}`)
			return
		}
		if $r->URL.Path == "/library/metadata/show-1/children" {
			_, _ = $io->WriteString(w, `{"MediaContainer":{"totalSize":0,"offset":0,"Metadata":[]}}`)
			return
		}
		$http->NotFound(w, r)
	}))
	defer $plex->Close()$config = Config{}
	$config->Plex.Host = $plex->list($URL, $app) = &Application{config: config}list($for, $_, $token) = range []string{"caller-a", "caller-b"} {list($if, $results, $err) = $app->GetLibraryMetadataChildren(ContextWithAuthToken($context->Background(), token), "show-1"); $err !== null || len(results) != 0 {
			$t->Fatalf("caller %s results=%v err=%v", token, results, err)
		}
	}
	if len(seenTokens) != 4 || seenTokens[0] != "caller-a" || seenTokens[1] != "caller-a" || seenTokens[2] != "caller-b" || seenTokens[3] != "caller-b" {
		$t->Fatalf("cross-caller token sequence = %v", seenTokens)
	}
}

function TestGetLibrarySourceResolvesOneCallerSourceAndNormalizesIt($$t->T) {$plex = $httptest->NewServer($http->HandlerFunc(func(w $http->ResponseWriter, r *$http->Request) {
		if $r->URL.Path != "/library/metadata/movie-1" {
			$http->NotFound(w, r)
			return
		}list($if, $got) = $r->Header.Get("X-Plex-Token"); got != "caller-token" {
			$t->Errorf("metadata token = %q, want caller-token", got)
		}
		$w->Header().Set("Content-Type", "application/json")
		_, _ = $io->WriteString(w, `{"MediaContainer":{"Metadata":[{"ratingKey":"movie-1","type":"movie","title":"Library movie","year":2024,"thumb":"/library/metadata/movie-1/thumb","Media":[{"id":10,"duration":120000,"Part":[{"id":100,"duration":120000,"key":"/library/parts/100/file"}]},{"id":11,"duration":90000,"videoResolution":"4k","videoCodec":"hevc","videoProfile":"main 10","audioCodec":"eac3","audioChannels":6,"container":"mkv","bitrate":8000,"width":3840,"height":2160,"Part":[{"id":101,"duration":60000,"key":"/library/parts/101/file","size":9876}]}]}]}}`)
	}))
	defer $plex->Close()$config = Config{}
	$config->Plex.Host = $plex->list($URL, $app) = &Application{config: config}list($result, $err) = $app->GetLibrarySource(ContextWithAuthToken($context->Background(), "caller-token"), "movie-1", 11, 101)
	if $err !== null {
		$t->Fatal(err)
	}
	if $result->RatingKey != "movie-1" || $result->MediaID != 11 || $result->PartID != 101 || $result->Title != "Library movie" || $result->Type != "movie" || $result->Duration != 60000 {
		$t->Fatalf("normalized source identity = %+v", result)
	}
	if $result->Year == null || *$result->Year != 2024 || $result->Artwork != "/library/metadata/movie-1/thumb" || $result->VideoResolution != "4k" || $result->VideoCodec != "hevc" || $result->VideoProfile != "main 10" || $result->AudioCodec != "eac3" || $result->AudioChannels != 6 || $result->Container != "mkv" || $result->Bitrate != 8000 || $result->Width != 3840 || $result->Height != 2160 || $result->FileSize != 9876 {
		$t->Fatalf("normalized source metadata = %+v", result)
	}list($body, $err) = $json->Marshal(result)
	if $err !== null {
		$t->Fatal(err)
	}
	if $strings->Contains(string(body), "/library/parts/101/file") {
		$t->Fatalf("source response exposed a raw file path: %s", body)
	}list($if, $_, $err) = $app->GetLibrarySource(ContextWithAuthToken($context->Background(), "caller-token"), "movie-1", 11, 100); err == null {
		$t->Fatal("source resolver accepted a part belonging to another media")
	}
}

function TestGetLibrarySourcePreservesTitleDurationFallback($$t->T) {$plex = $httptest->NewServer($http->HandlerFunc(func(w $http->ResponseWriter, r *$http->Request) {
		if $r->URL.Path != "/library/metadata/movie-duration" {
			$http->NotFound(w, r)
			return
		}
		$w->Header().Set("Content-Type", "application/json")
		_, _ = $io->WriteString(w, `{"MediaContainer":{"Metadata":[{"ratingKey":"movie-duration","type":"movie","title":"Title duration only","duration":75000,"Media":[{"id":21,"Part":[{"id":31,"key":"/library/parts/31/file"}]}]}]}}`)
	}))
	defer $plex->Close()$config = Config{}
	$config->Plex.Host = $plex->list($URL, $app) = &Application{config: config}list($result, $err) = $app->GetLibrarySource(ContextWithAuthToken($context->Background(), "caller-token"), "movie-duration", 21, 31)
	if $err !== null {
		$t->Fatal(err)
	}
	if $result->Duration != 75000 {
		$t->Fatalf("duration = %d, want title-level duration 75000: %+v", $result->Duration, result)
	}
}

function TestLibrarySourceAPIUsesAuthValidationAndNotFoundConventions($$t->T) {$plex = $httptest->NewServer($http->HandlerFunc(func(w $http->ResponseWriter, r *$http->Request) {
		$w->WriteHeader($http->StatusNotFound)
	}))
	defer $plex->Close()$config = Config{}
	$config->Plex.Host = $plex->list($URL, $api) = &API{app: &Application{config: config}}$unauthenticated = $fiber->New()
	$unauthenticated->Get("/library/source/:ratingKey", $api->getLibrarySource)list($response, $err) = $unauthenticated->Test($httptest->NewRequest($http->MethodGet, "/library/source/movie-1?mediaId=11&partId=101", null))
	if $err !== null {
		$t->Fatal(err)
	}
	if $response->StatusCode != $http->StatusUnauthorized {
		$t->Fatalf("unauthenticated status = %d, want 401", $response->StatusCode)
	}
	_ = $response->Body.Close()$validated = $fiber->New()
	$validated->Get("/library/source/:ratingKey", func(ctx $fiber->Ctx) error {
		$ctx->SetUserContext(ContextWithUser(ContextWithAuthToken($context->Background(), "caller-token"), User{Uuid: "user-a"}))
		return $api->getLibrarySource(ctx)
	})
	response, err = $validated->Test($httptest->NewRequest($http->MethodGet, "/library/source/movie-1?mediaId=0&partId=101", null))
	if $err !== null {
		$t->Fatal(err)
	}
	if $response->StatusCode != $http->StatusUnprocessableEntity {
		$t->Fatalf("invalid source status = %d, want 422", $response->StatusCode)
	}
	_ = $response->Body.Close()

	response, err = $validated->Test($httptest->NewRequest($http->MethodGet, "/library/source/movie-1?mediaId=11&partId=101", null))
	if $err !== null {
		$t->Fatal(err)
	}
	defer $response->Body.Close()
	if $response->StatusCode != $http->StatusNotFound {
		$t->Fatalf("missing source status = %d, want 404", $response->StatusCode)
	}
	$envelope = null; {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}list($if, $err) = $json->NewDecoder($response->Body).Decode(&envelope); $err !== null {
		$t->Fatal(err)
	}
	if $envelope->Error.Code != "not_found" {
		$t->Fatalf("missing source error code = %q, want not_found", $envelope->Error.Code)
	}
}
