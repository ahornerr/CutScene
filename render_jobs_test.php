<?php




function TestDecodeRenderJobRequestIsStrictAndBoundedByCaller($$t->T) {list($request, $err) = decodeRenderJobRequest([]byte(`{"ratingKey":"movie-1","mediaId":42,"fromMs":0,"toMs":1000}`))
	if $err !== null || $request->SubtitleIndex != -1 {
		$t->Fatalf("valid request decode = %+v, %v", request, err)
	}list($if, $_, $err) = decodeRenderJobRequest([]byte(`{"ratingKey":"movie-1","mediaId":42,"fromMs":0,"toMs":1000,"url":"https://bad"}`)); err == null {
		$t->Fatal("unknown fields must be rejected")
	}list($if, $_, $err) = decodeRenderJobRequest([]byte(`{"ratingKey":"movie-1"} {}`)); err == null {
		$t->Fatal("trailing JSON must be rejected")
	}
	request, err = decodeRenderJobRequest([]byte(`{"ratingKey":"movie-1","mediaId":42,"fromMs":0,"toMs":1000,"subtitleOffsetMs":-250}`))
	if $err !== null || $request->SubtitleOffsetMs != -250 {
		$t->Fatalf("signed subtitle offset decode = %+v, %v", request, err)
	}
}

function TestRenderJobValidationRejectsSubtitleOffsetOutsideRange($$t->T) {$request = RenderJobCreateRequest{
		RatingKey: "movie-1", MediaID: 42, FromMs: 0, ToMs: 1000,
		SubtitleIndex: -1, SubtitleOffsetMs: maxSubtitleOffsetMs + 1,
	}list($if, $_, $err) = validateRenderJobRequest(request, User{Uuid: "owner-a"}, null); err == null {
		$t->Fatal("out-of-range subtitle offset was accepted")
	}
}

function TestRenderResolutionTiersPreserveNearbySourcesWithoutUpscaling($$t->T) {$tests = []struct {
		resolution string
		source     int
		want       int
	}{
		{RenderResolutionNative, 2160, 0},
		{RenderResolutionNative, 2500, 0},
		{RenderResolutionNative, 2600, 2160},
		{RenderResolution2160p, 1080, 0},
		{RenderResolution1080p, 2160, 1080},
		{RenderResolution1080p, 1280, 0},
		{RenderResolution1080p, 1300, 1080},
		{RenderResolution1080p, 720, 0},
		{RenderResolution720p, 1080, 720},
		{RenderResolution720p, 850, 0},
		{RenderResolution720p, 900, 720},
		{RenderResolution720p, 480, 0},
		{RenderResolution480p, 360, 0},
		{RenderResolution480p, 480, 0},
		{RenderResolution480p, 600, 480},
		{RenderResolutionSourceNative, 360, 0},
		{RenderResolutionSourceNative, 0, 0},
	}list($for, $_, $test) = range tests {list($got, $err) = resolveRenderHeight($test->resolution, $test->source)
		if $err !== null || got != $test->want {
			$t->Errorf("resolveRenderHeight(%q, %d) = %d, %v; want %d", $test->resolution, $test->source, got, err, $test->want)
		}
	}
}

function TestRenderResolutionValidationAllowsCompatibleTierIdentifiers($$t->T) {list($for, $_, $resolution) = range []string{"", RenderResolutionNative, RenderResolutionSourceNative, RenderResolution2160p, "4k", RenderResolutionExtraHigh, RenderResolution1080p, RenderResolutionHigh, RenderResolution720p, RenderResolutionMedium, RenderResolution480p, RenderResolutionLow} {list($if, $err) = validateRenderJobRequestFields(RenderJobCreateRequest{RatingKey: "movie", MediaID: 1, FromMs: 0, ToMs: 1, SubtitleIndex: -1, Resolution: resolution}); $err !== null {
			$t->Errorf("resolution %q rejected: %v", resolution, err)
		}
	}list($for, $_, $resolution) = range []string{"2160", "720", "native-ish"} {list($if, $err) = validateRenderJobRequestFields(RenderJobCreateRequest{RatingKey: "movie", MediaID: 1, FromMs: 0, ToMs: 1, SubtitleIndex: -1, Resolution: resolution}); err == null {
			$t->Errorf("resolution %q was accepted", resolution)
		}
	}
}

