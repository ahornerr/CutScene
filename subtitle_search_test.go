package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/LukeHagar/plexgo/models/components"
)

func TestChunkSubtitleEntries(t *testing.T) {
	entries := []SubtitleEntry{{Start: 10, End: 20, Text: "first"}, {Start: 30, End: 40, Text: "second"}}
	chunks := chunkSubtitleEntries(entries)
	if len(chunks) != 1 || chunks[0].Start != 10 || chunks[0].End != 40 || chunks[0].Text != "first\nsecond" {
		t.Fatalf("unexpected chunks: %+v", chunks)
	}
}

func TestNormalizeSubtitleSearchText(t *testing.T) {
	tests := []struct {
		name, input, want string
	}{
		{"inline tags", "<i>♪ Lonely blue boy ♪</i>", "♪ Lonely blue boy ♪"},
		{"entities", "<b>Tom &amp; Jerry</b>&nbsp;again", "Tom & Jerry again"},
		{"ssa braces", "{\\i1}Hello {\\i0}<font color=red>world</font>", "Hello world"},
		{"markup-looking text", "<script>alert(1)</script> safe", "alert(1) safe"},
		{"line boundaries", "<i>one</i>\n  <b>two</b>", "one\ntwo"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := normalizeSubtitleSearchText(test.input); got != test.want {
				t.Fatalf("got %q, want %q", got, test.want)
			}
		})
	}
}

func TestChunkSubtitleEntriesStoresCleanText(t *testing.T) {
	chunks := chunkSubtitleEntries([]SubtitleEntry{{Start: 0, End: 1000, Text: "<i>Hello &amp; goodbye</i>"}})
	if len(chunks) != 1 || chunks[0].Text != "Hello & goodbye" {
		t.Fatalf("chunks = %+v", chunks)
	}
}

func TestDiversifySubtitleSearchHitsSoftlyFavorsMultipleSources(t *testing.T) {
	candidates := make([]subtitleSearchHit, 0, 44)
	for i := 0; i < 40; i++ {
		candidates = append(candidates, subtitleSearchHit{RatingKey: "dominant", MediaID: 1, PartID: 1, Score: 1 - float64(i)/100})
	}
	for i := int64(2); i <= 5; i++ {
		candidates = append(candidates, subtitleSearchHit{RatingKey: fmt.Sprintf("other-%d", i), MediaID: i, PartID: i, Score: 0.5})
	}
	results := diversifySubtitleSearchHits(candidates, 50)
	counts := make(map[string]int)
	firstOther := len(results)
	otherSourcesNearTop := 0
	for index, result := range results {
		key := fmt.Sprintf("%s:%d:%d", result.RatingKey, result.MediaID, result.PartID)
		counts[key]++
		if result.RatingKey != "dominant" {
			if firstOther == len(results) {
				firstOther = index
			}
			if index < 10 {
				otherSourcesNearTop++
			}
		}
	}
	if counts["dominant:1:1"] != 40 || len(counts) != 5 {
		t.Fatalf("diversification counts = %+v", counts)
	}
	if len(results) != 44 {
		t.Fatalf("results = %d, want all 44 available candidates", len(results))
	}
	if firstOther > 4 {
		t.Fatalf("first other source position = %d, want it near the top", firstOther)
	}
	if otherSourcesNearTop < 4 {
		t.Fatalf("other sources near top = %d, want 4", otherSourcesNearTop)
	}
}

func TestHybridSubtitleCandidatesPrioritizeLiteralAndDedupeByCue(t *testing.T) {
	literal := subtitleSearchHit{RatingKey: "brother", MediaID: 1, PartID: 1, SubtitleIndex: 0, StartMs: 100, EndMs: 200, Text: "Do not seek the treasure"}
	semantic := subtitleSearchHit{RatingKey: "deep-web", MediaID: 2, PartID: 2, SubtitleIndex: 0, StartMs: 10, EndMs: 20, Text: "unrelated semantic result"}
	candidates := mergeSubtitleSearchCandidates([]subtitleSearchCandidate{
		{Hit: semantic, Tier: 2, Rank: 0},
		{Hit: literal, Tier: 0, Rank: 0},
		{Hit: literal, Tier: 1, Rank: 0},
	})
	result := diversifyHybridSubtitleSearchCandidates(candidates, 10)
	if len(result) != 2 || result[0].Hit.RatingKey != "brother" || result[1].Hit.RatingKey != "deep-web" {
		t.Fatalf("hybrid ordering = %+v", result)
	}
}

func TestHybridSubtitleLiteralNormalizationHandlesCueLineBreaksAndPunctuation(t *testing.T) {
	query := normalizeSubtitleSearchLiteral("Do not seek the treasure!")
	text := normalizeSubtitleSearchLiteral("Do not\nseek — the treasure")
	if query != "do not seek the treasure" || !strings.Contains(text, query) {
		t.Fatalf("normalized query=%q text=%q", query, text)
	}
}

func TestHybridSubtitlePhraseTierFallsBackBeforeSemantic(t *testing.T) {
	phrase := subtitleSearchCandidate{Hit: subtitleSearchHit{RatingKey: "phrase"}, Tier: 1, Rank: 0}
	semantic := subtitleSearchCandidate{Hit: subtitleSearchHit{RatingKey: "semantic"}, Tier: 2, Rank: 0}
	result := diversifyHybridSubtitleSearchCandidates(mergeSubtitleSearchCandidates([]subtitleSearchCandidate{semantic, phrase}), 10)
	if len(result) != 2 || result[0].Hit.RatingKey != "phrase" {
		t.Fatalf("phrase fallback ordering = %+v", result)
	}
}

func TestHybridSubtitleQuotasRejectDominantTitlesAndFillFromLaterCandidates(t *testing.T) {
	candidates := make([]subtitleSearchCandidate, 0, 50)
	for i := 0; i < 40; i++ {
		candidates = append(candidates, subtitleSearchCandidate{Hit: subtitleSearchHit{
			RatingKey: "dominant", MediaID: int64(i + 1), PartID: int64(i + 1), Title: "The Same Movie",
		}, Tier: 2, Rank: i})
	}
	for i := 0; i < 10; i++ {
		candidates = append(candidates, subtitleSearchCandidate{Hit: subtitleSearchHit{
			RatingKey: fmt.Sprintf("later-%d", i), Title: fmt.Sprintf("Movie %d", i),
		}, Tier: 2, Rank: 40 + i})
	}
	result := diversifyHybridSubtitleSearchCandidates(mergeSubtitleSearchCandidates(candidates), 10)
	if len(result) != 10 {
		t.Fatalf("result count = %d, want 10", len(result))
	}
	dominant := 0
	laterTitles := make(map[string]bool)
	for _, candidate := range result {
		if candidate.Hit.RatingKey == "dominant" {
			dominant++
		} else {
			laterTitles[candidate.Hit.Title] = true
		}
	}
	if dominant > 3 || len(laterTitles) < 7 {
		t.Fatalf("dominant=%d later titles=%d, want dominant <= 3 and later titles >= 7", dominant, len(laterTitles))
	}
}

func TestHybridSubtitleTitleQuotaSpansVersionsAndParts(t *testing.T) {
	candidates := make([]subtitleSearchCandidate, 0, 12)
	for i := 0; i < 7; i++ {
		candidates = append(candidates, subtitleSearchCandidate{Hit: subtitleSearchHit{
			RatingKey: fmt.Sprintf("version-%d", i), MediaID: int64(i + 1), PartID: int64(i + 10), Title: "O Brother, Where Art Thou?",
		}, Tier: 2, Rank: i})
	}
	for i := 0; i < 5; i++ {
		candidates = append(candidates, subtitleSearchCandidate{Hit: subtitleSearchHit{
			RatingKey: fmt.Sprintf("other-%d", i), Title: fmt.Sprintf("Other %d", i),
		}, Tier: 2, Rank: 7 + i})
	}
	result := diversifyHybridSubtitleSearchCandidates(mergeSubtitleSearchCandidates(candidates), 10)
	if len(result) != 10 {
		t.Fatalf("result count = %d, want 10", len(result))
	}
	sameTitle := 0
	otherTitles := 0
	for _, candidate := range result {
		if candidate.Hit.Title == "O Brother, Where Art Thou?" {
			sameTitle++
		} else {
			otherTitles++
		}
	}
	if sameTitle > 5 || otherTitles != 5 {
		t.Fatalf("same title=%d other titles=%d, want same title <= 5 and other titles 5", sameTitle, otherTitles)
	}
}

func TestHybridSubtitleLiteralMatchSurvivesRemainderQuotas(t *testing.T) {
	candidates := make([]subtitleSearchCandidate, 0, 8)
	for i := 0; i < 7; i++ {
		candidates = append(candidates, subtitleSearchCandidate{Hit: subtitleSearchHit{
			RatingKey: "semantic-source", Title: "Semantic Movie", MediaID: int64(i + 1), PartID: int64(i + 1),
		}, Tier: 2, Rank: i})
	}
	candidates = append(candidates, subtitleSearchCandidate{Hit: subtitleSearchHit{
		RatingKey: "o-brother", Title: "O Brother, Where Art Thou?", Text: "O Brother, Where Art Thou?",
	}, Tier: 0, Rank: 0})
	result := diversifyHybridSubtitleSearchCandidates(mergeSubtitleSearchCandidates(candidates), 4)
	if len(result) != 4 || result[0].Tier != 0 || result[0].Hit.RatingKey != "o-brother" {
		t.Fatalf("literal result = %+v", result)
	}
}

func TestHybridSubtitleTitleContextKeepsEpisodesSeparate(t *testing.T) {
	seasonOne, seasonTwo := 1, 2
	candidates := []subtitleSearchCandidate{
		{Hit: subtitleSearchHit{RatingKey: "episode-one", Title: "Pilot", ShowTitle: "Example Show", Season: &seasonOne, Episode: func() *int { value := 1; return &value }()}, Tier: 2, Rank: 0},
		{Hit: subtitleSearchHit{RatingKey: "episode-two", Title: "Pilot", ShowTitle: "Example Show", Season: &seasonTwo, Episode: func() *int { value := 1; return &value }()}, Tier: 2, Rank: 1},
	}
	result := diversifyHybridSubtitleSearchCandidates(mergeSubtitleSearchCandidates(candidates), 2)
	if len(result) != 2 || result[0].Hit.RatingKey != "episode-one" || result[1].Hit.RatingKey != "episode-two" {
		t.Fatalf("episode title grouping = %+v", result)
	}
}

func TestValidateEmbeddings(t *testing.T) {
	good := [][]float32{make([]float32, subtitleEmbeddingDimensions)}
	if err := validateEmbeddings(good, 1, subtitleEmbeddingDimensions); err != nil {
		t.Fatalf("valid embedding rejected: %v", err)
	}
	if err := validateEmbeddings(good, 2, subtitleEmbeddingDimensions); err == nil {
		t.Fatal("mismatched count accepted")
	}
	bad := [][]float32{make([]float32, subtitleEmbeddingDimensions-1)}
	if err := validateEmbeddings(bad, 1, subtitleEmbeddingDimensions); err == nil {
		t.Fatal("wrong dimension accepted")
	}

	// Verify 1024 dimensions (e.g. Qwen/Qwen3-Embedding-0.6B)
	good1024 := [][]float32{make([]float32, 1024)}
	if err := validateEmbeddings(good1024, 1, 1024); err != nil {
		t.Fatalf("valid 1024-dim embedding rejected: %v", err)
	}
	if err := validateEmbeddings(good1024, 1, 768); err == nil {
		t.Fatal("1024-dim vector accepted when expecting 768")
	}
}