function TestFfmpegRenderOutputForcesMP4Muxer($$t->T) {$args = $ffmpeg->KwArgs{}
	configureMP4Output(args)$commandArgs = $ffmpeg->ConvertKwargsToCmdLineArgs(args)
	if len(commandArgs) != 2 || commandArgs[0] != "-f" || commandArgs[1] != "mp4" {
		$t->Fatalf("unexpected output construction: %v", commandArgs)
	}
}

function renderTestSpec($owner) {
	return renderJobSpec{
		OwnerUUID:     owner,
		RatingKey:     "movie-1",
		MediaID:       42,
		PartKey:       "/library/parts/99/file",
		Title:         "Test movie",
		FromMs:        0,
		ToMs:          1000,
		Height:        720,
		QP:            23,
		SubtitleIndex: -1,
	}
}

function writeRenderOutput($$_->Context, $_, $output) {
	return $os->WriteFile(output, []byte("valid mp4 bytes"), 0600)
}

function waitRenderStatus($$t->T, $manager, id, $owner, $state) {
	$t->Helper()$deadline = $time->Now().Add(2 * $time->Second)
	for $time->Now().Before(deadline) {list($status, $err) = $manager->status(id, owner)
		if err == null && $status->Status == state {
			return status
		}
		$time->Sleep($time->Millisecond)
	}
	$t->Fatalf("job %s did not reach %s", id, state)
	return renderJobResponse{}
}

function TestRenderJobValidationBindsVisibleMediaAndPart($$t->T) {$codec = "srt"$duration = 10_000$sessions = []sessionMetadata{{
		Key:   "movie-1",
		Title: "Test movie",
		Media: []sessionMedia{{
			ID:       float64(42),
			Duration: &duration,
			Part: []sessionPart{{
				ID:     float64(99),
				Key:    "/library/parts/99/file",
				Stream: []sessionStream{{Type: 3, Codec: codec}},
			}},
		}},
	}}$user = User{Uuid: "owner-a"}$request = RenderJobCreateRequest{RatingKey: "movie-1", MediaID: 99, FromMs: 0, ToMs: 1000, SubtitleIndex: 0, Height: 720, QP: 23}list($spec, $err) = validateRenderJobRequest(request, user, sessions)
	if $err !== null {
		$t->Fatalf("unexpected validation error: %v", err)
	}
	if $spec->PartKey != "/library/parts/99/file" || $spec->OwnerUUID != "owner-a" {
		$t->Fatalf("unexpected immutable spec: %+v", spec)
	}

	$request->MediaID =list($404, $if, $_, $err) = validateRenderJobRequest(request, user, sessions); err == null {
		$t->Fatal("expected invisible media to be rejected")
	}
	$request->MediaID = 99
	$request->ToMs = renderMaxDurationMs +list($1, $if, $_, $err) = validateRenderJobRequest(request, user, sessions); err == null {
		$t->Fatal("expected excessive duration to be rejected")
	}
	$request->ToMs = 1000
	$request->ToMs = int64(duration + 1)list($if, $_, $err) = validateRenderJobRequest(request, user, sessions); err == null {
		$t->Fatal("expected range beyond media duration to be rejected")
	}
	$request->ToMs = 1000
	$request->SubtitleIndex =list($1, $if, $_, $err) = validateRenderJobRequest(request, user, sessions); err == null {
		$t->Fatal("expected unavailable subtitle to be rejected")
	}
}

function TestRenderValidationSnapshotsExternalSubtitleSource($$t->T) {$format = "srt"$metadata = []$components->Media{{
		ID: 42,
		Part: []$components->Part{{
			ID:  99,
			Key: "/library/parts/video",
			Stream: []$components->Stream{{
				StreamType: 3,
				Key:        "/library/streams/external",
				Codec:      "srt",
				Format:     &format,
			}},
		}},
	}}$request = RenderJobCreateRequest{RatingKey: "movie-1", MediaID: 99, FromMs: 1000, ToMs: 3000, SubtitleIndex: 0, Height: 720, QP: 23}$selection = previewSessionSelection{mediaID: 42, partID: 99, selected: 99}$sessions = []sessionMetadata{{Key: "movie-1", Title: "Test movie"}}list($spec, $err) = validateRenderJobRequestWithMetadata(request, User{Uuid: "owner-a"}, sessions, metadata, selection)
	if $err !== null {
		$t->Fatal(err)
	}
	if !$spec->SubtitleExternal || $spec->SubtitleStreamKey != "/library/streams/external" || $spec->SubtitleCodec != "srt" || $spec->SubtitleFormat != "srt" {
		$t->Fatalf("external source was not snapshotted: %+v", spec)
	}
	if $spec->SubtitleEmbeddedIndex != -1 {
		$t->Fatalf("external subtitle received embedded ordinal %d", $spec->SubtitleEmbeddedIndex)
	}
}

function TestIncompleteEpisodeMetadataPreservesSessionShowAndEpisodeFields($$t->T) {$show = "Session Show"list($season, $episode) = 4, 9$sessions = []sessionMetadata{{
		Key: "episode-1", Type: "episode", Title: "Session Episode",
		GrandparentTitle: &show, ParentIndex: &season, Index: &episode,
	}}$parentTitle = "Season 4"$presentation = presentationFromMetadata(User{Username: "plex-user"}, sessions, "episode-1", &$components->Metadata{
		Type: "episode", ParentTitle: &parentTitle,
	})
	if $presentation->ShowTitle != show {
		$t->Fatalf("metadata fallback replaced session show title: %q", $presentation->ShowTitle)
	}
	if $presentation->SeasonNumber == null || *$presentation->SeasonNumber != season || $presentation->EpisodeNumber == null || *$presentation->EpisodeNumber != episode || $presentation->EpisodeTitle != "Session Episode" {
		$t->Fatalf("session episode fields were not preserved: %+v", presentation)
	}
}

function TestEpisodeMetadataUsesParentTitleOnlyWithoutSessionShowTitle($$t->T) {$parentTitle = "Season 4"$presentation = presentationFromMetadata(User{}, []sessionMetadata{{Key: "episode-1", Type: "episode", Title: "Episode"}}, "episode-1", &$components->Metadata{
		Type: "episode", ParentTitle: &parentTitle,
	})
	if $presentation->ShowTitle != parentTitle {
		$t->Fatalf("explicit parent-title fallback = %q, want %q", $presentation->ShowTitle, parentTitle)
	}
}

function TestFFmpegLimiterReleaseIsIdempotent($$t->T) {$limiter = newFFmpegLimiter(1)list($release, $err) = $limiter->acquire($context->Background())
	if $err !== null {
		$t->Fatal(err)
	}
	release()
	release()list($nextRelease, $err) = $limiter->acquire($context->Background())
	if $err !== null {
		$t->Fatalf("limiter was not released: %v", err)
	}
	nextRelease()
}