func TestTEIEmbedValidatesPayloadAndDimensions(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Inputs    []string `json:"inputs"`
			Normalize bool     `json:"normalize"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil || !request.Normalize || len(request.Inputs) != 1 {
			t.Errorf("unexpected TEI request: %+v", request)
		}
		_, _ = w.Write([]byte("[[" + strings.Trim(strings.Repeat("0,", subtitleEmbeddingDimensions), ",") + "]]"))
	}))
	defer server.Close()
	client, err := newTEIClient(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.embed(context.Background(), []string{"hello"}); err != nil {
		t.Fatalf("TEI response rejected: %v", err)
	}
}

func TestTEIClientRequiresHTTPURL(t *testing.T) {
	if _, err := newTEIClient("postgres://not-embeddings"); err == nil {
		t.Fatal("non-HTTP embeddings URL accepted")
	}
}

func TestOpenAIEmbeddingClientSendsVLLMRequestAndOrdersResults(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/embeddings" {
			t.Fatalf("path = %q, want /v1/embeddings", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Fatalf("authorization = %q", got)
		}
		var request struct {
			Model          string   `json:"model"`
			Input          []string `json:"input"`
			EncodingFormat string   `json:"encoding_format"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request.Model != openAIEmbeddingModel || request.EncodingFormat != "float" || len(request.Input) != 2 {
			t.Fatalf("unexpected OpenAI request: %+v", request)
		}
		zero := make([]float32, subtitleEmbeddingDimensions)
		one := make([]float32, subtitleEmbeddingDimensions)
		one[0] = 1
		_ = json.NewEncoder(w).Encode(openAIEmbeddingResponse{Data: []openAIEmbeddingItem{
			{Index: 1, Embedding: one}, {Index: 0, Embedding: zero},
		}})
	}))
	defer server.Close()

	client, err := newEmbeddingClient(server.URL, embeddingsProviderOpenAI, "test-key", "", subtitleEmbeddingDimensions)
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.embed(context.Background(), []string{"first", "second"})
	if err != nil {
		t.Fatal(err)
	}
	if result[0][0] != 0 || result[1][0] != 1 {
		t.Fatalf("results were not ordered by index: %v %v", result[0][0], result[1][0])
	}
}

func TestOpenAIEmbeddingClientRejectsWrongDimension(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bad := make([]float32, subtitleEmbeddingDimensions-1)
		_ = json.NewEncoder(w).Encode(openAIEmbeddingResponse{Data: []openAIEmbeddingItem{{Index: 0, Embedding: bad}}})
	}))
	defer server.Close()
	client, err := newEmbeddingClient(server.URL, embeddingsProviderOpenAI, "", "", subtitleEmbeddingDimensions)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.embed(context.Background(), []string{"bad"}); err == nil {
		t.Fatal("wrong dimension accepted")
	}
}

func TestEmbeddingsProviderDefaultsToTEIAndRejectsUnsupported(t *testing.T) {
	provider, err := normalizeEmbeddingsProvider("")
	if err != nil || provider != embeddingsProviderTEI {
		t.Fatalf("empty provider = %q, %v; want tei", provider, err)
	}
	if _, err := normalizeEmbeddingsProvider("local"); err == nil {
		t.Fatal("unsupported provider accepted")
	}
}

func TestIndexLibrarySectionSourcesUsesCallerHeadersTypeAndPagination(t *testing.T) {
	var mu sync.Mutex
	var starts []string
	var types []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Plex-Token"); got != "caller-token" {
			t.Errorf("token = %q", got)
		}
		if got := r.Header.Get("X-Plex-Accept"); got != "application/json" {
			t.Errorf("X-Plex-Accept = %q", got)
		}
		if got := r.Header.Get("Accept"); got != "application/json" {
			t.Errorf("Accept = %q", got)
		}
		mu.Lock()
		starts = append(starts, r.URL.Query().Get("X-Plex-Container-Start"))
		types = append(types, r.URL.Query().Get("type"))
		mu.Unlock()
		start, _ := strconv.Atoi(r.URL.Query().Get("X-Plex-Container-Start"))
		count := 100
		if start > 0 {
			count = 1
		}
		metadata := make([]components.Metadata, count)
		for i := range metadata {
			key := "movie-" + strconv.Itoa(start+i)
			metadata[i] = components.Metadata{Type: "movie", Title: key, RatingKey: &key}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"MediaContainer": map[string]any{
			"totalSize": 101, "offset": start, "Metadata": metadata,
		}})
	}))
	defer server.Close()

	app := &Application{}
	app.config.Plex.Host = server.URL
	items, err := app.indexLibrarySectionSources(context.Background(), "caller-token", "1", "movie")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 101 {
		t.Fatalf("items = %d, want 101", len(items))
	}
	if len(starts) != 2 || starts[0] != "0" || starts[1] != "100" || types[0] != "1" || types[1] != "1" {
		t.Fatalf("pagination requests starts=%v types=%v", starts, types)
	}
}

func TestIndexLibrarySectionsUsesJSONNegotiationAndDecodesSections(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/library/sections" || r.Header.Get("X-Plex-Accept") != "application/json" || r.Header.Get("Accept") != "application/json" {
			t.Fatalf("unexpected section request path=%q x-plex-accept=%q accept=%q", r.URL.Path, r.Header.Get("X-Plex-Accept"), r.Header.Get("Accept"))
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = w.Write([]byte(`{"MediaContainer":{"Directory":[{"key":"1","type":"movie"},{"key":"2","type":"show"}]}}`))
	}))
	defer server.Close()
	app := &Application{}
	app.config.Plex.Host = server.URL
	sections, err := app.indexLibrarySections(context.Background(), "caller-token")
	if err != nil || len(sections) != 2 || sections[0].Type != "movie" || sections[1].Type != "show" {
		t.Fatalf("sections=%+v err=%v", sections, err)
	}
}

func TestValidatePlexJSONResponseRejectsUnexpectedContentType(t *testing.T) {
	resp := &http.Response{Header: http.Header{"Content-Type": []string{"text/html"}}}
	if err := validatePlexJSONResponse(resp); err == nil {
		t.Fatal("unexpected content type accepted")
	}
}

func TestReadBoundedJSONUsesPlexNumericCompatibilityAndRejectsTrailingData(t *testing.T) {
	var page plexMetadataResponse
	data := []byte(`{"MediaContainer":{"totalSize":"1","offset":"0","Metadata":[{"type":"movie","title":"Example","ratingKey":"movie-1","year":"2024"}]}}`)
	if err := readBoundedJSON(strings.NewReader(string(data)), &page); err != nil {
		t.Fatalf("quoted numeric Plex page rejected: %v", err)
	}
	if page.MediaContainer == nil || page.MediaContainer.TotalSize != 1 || page.MediaContainer.Offset != 0 || len(page.MediaContainer.Metadata) != 1 {
		t.Fatalf("unexpected decoded page: %+v", page.MediaContainer)
	}
	var trailing plexMetadataResponse
	if err := readBoundedJSON(strings.NewReader(string(data)+" trailing"), &trailing); err == nil {
		t.Fatal("trailing JSON accepted")
	}
}