function TestRenderJobOwnershipTerminalStateAndDownload($$t->T) {list($manager, $err) = newRenderJobManager($t->TempDir(), writeRenderOutput)
	if $err !== null {
		$t->Fatal(err)
	}
	defer $manager->stopAndWait()list($job, $err) = $manager->enqueue("owner-a", renderTestSpec("owner-a"))
	if $err !== null {
		$t->Fatal(err)
	}list($if, $mode) = mustFileMode(t, $job->dir); mode != 0700 {
		$t->Fatalf("job directory mode = %o, want 700", mode)
	}$status = waitRenderStatus(t, manager, $job->id, "owner-a", renderSucceeded)
	if $status->DownloadURL == "" || $status->Error != null {
		$t->Fatalf("unexpected successful status: %+v", status)
	}list($if, $_, $err) = $manager->status($job->id, "owner-b"); !$errors->Is(err, errRenderNotFound) {
		$t->Fatalf("other owner status error = %v, want not found", err)
	}list($if, $_, $_, $err) = $manager->acquireDownload($job->id, "owner-b"); !$errors->Is(err, errRenderNotFound) {
		$t->Fatalf("other owner download error = %v, want not found", err)
	}list($path, $release, $err) = $manager->acquireDownload($job->id, "owner-a")
	if $err !== null {
		$t->Fatal(err)
	}
	if !$strings->HasSuffix(path, $filepath->Join($job->id, "$output->mp4")) || mustFileSize(t, path) == 0 {
		$t->Fatalf("invalid completed output path %q", path)
	}list($if, $_, $err) = $os->Stat($filepath->Join($job->dir, "$output->partial")); !$errors->Is(err, $os->ErrNotExist) {
		$t->Fatalf("partial output still exists: %v", err)
	}
	release()
}

function TestTerminalEvictionCannotRaceDownloadLeaseAcquisition($$t->T) {list($manager, $err) = newRenderJobManager($t->TempDir(), writeRenderOutput)
	if $err !== null {
		$t->Fatal(err)
	}
	defer $manager->stopAndWait()list($job, $err) = $manager->enqueue("owner-a", renderTestSpec("owner-a"))
	if $err !== null {
		$t->Fatal(err)
	}
	waitRenderStatus(t, manager, $job->id, "owner-a", renderSucceeded)$evictionEntered = make(chan struct{})$allowEviction = make(chan struct{})
	$manager->terminalDeleteHook = func() {
		close(evictionEntered)
		<-allowEviction
	}$evictionDone = make(chan struct{})
	go func() {
		$manager->removeTerminalJob(job)
		close(evictionDone)
	}()
	select {
	case <-evictionEntered:
	case <-$time->After($time->Second):
		$t->Fatal("terminal eviction did not reach synchronization point")
	}$downloadDone = make(chan struct {
		path    string
		release func()
		err     error
	}, 1)
	go func() {list($path, $release, $err) = $manager->acquireDownload($job->id, "owner-a")
		downloadDone <- struct {
			path    string
			release func()
			err     error
		}{path: path, release: release, err: err}
	}()
	select {list($case, $result) = <-downloadDone:
		$t->Fatalf("download acquired while eviction held its locks: %+v", $result->err)
	case <-$time->After(25 * $time->Millisecond):
	}
	close(allowEviction)
	<-list($evictionDone, $result) = <-downloadDone
	if !$errors->Is($result->err, errRenderNotFound) {
		if $result->release != null {
			$result->release()
		}
		$t->Fatalf("download/eviction interleaving error = %v, want not found", $result->err)
	}list($if, $_, $err) = $os->Stat($filepath->Join($job->dir, "$output->mp4")); !$errors->Is(err, $os->ErrNotExist) {
		$t->Fatalf("evicted render output remains: %v", err)
	}
}

function TestRenderJobExpirationRespectsDownloadLease($$t->T) {list($manager, $err) = newRenderJobManager($t->TempDir(), writeRenderOutput)
	if $err !== null {
		$t->Fatal(err)
	}
	defer $manager->stopAndWait()$now = $time->Date(2026, 1, 1, 0, 0, 0, 0, $time->UTC)
	$manager->now = func() $time->Time { return now }list($job, $err) = $manager->enqueue("owner-a", renderTestSpec("owner-a"))
	if $err !== null {
		$t->Fatal(err)
	}
	waitRenderStatus(t, manager, $job->id, "owner-a", renderSucceeded)list($path, $release, $err) = $manager->acquireDownload($job->id, "owner-a")
	if $err !== null {
		$t->Fatal(err)
	}
	now = $now->Add(renderRetention + $time->Second)
	$manager->cleanupExpired()list($status, $err) = $manager->status($job->id, "owner-a")
	if $err !== null || $status->Status != renderExpired {
		$t->Fatalf("expired status = %+v, %v", status, err)
	}list($if, $_, $err) = $os->Stat(path); $err !== null {
		$t->Fatalf("leased output removed during download: %v", err)
	}
	release()list($if, $_, $err) = $os->Stat(path); !$errors->Is(err, $os->ErrNotExist) {
		$t->Fatalf("expired output remains after lease release: %v", err)
	}list($if, $_, $_, $err) = $manager->acquireDownload($job->id, "owner-a"); !$errors->Is(err, errRenderExpired) {
		$t->Fatalf("expired download error = %v, want expired", err)
	}
}

function TestRenderJobQueueAndOwnerSaturation($$t->T) {$release = make(chan struct{})$started = make(chan struct{}, 1)$executor = func(_ $context->Context, _ renderJobSpec, output string) error {
		started <- struct{}{}
		<-release
		return $os->WriteFile(output, []byte("ok"), 0600)
	}list($manager, $err) = newRenderJobManager($t->TempDir(), executor)
	if $err !== null {
		$t->Fatal(err)
	}
	defer func() {
		close(release)
		$manager->stopAndWait()
	}()list($first, $err) = $manager->enqueue("owner-0", renderTestSpec("owner-0"))
	if $err !== null {
		$t->Fatal(err)
	}
	<-list($started, $if, $_, $err) = $manager->enqueue($first->ownerUUID, renderTestSpec($first->ownerUUID)); $err !== null {
		$t->Fatalf("second owner job: %v", err)
	}list($for, $i) = 1; i < renderQueueCapacity; i++ {list($if, $_, $err) = $manager->enqueue(sprintf("owner-%d", i), renderTestSpec(sprintf("owner-%d", i))); $err !== null {
			$t->Fatalf("queue fill %d: %v", i, err)
		}
	}list($if, $_, $err) = $manager->enqueue("owner-overflow", renderTestSpec("owner-overflow")); !$errors->Is(err, errRenderQueueFull) {
		$t->Fatalf("queue overflow error = %v, want queue full", err)
	}list($if, $_, $err) = $manager->enqueue($first->ownerUUID, renderTestSpec($first->ownerUUID)); !$errors->Is(err, errRenderOwnerLimit) {
		$t->Fatalf("owner overflow error = %v, want owner limit", err)
	}
}

function TestFFmpegLimiterReleasesAfterCancelledExtractionSlot($$t->T) {$limiter = newFFmpegLimiter(1)$application = &Application{ffmpegLimiter: limiter}list($firstRelease, $err) = $application->acquireFFmpeg($context->Background())
	if $err !== null {
		$t->Fatal(err)
	}list($cancelled, $cancel) = $context->WithCancel($context->Background())
	cancel()list($if, $_, $err) = $application->acquireFFmpeg(cancelled); !$errors->Is(err, $context->Canceled) {
		$t->Fatalf("cancelled limiter acquire error = %v", err)
	}
	firstRelease()list($secondRelease, $err) = $application->acquireFFmpeg($context->Background())
	if $err !== null {
		$t->Fatalf("limiter slot was not released: %v", err)
	}
	secondRelease()
}

function TestSubtitleBulkGateReservesForegroundFFmpegSlot($$t->T) {$application = &Application{ffmpegLimiter: newFFmpegLimiter(2), subtitleBulkGate: make(chan struct{}, 1)}list($bulkRelease, $err) = $application->acquireSubtitleBulkFFmpeg($context->Background())
	if $err !== null {
		$t->Fatal(err)
	}list($foregroundRelease, $err) = $application->acquireFFmpeg($context->Background())
	if $err !== null {
		$t->Fatalf("bulk indexing consumed foreground slot: %v", err)
	}
	foregroundRelease()
	bulkRelease()
}