func TestSubtitleIndexJobStatusAndGlobalActiveGuard(t *testing.T) {
	manager := &subtitleIndexJobManager{jobs: make(map[string]*subtitleIndexJob)}
	now := time.Now().UTC()
	job := &subtitleIndexJob{id: "job-1", owner: "owner-a", state: "running", startedAt: now, updatedAt: now, discovered: 4, processed: 2, indexed: 8, skipped: 1, failed: 1, currentTitle: "Movie"}
	manager.active = job
	if _, err := manager.start(context.Background(), "owner-b"); !errors.Is(err, errSubtitleIndexActive) {
		t.Fatalf("active job error = %v", err)
	}
	status := job.status()
	if status.State != "running" || status.Discovered != 4 || status.Processed != 2 || status.IndexedChunks != 8 || status.CurrentTitle != "Movie" {
		t.Fatalf("unexpected status: %+v", status)
	}
}

func TestSubtitleIndexJobFailureUsesSafeStageDiagnostic(t *testing.T) {
	manager := &subtitleIndexJobManager{jobs: make(map[string]*subtitleIndexJob)}
	now := time.Now().UTC()
	job := &subtitleIndexJob{id: "job-2", owner: "owner-a", state: "running", startedAt: now, updatedAt: now}
	manager.active = job
	manager.finish(job, "failed", "could not discover Plex libraries")
	status := job.status()
	if status.State != "failed" || status.Error != "could not discover Plex libraries" || status.FinishedAt == nil {
		t.Fatalf("unexpected failure status: %+v", status)
	}
}

func TestSubtitleIndexJobRecordDoesNotRetainPlexAccess(t *testing.T) {
	if _, ok := reflect.TypeOf(subtitleIndexJob{}).FieldByName("access"); ok {
		t.Fatal("subtitle index job record retains Plex access")
	}
}

func TestSubtitleIndexJobRecordsEachTrackOutcomeOnce(t *testing.T) {
	job := &subtitleIndexJob{}
	job.recordIndexResult(subtitleSearchIndexResponse{
		Indexed:           2,
		Skipped:           []subtitleSearchSkipped{{SubtitleIndex: 0}, {SubtitleIndex: 1}, {SubtitleIndex: 2}},
		UnchangedTracks:   1,
		UnsupportedTracks: 1,
		EmptyTracks:       1,
	})
	status := job.status()
	if status.IndexedChunks != 2 || status.Skipped != 3 || status.UnchangedTracks != 1 || status.UnsupportedTracks != 1 || status.EmptyTracks != 1 {
		t.Fatalf("outcome counters = %+v", status)
	}
}

func TestSubtitleIndexItemFailureIsIsolatedAndDiagnosticsAreBounded(t *testing.T) {
	job := &subtitleIndexJob{}
	source := LibrarySearchResult{Title: "broken media"}
	itemErr := subtitleItemFailure(errors.New("could not extract subtitle: Matroska read error"))
	if !errors.Is(itemErr, errSubtitleIndexItem) {
		t.Fatal("item failure was not classified as an isolated source failure")
	}
	job.recordSourceFailure(source, itemErr, 0)
	status := job.status()
	if status.Failed != 1 || status.FailedTracks != 1 || status.Error == "" {
		t.Fatalf("unexpected isolated failure status: %+v", status)
	}
	if len(status.Error) > 1200 || strings.Contains(status.Error, "X-Plex-Token") {
		t.Fatalf("diagnostic was not bounded/sanitized: %q", status.Error)
	}
}

func TestSubtitleIndexJobCountsFailedTracksSeparatelyFromEmptyTracks(t *testing.T) {
	job := &subtitleIndexJob{}
	result := subtitleSearchIndexResponse{
		Skipped:               []subtitleSearchSkipped{{SubtitleIndex: 1}, {SubtitleIndex: 2}, {SubtitleIndex: 3}},
		EmptyTracks:           1,
		FailedTracks:          2,
		FailedEmbeddedIndices: []int{4, 5},
	}
	job.recordSourceFailure(LibrarySearchResult{Title: "partially broken media"}, errors.New("subtitle extraction failed"), result.FailedTracks)
	job.recordIndexResult(result)
	status := job.status()
	if status.Failed != 1 || status.FailedTracks != 2 || status.EmptyTracks != 1 || status.Skipped != 3 {
		t.Fatalf("unexpected track outcome status: %+v", status)
	}
}

func TestSubtitleEmbeddingContextExtractsBoundedCast(t *testing.T) {
	actor, role := "Actor", "Character"
	metadata := &components.Metadata{Title: "Film", Role: []components.Tag{{Tag: actor, Role: &role}}}
	context := subtitleEmbeddingContext(metadata)
	if !strings.Contains(context, "Title: Film") || !strings.Contains(context, "Actor as Character") {
		t.Fatalf("cast context = %q", context)
	}
	if len([]rune(context)) > 900 {
		t.Fatalf("cast context exceeds bound: %d", len([]rune(context)))
	}
}

func TestSubtitleEmbeddingContextFingerprintChangesWhenCastChanges(t *testing.T) {
	stream := SubtitleStream{Index: 0, Codec: "srt", Language: "English", Type: "text"}
	first := subtitleSourceFingerprint("movie", 1, 2, stream, "Title: Film\nCast: A as X")
	second := subtitleSourceFingerprint("movie", 1, 2, stream, "Title: Film\nCast: B as Y")
	if first == second {
		t.Fatal("cast change did not change fingerprint")
	}
}

func TestSubtitleSourceFingerprintIncludesSourceRevision(t *testing.T) {
	stream := SubtitleStream{Index: 0, Codec: "srt", Language: "English", Type: "text"}
	first := subtitleSourceFingerprint("movie", 1, 2, stream, "", "revision-a")
	unchanged := subtitleSourceFingerprint("movie", 1, 2, stream, "", "revision-a")
	changed := subtitleSourceFingerprint("movie", 1, 2, stream, "", "revision-b")
	if first != unchanged || first == changed {
		t.Fatalf("source revision fingerprints first=%q unchanged=%q changed=%q", first, unchanged, changed)
	}
}