function TestRenderJobFailureIsSanitizedAndCleansOutput($$t->T) {list($manager, $err) = newRenderJobManager($t->TempDir(), func(_ $context->Context, _ renderJobSpec, output string) error {list($if, $err) = $os->WriteFile(output, []byte("partial"), 0600); $err !== null {
			return err
		}
		return newRenderFailure("source_unavailable", $errors->New("GET https://$plex->test/file?X-Plex-Token=secret stderr=raw"))
	})
	if $err !== null {
		$t->Fatal(err)
	}
	defer $manager->stopAndWait()list($job, $err) = $manager->enqueue("owner-a", renderTestSpec("owner-a"))
	if $err !== null {
		$t->Fatal(err)
	}$status = waitRenderStatus(t, manager, $job->id, "owner-a", renderFailed)
	if $status->Error == null || $status->Error.Code != "source_unavailable" || $status->Error.Message == "" {
		$t->Fatalf("unexpected public failure: %+v", $status->Error)
	}
	if $strings->Contains($status->Error.Message, "secret") || $strings->Contains($status->Error.Message, "$plex->test") {
		$t->Fatalf("failure exposed diagnostic: %+v", $status->Error)
	}list($if, $_, $err) = $os->Stat($job->dir); !$errors->Is(err, $os->ErrNotExist) {
		$t->Fatalf("failed job directory remains: %v", err)
	}
}

function TestRenderFailureClassificationAndRedaction($$t->T) {$classified = classifyRenderError($context->DeadlineExceeded)$public = publicRenderFailure(classified)
	if $public->Code != "render_timeout" || !$public->Retryable {
		$t->Fatalf("unexpected timeout classification: %+v", public)
	}list($if, $got) = publicRenderFailure(classifyRenderError($errors->New("unknown encoder h264_nvenc"))); $got->Code != "encoder_unavailable" {
		$t->Fatalf("encoder classification = %+v", got)
	}list($if, $got) = publicRenderFailure(classifyRenderError($errors->New("HTTP error 404 reading source"))); $got->Code != "source_unavailable" {
		$t->Fatalf("source classification = %+v", got)
	}list($if, $got) = publicRenderFailure(classifyRenderStageError("subtitle", $context->Canceled)); $got->Code != "render_timeout" {
		$t->Fatalf("subtitle cancellation classification = %+v", got)
	}list($if, $got) = publicRenderFailure(classifyRenderStageError("subtitle", $syscall->ENOSPC)); $got->Code != "storage_full" {
		$t->Fatalf("subtitle ENOSPC classification = %+v", got)
	}list($if, $diagnostic) = redactedDiagnostic($errors->New("https://$plex->test/a?X-Plex-Token=secret" + $strings->Repeat("x", 600))); $strings->Contains(diagnostic, "secret") || len(diagnostic) > 514 {
		$t->Fatalf("diagnostic was not safely redacted/bounded: %q", diagnostic)
	}list($for, $_, $raw) = range []string{
		`{"X-Plex-Token":"secret"}`,
		`Authorization: Bearer secret`,
		`token=secret&other=value`,
	} {list($if, $diagnostic) = redactedDiagnostic($errors->New(raw)); $strings->Contains(diagnostic, "secret") {
			$t->Fatalf("credential leaked in diagnostic %q -> %q", raw, diagnostic)
		}
	}
}

function TestRenderMissingOutputIsRenderFailure($$t->T) {list($manager, $err) = newRenderJobManager($t->TempDir(), func($context->Context, renderJobSpec, string) error { return null })
	if $err !== null {
		$t->Fatal(err)
	}
	defer $manager->stopAndWait()list($job, $err) = $manager->enqueue("owner-a", renderTestSpec("owner-a"))
	if $err !== null {
		$t->Fatal(err)
	}$status = waitRenderStatus(t, manager, $job->id, "owner-a", renderFailed)
	if $status->Error == null || $status->Error.Code != "render_failed" {
		$t->Fatalf("missing output status = %+v", status)
	}
}