func TestSubtitleSourceRevisionUsesPlexMetadataAndPartRevision(t *testing.T) {
	firstUpdated, secondUpdated := int64(100), int64(101)
	size := int64(1234)
	metadata := &components.Metadata{UpdatedAt: &firstUpdated, Media: []components.Media{{ID: 10, Part: []components.Part{{ID: 20, Key: "/library/parts/20/file", Size: &size}}}}}
	first := subtitleSourceRevision(metadata, 10, 20)
	metadata.Media[0].Part[0].Key = "/library/parts/20/replaced"
	metadata.UpdatedAt = &secondUpdated
	second := subtitleSourceRevision(metadata, 10, 20)
	if first == second {
		t.Fatalf("metadata revision did not change source revision: %q", first)
	}
}

func TestSubtitleIndexMediaURLRestoresAuthenticatedCallerCapability(t *testing.T) {
	upstream := httptest.NewServer(http.NotFoundHandler())
	defer upstream.Close()
	resolver, err := NewPlexResourceResolver(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	access := &PlexAccess{baseOrigin: upstream.URL, secretToken: "resource-token", accountToken: "caller-token", callerUUID: "caller", machineIdentifier: "machine"}
	resolver.mu.Lock()
	resolver.cache[plexAccessCacheKey{callerUUID: "caller", machineIdentifier: "machine", accountTokenHash: sha256.Sum256([]byte("caller-token"))}] = plexAccessCacheEntry{access: access, expires: time.Now().Add(time.Minute)}
	resolver.mu.Unlock()
	app := &Application{
		plexResources:     resolver,
		machineIdentifier: "machine",
	}
	ctx := ContextWithUser(ContextWithAuthToken(context.Background(), "caller-token"), User{Uuid: "caller"})
	url, release, err := app.subtitleIndexMediaURL(ctx, &components.Part{Key: "/library/parts/1"})
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	defer app.mediaProxy.Close()
	if strings.Contains(url, "X-Plex-Token") || !strings.HasPrefix(url, app.mediaProxy.URL()+"/plex-media/") {
		t.Fatalf("authenticated subtitle URL leaked credentials or bypassed proxy: %q", url)
	}
	app.mediaProxy.mu.Lock()
	for _, lease := range app.mediaProxy.items {
		if time.Until(lease.expires) <= 2*time.Minute {
			app.mediaProxy.mu.Unlock()
			t.Fatalf("index capability retained the short default TTL: %v", time.Until(lease.expires))
		}
	}
	app.mediaProxy.mu.Unlock()
}

func TestStreamingSectionTraversalCanExceedFormerCatalogCap(t *testing.T) {
	const total = 10001
	var pages int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start, _ := strconv.Atoi(r.URL.Query().Get("X-Plex-Container-Start"))
		pages++
		remaining := total - start
		count := indexLibraryPageSize
		if remaining < count {
			count = remaining
		}
		items := make([]components.Metadata, count)
		for i := range items {
			key := "movie-" + strconv.Itoa(start+i)
			items[i] = components.Metadata{Type: "movie", Title: key, RatingKey: &key}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"MediaContainer": map[string]any{"Metadata": items, "totalSize": total, "offset": start}})
	}))
	defer server.Close()
	app := &Application{}
	app.config.Plex.Host = server.URL
	seen := 0
	err := app.streamIndexLibrarySectionSources(context.Background(), "token", "1", "movie", func(items []components.Metadata) error {
		seen += len(items)
		return nil
	})
	if err != nil || seen != total || pages <= 100 {
		t.Fatalf("streamed=%d pages=%d err=%v", seen, pages, err)
	}
}