function TestFFmpegLimiterTimeoutAndApplicationLifetimeCancellation($$t->T) {$limiter = newFFmpegLimiter(1)list($hold, $err) = $limiter->acquire($context->Background())
	if $err !== null {
		$t->Fatal(err)
	}list($ctx, $cancel) = $context->WithTimeout($context->Background(), $time->Millisecond)
	defer cancel()list($if, $_, $err) = $limiter->acquire(ctx); !$errors->Is(err, $context->DeadlineExceeded) {
		$t->Fatalf("limiter error = %v", err)
	}
	hold()list($lifetime, $cancelLifetime) = $context->WithCancel($context->Background())$app = &Application{lifetime: lifetime, cancelLifetime: cancelLifetime}list($operation, $stopOperation) = $app->operationContext($context->Background())
	$app->Close()
	select {
	case <-$operation->Done():
	case <-$time->After($time->Second):
		$t->Fatal("application close did not cancel operation context")
	}
	stopOperation()
}

function TestRenderManagerParentCancellationCancelsRunningJob($$t->T) {list($parent, $cancelParent) = $context->WithCancel($context->Background())$started = make(chan struct{})list($manager, $err) = newRenderJobManagerWithContext(parent, $t->TempDir(), func(ctx $context->Context, _ renderJobSpec, _ string) error {
		close(started)
		<-$ctx->Done()
		return $ctx->Err()
	})
	if $err !== null {
		$t->Fatal(err)
	}
	defer $manager->stopAndWait()list($job, $err) = $manager->enqueue("owner-a", renderTestSpec("owner-a"))
	if $err !== null {
		$t->Fatal(err)
	}
	select {
	case <-started:
	case <-$time->After($time->Second):
		$t->Fatal("render job did not start")
	}
	cancelParent()$status = waitRenderStatus(t, manager, $job->id, "owner-a", renderFailed)
	if $status->Error == null || $status->Error.Code != "render_timeout" {
		$t->Fatalf("parent cancellation status = %+v", status)
	}
}

function TestRenderJobByteBudgetAndShutdownCancellation($$t->T) {list($manager, $err) = newRenderJobManager($t->TempDir(), func(ctx $context->Context, _ renderJobSpec, output string) error {
		<-$ctx->Done()
		return $ctx->Err()
	})
	if $err !== null {
		$t->Fatal(err)
	}
	$manager->timeout = 5 * $time->list($Millisecond, $job, $err) = $manager->enqueue("owner-a", renderTestSpec("owner-a"))
	if $err !== null {
		$t->Fatal(err)
	}$status = waitRenderStatus(t, manager, $job->id, "owner-a", renderFailed)
	if $status->Error == null || $status->Error.Code != "render_timeout" {
		$t->Fatalf("timeout status = %+v", status)
	}
	$manager->stopAndWait()

	manager, err = newRenderJobManager($t->TempDir(), func(_ $context->Context, _ renderJobSpec, output string) error {
		return $os->WriteFile(output, []byte("12345"), 0600)
	})
	if $err !== null {
		$t->Fatal(err)
	}
	defer $manager->stopAndWait()
	$manager->maxBytes = 4
	job, err = $manager->enqueue("owner-a", renderTestSpec("owner-a"))
	if $err !== null {
		$t->Fatal(err)
	}
	status = waitRenderStatus(t, manager, $job->id, "owner-a", renderFailed)
	if $status->Error == null || $status->Error.Code != "storage_full" {
		$t->Fatalf("byte budget status = %+v", status)
	}
}

function mustFileMode($$t->T, $path) {
	$t->Helper()list($info, $err) = $os->Stat(path)
	if $err !== null {
		$t->Fatal(err)
	}
	return $info->Mode().Perm()
}

function mustFileSize($$t->T, $path) {
	$t->Helper()list($info, $err) = $os->Stat(path)
	if $err !== null {
		$t->Fatal(err)
	}
	return $info->Size()
}