func TestScanSubtitleSearchHitAllowsNullableMetadata(t *testing.T) {
	hit, err := scanSubtitleSearchHit(func(dest ...any) error {
		values := []any{"movie-1", int64(1), int64(2), 0, "Movie", nil, nil, nil, nil, int64(100), int64(200), "dialogue", 0.9}
		for i := range dest {
			switch value := dest[i].(type) {
			case *string:
				*value = values[i].(string)
			case *int64:
				*value = values[i].(int64)
			case *int:
				*value = values[i].(int)
			case *float64:
				*value = values[i].(float64)
			case *sql.NullString:
				*value = sql.NullString{}
			case *sql.NullInt64:
				*value = sql.NullInt64{}
			default:
				return errors.New("unexpected scan destination")
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if hit.ShowTitle != "" || hit.Season != nil || hit.Episode != nil || hit.Year != nil || hit.Text != "dialogue" {
		t.Fatalf("nullable fields were not preserved: %+v", hit)
	}
}

func TestSharedSubtitleAuthorizationChecksEachSourceOnceBeforeFetch(t *testing.T) {
	candidates := []sharedSubtitleCandidate{
		{MachineIdentifier: "machine", SectionUUID: "uuid-1", SectionKey: "1", ScanID: "scan-1", RatingKey: "movie-1", MediaID: 10, PartID: 20, SubtitleIndex: 0},
		{MachineIdentifier: "machine", SectionUUID: "uuid-1", SectionKey: "1", ScanID: "scan-1", RatingKey: "movie-1", MediaID: 10, PartID: 20, SubtitleIndex: 1},
		{MachineIdentifier: "machine", SectionUUID: "uuid-1", SectionKey: "1", ScanID: "scan-1", RatingKey: "movie-2", MediaID: 11, PartID: 21, SubtitleIndex: 0},
	}
	checks, fetches := 0, 0
	hits, err := authorizeAndFetchSharedSubtitleHits(context.Background(), candidates,
		func(_ context.Context, candidate sharedSubtitleCandidate) (bool, error) {
			checks++
			return candidate.RatingKey == "movie-1", nil
		},
		func(_ context.Context, candidate sharedSubtitleCandidate) (subtitleSearchHit, error) {
			fetches++
			return subtitleSearchHit{RatingKey: candidate.RatingKey, Text: "authorized"}, nil
		})
	if err != nil || checks != 2 || fetches != 2 || len(hits) != 2 {
		t.Fatalf("checks=%d fetches=%d hits=%+v err=%v", checks, fetches, hits, err)
	}
}

func TestSharedSubtitleAuthorizationFailsClosedAndDoesNotFetchDeniedData(t *testing.T) {
	candidates := []sharedSubtitleCandidate{{MachineIdentifier: "machine", SectionUUID: "uuid-1", SectionKey: "1", ScanID: "scan-1", RatingKey: "denied", MediaID: 1, PartID: 2}}
	fetches := 0
	if _, err := authorizeAndFetchSharedSubtitleHits(context.Background(), candidates,
		func(_ context.Context, _ sharedSubtitleCandidate) (bool, error) {
			return false, errors.New("systemic failure")
		},
		func(_ context.Context, _ sharedSubtitleCandidate) (subtitleSearchHit, error) {
			fetches++
			return subtitleSearchHit{Text: "secret"}, nil
		}); err == nil || fetches != 0 {
		t.Fatalf("err=%v fetches=%d; authorization did not fail closed", err, fetches)
	}
	if _, err := authorizeAndFetchSharedSubtitleHits(context.Background(), candidates,
		func(_ context.Context, _ sharedSubtitleCandidate) (bool, error) { return false, nil },
		func(_ context.Context, _ sharedSubtitleCandidate) (subtitleSearchHit, error) {
			fetches++
			return subtitleSearchHit{Text: "secret"}, nil
		}); err != nil || fetches != 0 {
		t.Fatalf("err=%v fetches=%d; denied source reached fetch", err, fetches)
	}
}

func TestSharedLexicalCandidateIsAuthorizedBeforeTextFetch(t *testing.T) {
	candidate := sharedSubtitleCandidate{MachineIdentifier: "machine", SectionUUID: "uuid-1", SectionKey: "1", ScanID: "scan-1", RatingKey: "literal-secret", Tier: 0, Rank: 0}
	fetched := false
	hits, err := authorizeAndFetchSharedSubtitleCandidates(context.Background(), []sharedSubtitleCandidate{candidate}, func(_ context.Context, _ sharedSubtitleCandidate) (bool, error) {
		return false, nil
	}, func(_ context.Context, _ sharedSubtitleCandidate) (subtitleSearchHit, error) {
		fetched = true
		return subtitleSearchHit{Text: "secret"}, nil
	})
	if err != nil || fetched || len(hits) != 0 {
		t.Fatalf("unauthorized lexical candidate reached text fetch: err=%v fetched=%v hits=%v", err, fetched, hits)
	}
}

func TestScanSharedSubtitleCandidateDoesNotDecodeSubtitleText(t *testing.T) {
	candidate, err := scanSharedSubtitleCandidate(func(dest ...any) error {
		if len(dest) != 12 {
			return fmt.Errorf("scan requested %d fields, want uuid/scan identity fields", len(dest))
		}
		values := []any{"machine", "uuid-1", "1", "scan-1", "movie", "movie-1", int64(1), int64(2), 0, int64(100), int64(200), 0.9}
		for i := range dest {
			switch value := dest[i].(type) {
			case *string:
				*value = values[i].(string)
			case *int64:
				*value = values[i].(int64)
			case *int:
				*value = values[i].(int)
			case *float64:
				*value = values[i].(float64)
			default:
				return fmt.Errorf("unexpected scan destination %T", dest[i])
			}
		}
		return nil
	})
	if err != nil || candidate.RatingKey != "movie-1" || candidate.StartMs != 100 {
		t.Fatalf("candidate=%+v err=%v", candidate, err)
	}
}

func TestTEIProbeDimensions1024(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/embed" {
			t.Fatalf("unexpected path: %q", r.URL.Path)
		}
		// Return 1024-dim vector for Qwen3
		vec := make([]string, 1024)
		for i := range vec {
			vec[i] = "0.0"
		}
		_, _ = w.Write([]byte("[[" + strings.Join(vec, ",") + "]]"))
	}))
	defer server.Close()

	client, err := newEmbeddingClient(server.URL, embeddingsProviderTEI, "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	dim, err := client.probeDimensions(context.Background())
	if err != nil {
		t.Fatalf("probe failed: %v", err)
	}
	if dim != 1024 {
		t.Fatalf("probed dimension = %d, want 1024", dim)
	}
}

func TestOpenAIClientCustomModelAnd1024Dimensions(t *testing.T) {
	const customModel = "Qwen/Qwen3-Embedding-0.6B"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Model string   `json:"model"`
			Input []string `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		if req.Model != customModel {
			t.Fatalf("model = %q, want %q", req.Model, customModel)
		}
		vec := make([]float32, 1024)
		vec[0] = 0.42
		_ = json.NewEncoder(w).Encode(openAIEmbeddingResponse{
			Data: []openAIEmbeddingItem{{Index: 0, Embedding: vec}},
		})
	}))
	defer server.Close()

	client, err := newEmbeddingClient(server.URL, embeddingsProviderOpenAI, "nas-token", customModel, 1024)
	if err != nil {
		t.Fatal(err)
	}
	res, err := client.embed(context.Background(), []string{"test query"})
	if err != nil {
		t.Fatalf("embed failed: %v", err)
	}
	if len(res) != 1 || len(res[0]) != 1024 {
		t.Fatalf("expected 1 vector of length 1024, got len=%d", len(res[0]))
	}
	if res[0][0] != 0.42 {
		t.Fatalf("vector[0] = %f, want 0.42", res[0][0])
	}
}

func TestSubtitleSearchSchemaGenerationWithDimensions(t *testing.T) {
	schema768 := subtitleSearchSchema(768)
	if !strings.Contains(schema768, "embedding vector(768) NOT NULL") {
		t.Fatal("schema768 does not contain vector(768)")
	}

	schema1024 := subtitleSearchSchema(1024)
	if !strings.Contains(schema1024, "embedding vector(1024) NOT NULL") {
		t.Fatal("schema1024 does not contain vector(1024)")
	}
	// Verify both tables have 1024
	count := strings.Count(schema1024, "embedding vector(1024) NOT NULL")
	if count != 2 {
		t.Fatalf("expected 2 vector(1024) columns (chunks + shared_chunks), got %d", count)
	}
}

func TestSubtitleSearchSchemaIncludesBoundedLiteralSearchIndexes(t *testing.T) {
	schema := subtitleSearchSchema(768)
	wants := []string{
		"CREATE EXTENSION IF NOT EXISTS pg_trgm;",
		"CREATE INDEX IF NOT EXISTS subtitle_chunks_owner_idx ON subtitle_chunks (owner_uuid);",
		"CREATE INDEX IF NOT EXISTS subtitle_shared_chunks_machine_section_scan_idx ON subtitle_shared_chunks (machine_identifier, section_uuid, scan_id);",
		"CREATE INDEX IF NOT EXISTS subtitle_chunks_normalized_text_trgm_idx ON subtitle_chunks USING GIN (btrim(regexp_replace(lower(text), '[^[:alnum:]]+', ' ', 'g')) gin_trgm_ops);",
		"CREATE INDEX IF NOT EXISTS subtitle_shared_chunks_normalized_text_trgm_idx ON subtitle_shared_chunks USING GIN (btrim(regexp_replace(lower(text), '[^[:alnum:]]+', ' ', 'g')) gin_trgm_ops);",
	}
	for _, want := range wants {
		if !strings.Contains(schema, want) {
			t.Errorf("schema does not contain %q", want)
		}
	}
}

func TestSubtitleSourceFingerprintDimensionSensitivity(t *testing.T) {
	stream := SubtitleStream{Index: 0, Codec: "srt", Language: "English", Type: "text"}
	fp768 := subtitleSourceFingerprintWithDimensions("movie-1", 1, 2, stream, "context", 768, "rev-1")
	fp1024 := subtitleSourceFingerprintWithDimensions("movie-1", 1, 2, stream, "context", 1024, "rev-1")

	if fp768 == fp1024 {
		t.Fatalf("expected fingerprints to differ for different dimensions, both got: %s", fp768)
	}
}

func TestChunkSubtitleEntriesTimingBoundaries(t *testing.T) {
	t.Run("cues with gap within 15s are merged", func(t *testing.T) {
		entries := []SubtitleEntry{
			{Start: 1000, End: 5000, Text: "first sentence"},
			{Start: 15000, End: 19000, Text: "second sentence"}, // gap: 15000 - 5000 = 10000ms (10s <= 15s)
		}
		chunks := chunkSubtitleEntries(entries)
		if len(chunks) != 1 {
			t.Fatalf("expected 1 chunk, got %d: %+v", len(chunks), chunks)
		}
		if chunks[0].Start != 1000 || chunks[0].End != 19000 || chunks[0].Text != "first sentence\nsecond sentence" {
			t.Fatalf("unexpected chunk content: %+v", chunks[0])
		}
	})

	t.Run("cues with gap exceeding 15s are split", func(t *testing.T) {
		entries := []SubtitleEntry{
			{Start: 1000, End: 5000, Text: "first sentence"},
			{Start: 21000, End: 25000, Text: "second sentence"}, // gap: 21000 - 5000 = 16000ms (16s > 15s)
		}
		chunks := chunkSubtitleEntries(entries)
		if len(chunks) != 2 {
			t.Fatalf("expected 2 chunks, got %d: %+v", len(chunks), chunks)
		}
		if chunks[0].Start != 1000 || chunks[0].End != 5000 || chunks[0].Text != "first sentence" {
			t.Fatalf("unexpected first chunk: %+v", chunks[0])
		}
		if chunks[1].Start != 21000 || chunks[1].End != 25000 || chunks[1].Text != "second sentence" {
			t.Fatalf("unexpected second chunk: %+v", chunks[1])
		}
	})

	t.Run("cues within 60s total duration are merged", func(t *testing.T) {
		entries := []SubtitleEntry{
			{Start: 0, End: 10000, Text: "cue 1"},
			{Start: 12000, End: 22000, Text: "cue 2"},
			{Start: 24000, End: 34000, Text: "cue 3"},
			{Start: 36000, End: 46000, Text: "cue 4"},
			{Start: 48000, End: 58000, Text: "cue 5"}, // span 0 to 58000ms (58s <= 60s)
		}
		chunks := chunkSubtitleEntries(entries)
		if len(chunks) != 1 {
			t.Fatalf("expected 1 chunk, got %d: %+v", len(chunks), chunks)
		}
		if chunks[0].Start != 0 || chunks[0].End != 58000 {
			t.Fatalf("unexpected chunk timing: start=%d end=%d", chunks[0].Start, chunks[0].End)
		}
	})

	t.Run("cues exceeding 60s total duration are split", func(t *testing.T) {
		entries := []SubtitleEntry{
			{Start: 0, End: 20000, Text: "cue 1"},
			{Start: 25000, End: 45000, Text: "cue 2"}, // total span: 45s <= 60s
			{Start: 50000, End: 65000, Text: "cue 3"}, // gap is 5s, but total span from start 0 would be 65s > 60s
		}
		chunks := chunkSubtitleEntries(entries)
		if len(chunks) != 2 {
			t.Fatalf("expected 2 chunks due to 60s duration limit, got %d: %+v", len(chunks), chunks)
		}
		if chunks[0].Start != 0 || chunks[0].End != 45000 || chunks[0].Text != "cue 1\ncue 2" {
			t.Fatalf("unexpected first chunk: %+v", chunks[0])
		}
		if chunks[1].Start != 50000 || chunks[1].End != 65000 || chunks[1].Text != "cue 3" {
			t.Fatalf("unexpected second chunk: %+v", chunks[1])
		}
	})

	t.Run("single long cue exceeding 60s is preserved without synthetic timestamps", func(t *testing.T) {
		entries := []SubtitleEntry{
			{Start: 0, End: 90000, Text: "single long descriptive cue spanning 90 seconds"},
		}
		chunks := chunkSubtitleEntries(entries)
		if len(chunks) != 1 {
			t.Fatalf("expected 1 chunk for single cue, got %d", len(chunks))
		}
		if chunks[0].Start != 0 || chunks[0].End != 90000 {
			t.Fatalf("unexpected chunk boundaries: %+v", chunks[0])
		}
	})

	t.Run("overlapping cues preserve maximum end timestamp", func(t *testing.T) {
		entries := []SubtitleEntry{
			{Start: 1000, End: 8000, Text: "primary line"},
			{Start: 2000, End: 6000, Text: "overlapping shorter line"},
		}
		chunks := chunkSubtitleEntries(entries)
		if len(chunks) != 1 {
			t.Fatalf("expected 1 chunk, got %d: %+v", len(chunks), chunks)
		}
		if chunks[0].Start != 1000 || chunks[0].End != 8000 {
			t.Fatalf("unexpected chunk boundaries for overlapping cues: %+v", chunks[0])
		}
	})
}

func TestResolveQueryInstruction(t *testing.T) {
	custom := "Instruct: search\nQuery: "
	empty := ""

	tests := []struct {
		name       string
		cfg        SemanticSearchConfig
		dimensions int
		want       string
	}{
		{
			name: "explicit custom instruction",
			cfg: SemanticSearchConfig{
				QueryInstruction: &custom,
				EmbeddingsModel:  "BAAI/bge-base-en-v1.5",
			},
			dimensions: 768,
			want:       custom,
		},
		{
			name: "explicit empty instruction disables prefix even for bge",
			cfg: SemanticSearchConfig{
				QueryInstruction: &empty,
				EmbeddingsModel:  "BAAI/bge-base-en-v1.5",
			},
			dimensions: 768,
			want:       "",
		},
		{
			name: "implicit bge model defaults to bge instruction",
			cfg: SemanticSearchConfig{
				EmbeddingsModel: "BAAI/bge-base-en-v1.5",
			},
			dimensions: 768,
			want:       bgeQueryInstruction,
		},
		{
			name: "implicit qwen model defaults to empty instruction",
			cfg: SemanticSearchConfig{
				EmbeddingsModel: "Qwen/Qwen3-Embedding-0.6B",
			},
			dimensions: 1024,
			want:       "",
		},
		{
			name: "implicit default tei with 768 dimensions defaults to bge instruction",
			cfg: SemanticSearchConfig{
				EmbeddingsProvider: "tei",
			},
			dimensions: 768,
			want:       bgeQueryInstruction,
		},
		{
			name: "implicit default tei with 1024 dimensions defaults to empty instruction",
			cfg: SemanticSearchConfig{
				EmbeddingsProvider: "tei",
			},
			dimensions: 1024,
			want:       "",
		},
		{
			name: "openai provider with non-bge model defaults to empty instruction",
			cfg: SemanticSearchConfig{
				EmbeddingsProvider: "openai",
				EmbeddingsModel:    "text-embedding-3-small",
			},
			dimensions: 1536,
			want:       "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveQueryInstruction(tt.cfg, tt.dimensions)
			if got != tt.want {
				t.Fatalf("resolveQueryInstruction() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestNormalizeSubtitleEmbeddingBatchSizeAndConcurrency(t *testing.T) {
	if got := normalizeSubtitleEmbeddingBatchSize(0); got != defaultSubtitleEmbeddingBatchSize {
		t.Fatalf("expected default batch size %d, got %d", defaultSubtitleEmbeddingBatchSize, got)
	}
	if got := normalizeSubtitleEmbeddingBatchSize(-5); got != defaultSubtitleEmbeddingBatchSize {
		t.Fatalf("expected default batch size %d, got %d", defaultSubtitleEmbeddingBatchSize, got)
	}
	if got := normalizeSubtitleEmbeddingBatchSize(64); got != 64 {
		t.Fatalf("expected batch size 64, got %d", got)
	}
	if got := normalizeSubtitleEmbeddingBatchSize(1000); got != maxSubtitleEmbeddingBatchSize {
		t.Fatalf("expected capped batch size %d, got %d", maxSubtitleEmbeddingBatchSize, got)
	}

	if got := normalizeSubtitleEmbeddingConcurrency(0); got != defaultSubtitleEmbeddingConcurrency {
		t.Fatalf("expected default concurrency %d, got %d", defaultSubtitleEmbeddingConcurrency, got)
	}
	if got := normalizeSubtitleEmbeddingConcurrency(-2); got != defaultSubtitleEmbeddingConcurrency {
		t.Fatalf("expected default concurrency %d, got %d", defaultSubtitleEmbeddingConcurrency, got)
	}
	if got := normalizeSubtitleEmbeddingConcurrency(4); got != 4 {
		t.Fatalf("expected concurrency 4, got %d", got)
	}
	if got := normalizeSubtitleEmbeddingConcurrency(100); got != maxSubtitleEmbeddingConcurrency {
		t.Fatalf("expected capped concurrency %d, got %d", maxSubtitleEmbeddingConcurrency, got)
	}
}

func TestSubtitleSearchStoreQueryInput(t *testing.T) {
	var nilStore *subtitleSearchStore
	if got := nilStore.queryInput("test"); got != "test" {
		t.Fatalf("expected %q, got %q", "test", got)
	}

	emptyStore := &subtitleSearchStore{queryInstruction: ""}
	if got := emptyStore.queryInput("test"); got != "test" {
		t.Fatalf("expected %q, got %q", "test", got)
	}

	bgeStore := &subtitleSearchStore{queryInstruction: bgeQueryInstruction}
	want := bgeQueryInstruction + "test"
	if got := bgeStore.queryInput("test"); got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

func TestSharedSubtitleCandidateIdentityDeduplication(t *testing.T) {
	c1 := sharedSubtitleCandidate{
		MachineIdentifier: "pms-1",
		SectionUUID:       "sec-1",
		ScanID:            "scan-1",
		RatingKey:         "movie-1",
		MediaID:           10,
		PartID:            20,
		SubtitleIndex:     0,
		StartMs:           1000,
		EndMs:             2000,
		Tier:              2,
		Rank:              1,
	}
	c2 := sharedSubtitleCandidate{
		MachineIdentifier: "pms-1",
		SectionUUID:       "sec-1",
		ScanID:            "scan-2",
		RatingKey:         "movie-1",
		MediaID:           10,
		PartID:            20,
		SubtitleIndex:     0,
		StartMs:           1000,
		EndMs:             2000,
		Tier:              0,
		Rank:              0,
	}
	raw := []subtitleSearchCandidate{
		{Shared: c1, IsShared: true, Tier: c1.Tier, Rank: c1.Rank},
		{Shared: c2, IsShared: true, Tier: c2.Tier, Rank: c2.Rank},
	}
	merged := mergeSubtitleSearchCandidates(raw)
	if len(merged) == 0 || merged[0].Tier != 0 {
		t.Fatalf("expected merged candidates to prioritize Tier 0, got %+v", merged)
	}
}
