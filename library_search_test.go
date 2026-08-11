package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/LukeHagar/plexgo"
	"github.com/LukeHagar/plexgo/models/components"
	"github.com/gofiber/fiber/v3"
)

func TestSearchLibraryUsesCallerTokenFiltersAndRefetchesHubs(t *testing.T) {
	plex := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Plex-Token") != "user-token" {
			t.Errorf("Plex token = %q, want caller token", r.Header.Get("X-Plex-Token"))
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/hubs/search":
			if r.URL.Query().Get("query") != "space" {
				t.Errorf("search query = %q", r.URL.Query().Get("query"))
			}
			_, _ = io.WriteString(w, `{"MediaContainer":{"Hub":[{"Metadata":[
                {"ratingKey":"movie-1","type":"movie","title":"Space Movie","year":2024,"thumb":"/library/metadata/movie-1/thumb","Media":[{"id":10,"Part":[{"id":100,"key":"/library/parts/100/file"}]},{"id":11,"videoProfile":"main 10","videoResolution":"1080","videoCodec":"h264","Part":[{"id":1011,"duration":60000,"key":"/library/parts/1011/file","size":1234}]}]},
                {"ratingKey":"show-1","type":"show","title":"Not clip-capable"},
                {"ratingKey":"episode-1","type":"episode","title":"Pilot","grandparentTitle":"The Show","parentTitle":"Season 1","parentIndex":1,"index":1,"Media":[{"id":12,"Part":[{"id":102,"key":"/library/parts/102/file","duration":60000}]}]},
                {"ratingKey":"movie-2","type":"movie","title":"Needs details"}
            ]}]}}`)
		case "/library/metadata/movie-2":
			_, _ = io.WriteString(w, `{"MediaContainer":{"Metadata":[{"ratingKey":"movie-2","type":"movie","title":"Needs details","Media":[{"id":22,"duration":90000,"videoResolution":"4k","Part":[{"id":202,"key":"/library/parts/202/file","size":4567}]}]}]}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer plex.Close()
	config := Config{}
	config.Plex.Host = plex.URL
	app := &Application{config: config}
	app.plexUser = plexgo.New(plexgo.WithServerURL(plex.URL), plexgo.WithSecuritySource(app.plexSecurityUserToken))
	results, err := app.SearchLibrary(ContextWithAuthToken(context.Background(), "user-token"), " space ")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 4 {
		t.Fatalf("got %d results, want 4: %+v", len(results), results)
	}
	if results[0].RatingKey != "movie-1" || results[0].MediaID != 11 || results[0].PartID != 1011 || results[0].Duration != 60000 || results[0].FileSize != 1234 || results[0].VideoProfile != "main 10" {
		t.Fatalf("first result = %+v", results[0])
	}
	movieJSON, err := json.Marshal(results[0])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(movieJSON), `"videoProfile":"main 10"`) {
		t.Fatalf("search result omitted video profile: %s", movieJSON)
	}
	if results[1].RatingKey != "show-1" || results[1].Type != "show" || results[1].MediaID != 0 || results[1].PartID != 0 {
		t.Fatalf("show result = %+v", results[1])
	}
	showJSON, err := json.Marshal(results[1])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(showJSON), `"mediaId"`) || strings.Contains(string(showJSON), `"partId"`) {
		t.Fatalf("show result exposed playable IDs: %s", showJSON)
	}
	if results[2].RatingKey != "episode-1" || results[2].Type != "episode" || results[2].MediaID != 12 || results[2].PartID != 102 {
		t.Fatalf("direct episode result = %+v", results[2])
	}
	if results[3].RatingKey != "movie-2" || results[3].MediaID != 22 || results[3].PartID != 202 || results[3].Duration != 90000 {
		t.Fatalf("refetched result = %+v", results[3])
	}
}

func TestSearchLibraryRequiresCallerTokenAndValidQuery(t *testing.T) {
	app := &Application{}
	if _, err := app.SearchLibrary(context.Background(), "movie"); err == nil {
		t.Fatal("search without caller token was accepted")
	}
	ctx := ContextWithAuthToken(context.Background(), "user-token")
	if _, err := app.SearchLibrary(ctx, "\n"); err == nil {
		t.Fatal("control-character query was accepted")
	}
}

func TestSearchLibraryPropagatesSystemicDetailFailures(t *testing.T) {
	for _, testCase := range []struct {
		name string
		body string
	}{
		{name: "server error", body: "status"},
		{name: "malformed metadata", body: "malformed"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			plex := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/hubs/search" {
					_, _ = io.WriteString(w, `{"MediaContainer":{"Hub":[{"Metadata":[{"ratingKey":"movie-1","type":"movie","title":"Movie"}]}]}}`)
					return
				}
				if r.URL.Path == "/library/metadata/movie-1" {
					if testCase.body == "status" {
						w.WriteHeader(http.StatusBadGateway)
						return
					}
					_, _ = io.WriteString(w, "{not-json")
					return
				}
				http.NotFound(w, r)
			}))
			defer plex.Close()
			config := Config{}
			config.Plex.Host = plex.URL
			app := &Application{config: config}
			results, err := app.SearchLibrary(ContextWithAuthToken(context.Background(), "caller-token"), "movie")
			if err == nil || results != nil {
				t.Fatalf("results=%v err=%v; systemic detail failure returned partial success", results, err)
			}
		})
	}
}

func TestSearchLibraryDetailRefetchBudgetPreservesResolvedResults(t *testing.T) {
	plex := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/hubs/search" {
			var hubs strings.Builder
			hubs.WriteString(`{"MediaContainer":{"Hub":[{"Metadata":[`)
			for i := 1; i <= maxLibrarySearchRefetches+1; i++ {
				if i > 1 {
					hubs.WriteString(",")
				}
				_, _ = fmt.Fprintf(&hubs, `{"ratingKey":"abbreviated-%d","type":"movie","title":"Movie %d"}`, i, i)
			}
			hubs.WriteString(`]}]}}`)
			_, _ = io.WriteString(w, hubs.String())
			return
		}
		if strings.HasPrefix(r.URL.Path, "/library/metadata/abbreviated-") {
			key := strings.TrimPrefix(r.URL.Path, "/library/metadata/")
			_, _ = fmt.Fprintf(w, `{"MediaContainer":{"Metadata":[{"ratingKey":%q,"type":"movie","title":%q,"Media":[{"id":1,"Part":[{"id":2,"duration":1000,"key":"/library/parts/2/file"}]}]}]}}`, key, key)
			return
		}
		http.NotFound(w, r)
	}))
	defer plex.Close()
	config := Config{}
	config.Plex.Host = plex.URL
	app := &Application{config: config}
	results, err := app.SearchLibrary(ContextWithAuthToken(context.Background(), "caller-token"), "movies")
	if err != nil {
		t.Fatalf("search returned detail-budget error: %v", err)
	}
	if len(results) != maxLibrarySearchRefetches {
		t.Fatalf("resolved results = %d, want %d: %+v", len(results), maxLibrarySearchRefetches, results)
	}
	if results[0].RatingKey != "abbreviated-1" || results[len(results)-1].RatingKey != fmt.Sprintf("abbreviated-%d", maxLibrarySearchRefetches) {
		t.Fatalf("unexpected resolved result prefix: first=%q last=%q", results[0].RatingKey, results[len(results)-1].RatingKey)
	}
}

func TestLibrarySourceSelectionBindsPlayablePartForPreviewAndRender(t *testing.T) {
	duration := 120000
	metadata := &components.Metadata{
		RatingKey: stringPointer("movie-1"), Type: "movie", Title: "Library movie",
		Media: []components.Media{
			{ID: 10, Duration: &duration, VideoProfile: stringPointer("main 10"), Part: []components.Part{{ID: 100, Key: "/bad"}}},
			{ID: 11, Duration: &duration, Part: []components.Part{{ID: 101, Key: "/library/parts/101/file"}}},
		},
	}
	media, part, err := selectLibraryMetadataSource(metadata, 101, true)
	if err != nil || media.ID != 11 || part.ID != 101 {
		t.Fatalf("library source = media=%v part=%v err=%v", media, part, err)
	}
	request := RenderJobCreateRequest{RatingKey: "movie-1", MediaID: 101, FromMs: 0, ToMs: 1000, SubtitleIndex: -1}
	spec, err := validateRenderJobRequestWithMetadataAndItem(request, User{Uuid: "user-a"}, nil, metadata.Media, previewSessionSelection{mediaID: 11, partID: 101, selected: 101}, metadata)
	if err != nil {
		t.Fatal(err)
	}
	if spec.PartKey != "/library/parts/101/file" || spec.Title != "Library movie" {
		t.Fatalf("render source snapshot = %+v", spec)
	}
	main10Media, main10Part, err := selectLibraryMetadataSource(metadata, 100, true)
	if err != nil || main10Media.ID != 10 || main10Part.ID != 100 {
		t.Fatalf("main-10 explicit source was not accepted: media=%v part=%v err=%v", main10Media, main10Part, err)
	}
}

func TestSubtitleCacheIsolatedByValidatedCaller(t *testing.T) {
	var streamRequests int
	plex := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/library/metadata/movie-1":
			_, _ = io.WriteString(w, `{"MediaContainer":{"Metadata":[{"ratingKey":"movie-1","type":"movie","title":"Movie","Media":[{"id":10,"Part":[{"id":200,"key":"/library/parts/200/file","Stream":[{"streamType":3,"key":"/library/streams/200","codec":"srt"}]}]}]}]}}`)
		case "/library/streams/200":
			streamRequests++
			w.Header().Set("Content-Type", "text/plain")
			_, _ = io.WriteString(w, "1\n00:00:01,000 --> 00:00:02,000\ncaller-specific\n")
		default:
			http.NotFound(w, r)
		}
	}))
	defer plex.Close()
	config := Config{}
	config.Plex.Host = plex.URL
	app := &Application{config: config, subtitleCache: newSubtitleCache(8)}
	app.plexUser = plexgo.New(plexgo.WithServerURL(plex.URL), plexgo.WithSecuritySource(app.plexSecurityUserToken))
	for _, user := range []User{{Uuid: "user-a"}, {Uuid: "user-b"}} {
		ctx := ContextWithUser(ContextWithAuthToken(context.Background(), user.Uuid+"-token"), user)
		entries, err := app.GetSubtitleEntries(ctx, "movie-1", "200", 0)
		if err != nil || len(entries) != 1 || entries[0].Text != "caller-specific" {
			t.Fatalf("caller %s subtitle result = %+v, %v", user.Uuid, entries, err)
		}
	}
	if streamRequests != 2 {
		t.Fatalf("subtitle cache crossed callers; stream requests = %d, want 2", streamRequests)
	}
}

func TestExplicitPartRenderSkipsSessionStatusAndUsesCallerToken(t *testing.T) {
	plex := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/status/sessions" {
			t.Fatal("explicit library render called /status/sessions")
		}
		if r.Header.Get("X-Plex-Token") != "caller-token" {
			t.Errorf("metadata token = %q, want caller-token", r.Header.Get("X-Plex-Token"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"MediaContainer":{"Metadata":[{"ratingKey":"movie-1","type":"movie","title":"Library movie","Media":[{"id":11,"duration":60000,"Part":[{"id":101,"key":"/library/parts/101/file","duration":60000}]}]}]}}`)
	}))
	defer plex.Close()
	manager, err := newRenderJobManager(t.TempDir(), writeRenderOutput)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.stopAndWait()
	config := Config{}
	config.Plex.Host = plex.URL
	config.Plex.Token = "admin-token"
	application := &Application{config: config, plexUser: nil, renderJobs: manager}
	application.plexUser = plexgo.New(plexgo.WithServerURL(plex.URL), plexgo.WithSecuritySource(application.plexSecurityUserToken))
	api := &API{config: config, app: application}
	httpApp := fiber.New()
	httpApp.Post("/render", func(ctx fiber.Ctx) error {
		userCtx := ContextWithUser(ContextWithAuthToken(context.Background(), "caller-token"), User{Uuid: "user-a"})
		ctx.SetUserContext(userCtx)
		return api.createRenderJob(ctx)
	})
	request := httptest.NewRequest(http.MethodPost, "/render", strings.NewReader(`{"ratingKey":"movie-1","mediaId":11,"partId":101,"fromMs":0,"toMs":1000}`))
	request.Header.Set("Content-Type", "application/json")
	response, err := httpApp.Test(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("library render status = %d body=%s", response.StatusCode, body)
	}
}

func TestMultipartSourceWithoutPartDurationIsRejected(t *testing.T) {
	duration := 2000
	metadata := &components.Metadata{RatingKey: stringPointer("movie-1"), Type: "movie", Title: "Movie", Media: []components.Media{
		{ID: 1, Part: []components.Part{{ID: 2, Key: "/a"}, {ID: 3, Key: "/b"}}},
		{ID: 4, Part: []components.Part{{ID: 5, Duration: &duration, Key: "/later"}}},
	}}
	media, part, err := resolveLibraryMetadataSource(metadata, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	if media.ID != 1 || part.ID != 2 {
		t.Fatalf("explicit source resolved to later candidate: media=%d part=%d", media.ID, part.ID)
	}
	if _, err := selectedSourceDuration(media, part); err == nil {
		t.Fatal("explicit unsafe part was accepted despite a later valid version")
	}
}

func stringPointer(value string) *string { return &value }

func newHierarchyFixture(t *testing.T) *Application {
	t.Helper()
	plex := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Plex-Token"); got != "caller-token" {
			t.Errorf("%s token = %q, want caller-token", r.URL.Path, got)
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/library/metadata/show-1":
			_, _ = io.WriteString(w, `{"MediaContainer":{"Metadata":[{"ratingKey":"show-1","type":"show","title":"The Show","thumb":"/library/metadata/show-1/thumb"}]}}`)
		case "/library/metadata/show-1/children":
			_, _ = io.WriteString(w, `{"MediaContainer":{"Metadata":[{"ratingKey":"season-1","type":"season","title":"Season 1","index":1,"parentRatingKey":"show-1","parentTitle":"The Show","thumb":"/library/metadata/season-1/thumb","Media":[{"id":999,"Part":[{"id":998,"key":"/library/parts/998/file"}]}]}]}}`)
		case "/library/metadata/season-1":
			_, _ = io.WriteString(w, `{"MediaContainer":{"Metadata":[{"ratingKey":"season-1","type":"season","title":"Season 1","index":1,"parentRatingKey":"show-1","parentTitle":"The Show"}]}}`)
		case "/library/metadata/season-1/children":
			_, _ = io.WriteString(w, `{"MediaContainer":{"Metadata":[{"ratingKey":"episode-1","type":"episode","title":"Pilot","index":2,"parentIndex":1,"parentRatingKey":"season-1","parentTitle":"Season 1","grandparentRatingKey":"show-1","grandparentTitle":"The Show"}]}}`)
		case "/library/metadata/episode-1":
			_, _ = io.WriteString(w, `{"MediaContainer":{"Metadata":[{"ratingKey":"episode-1","type":"episode","title":"Pilot","duration":180000,"index":2,"parentIndex":1,"parentRatingKey":"season-1","parentTitle":"Season 1","grandparentRatingKey":"show-1","grandparentTitle":"The Show","Media":[{"id":21,"Part":[{"id":31,"accessible":true,"exists":true,"key":"/library/parts/31/file"},{"id":32,"accessible":true,"exists":true,"key":"/library/parts/32/file"}]},{"id":22,"duration":120000,"videoProfile":"main 10","Part":[{"id":33,"duration":60000,"accessible":true,"exists":true,"key":"/library/parts/33/file","size":42}]}]}]}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(plex.Close)
	config := Config{}
	config.Plex.Host = plex.URL
	app := &Application{config: config}
	app.plexUser = plexgo.New(plexgo.WithServerURL(plex.URL), plexgo.WithSecuritySource(app.plexSecurityUserToken))
	return app
}

func TestLibraryMetadataChildrenShowReturnsNonPlayableSeasons(t *testing.T) {
	app := newHierarchyFixture(t)
	seasons, err := app.GetLibraryMetadataChildren(ContextWithAuthToken(context.Background(), "caller-token"), "show-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(seasons) != 1 || seasons[0].Type != "season" || seasons[0].RatingKey != "season-1" || seasons[0].SeasonNumber == nil || *seasons[0].SeasonNumber != 1 {
		t.Fatalf("season results = %+v", seasons)
	}
	if seasons[0].MediaID != 0 || seasons[0].PartID != 0 {
		t.Fatalf("season became playable: %+v", seasons[0])
	}
	body, err := json.Marshal(seasons)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "/library/parts/998/file") {
		t.Fatalf("season response exposed a raw file path: %s", body)
	}
}

func TestLibraryMetadataChildrenSeasonReturnsPlayableEpisodesWithoutRawPaths(t *testing.T) {
	app := newHierarchyFixture(t)
	episodes, err := app.GetLibraryMetadataChildren(ContextWithAuthToken(context.Background(), "caller-token"), "season-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(episodes) != 1 {
		t.Fatalf("episode results = %+v", episodes)
	}
	episode := episodes[0]
	if episode.Type != "episode" || episode.RatingKey != "episode-1" || episode.MediaID != 22 || episode.PartID != 33 || episode.Duration != 60000 || episode.SeasonNumber == nil || *episode.SeasonNumber != 1 || episode.EpisodeNumber == nil || *episode.EpisodeNumber != 2 {
		t.Fatalf("normalized episode = %+v", episode)
	}
	body, err := json.Marshal([]LibrarySearchResult{episode})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "/library/parts/31/file") || strings.Contains(string(body), "/library/parts/33/file") {
		t.Fatalf("hierarchy response exposed a raw file path: %s", body)
	}
}

func TestLibraryMetadataChildrenTrustsValidatedListingContainment(t *testing.T) {
	plex := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/library/metadata/season-1":
			_, _ = io.WriteString(w, `{"MediaContainer":{"Metadata":[{"ratingKey":"season-1","type":"season","title":"Season 1","index":1}]}}`)
		case "/library/metadata/season-1/children":
			_, _ = io.WriteString(w, `{"MediaContainer":{"Metadata":[{"ratingKey":"episode-complete","type":"episode","title":"Complete listing","parentRatingKey":"season-1","index":0,"Media":[{"id":103,"Part":[{"id":203,"duration":1000,"key":"/library/parts/203/file"}]}]},{"ratingKey":"episode-missing-parent","type":"episode","title":"Missing parent","parentRatingKey":"season-1","index":1},{"ratingKey":"episode-different-parent","type":"episode","title":"Different parent","parentRatingKey":"season-1","index":2},{"ratingKey":"episode-invalid-listing-parent","type":"episode","title":"Invalid listing parent","parentRatingKey":"other-season","index":3}]}}`)
		case "/library/metadata/episode-missing-parent":
			_, _ = io.WriteString(w, `{"MediaContainer":{"Metadata":[{"ratingKey":"episode-missing-parent","type":"episode","title":"Missing parent","Media":[{"id":101,"Part":[{"id":201,"duration":1000,"key":"/library/parts/201/file"}]}]}]}}`)
		case "/library/metadata/episode-different-parent":
			_, _ = io.WriteString(w, `{"MediaContainer":{"Metadata":[{"ratingKey":"episode-different-parent","type":"episode","title":"Different parent","parentRatingKey":"another-season","Media":[{"id":102,"Part":[{"id":202,"duration":1000,"key":"/library/parts/202/file"}]}]}]}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer plex.Close()
	config := Config{}
	config.Plex.Host = plex.URL
	app := &Application{config: config}
	results, err := app.GetLibraryMetadataChildren(ContextWithAuthToken(context.Background(), "caller-token"), "season-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 3 {
		t.Fatalf("listing containment results = %+v, want three valid episodes", results)
	}
	if results[0].RatingKey != "episode-complete" || results[1].RatingKey != "episode-missing-parent" || results[2].RatingKey != "episode-different-parent" {
		t.Fatalf("unexpected listing containment results = %+v", results)
	}
}

func TestLibraryMetadataChildrenSkipsInaccessibleEpisodesAndRejectsMovies(t *testing.T) {
	plex := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/library/metadata/season-1":
			_, _ = io.WriteString(w, `{"MediaContainer":{"Metadata":[{"ratingKey":"season-1","type":"season","title":"Season 1","index":1}]}}`)
		case "/library/metadata/season-1/children":
			_, _ = io.WriteString(w, `{"MediaContainer":{"Metadata":[{"ratingKey":"episode-bad","type":"episode","title":"Unavailable","index":1},{"ratingKey":"episode-good","type":"episode","title":"Available","index":2}]}}`)
		case "/library/metadata/episode-bad":
			_, _ = io.WriteString(w, `{"MediaContainer":{"Metadata":[{"ratingKey":"episode-bad","type":"episode","title":"Unavailable","Media":[{"id":41,"Part":[{"id":51,"accessible":false,"key":"/private/unavailable.mkv"}]}]}]}}`)
		case "/library/metadata/episode-good":
			_, _ = io.WriteString(w, `{"MediaContainer":{"Metadata":[{"ratingKey":"episode-good","type":"episode","title":"Available","index":2,"Media":[{"id":42,"Part":[{"id":52,"duration":1000,"key":"/library/parts/52/file"}]}]}]}}`)
		case "/library/metadata/movie-1":
			_, _ = io.WriteString(w, `{"MediaContainer":{"Metadata":[{"ratingKey":"movie-1","type":"movie","title":"Movie"}]}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer plex.Close()
	config := Config{}
	config.Plex.Host = plex.URL
	app := &Application{config: config}
	app.plexUser = plexgo.New(plexgo.WithServerURL(plex.URL), plexgo.WithSecuritySource(app.plexSecurityUserToken))
	ctx := ContextWithAuthToken(context.Background(), "caller-token")

	episodes, err := app.GetLibraryMetadataChildren(ctx, "season-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(episodes) != 1 || episodes[0].RatingKey != "episode-good" || episodes[0].MediaID != 42 || episodes[0].PartID != 52 {
		t.Fatalf("inaccessible episode was not filtered: %+v", episodes)
	}
	if _, err := app.GetLibraryMetadataChildren(ctx, "movie-1"); err == nil {
		t.Fatal("movie was accepted as a navigable hierarchy parent")
	}
}

func TestCallerLibraryRequestsDoNotForwardTokensAcrossRedirects(t *testing.T) {
	var targetRequests int
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetRequests++
		if got := r.Header.Get("X-Plex-Token"); got != "" {
			t.Errorf("redirect target received caller token %q", got)
		}
		http.Error(w, "unexpected redirect follow", http.StatusInternalServerError)
	}))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/redirect-target", http.StatusFound)
	}))
	defer source.Close()

	config := Config{}
	config.Plex.Host = source.URL
	app := &Application{config: config}
	ctx := ContextWithAuthToken(context.Background(), "caller-token")
	if _, err := app.SearchLibrary(ctx, "space"); err == nil {
		t.Fatal("redirecting search unexpectedly succeeded")
	}
	if _, err := app.GetLibraryMetadataChildren(ctx, "show-1"); err == nil {
		t.Fatal("redirecting metadata unexpectedly succeeded")
	}
	if targetRequests != 0 {
		t.Fatalf("redirect target received %d requests; caller token crossed origin", targetRequests)
	}
}

func TestLibraryMetadataChildrenPropagatesSystemicFailures(t *testing.T) {
	for _, testCase := range []struct {
		name string
		body func(http.ResponseWriter, *http.Request)
	}{
		{name: "server error", body: func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusBadGateway)
		}},
		{name: "malformed JSON", body: func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, "{not-json")
		}},
		{name: "timeout", body: func(w http.ResponseWriter, r *http.Request) {
			<-r.Context().Done()
		}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			plex := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/library/metadata/show-1" {
					w.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(w, `{"MediaContainer":{"Metadata":[{"ratingKey":"show-1","type":"show","title":"Show"}]}}`)
					return
				}
				if r.URL.Path == "/library/metadata/show-1/children" {
					testCase.body(w, r)
					return
				}
				http.NotFound(w, r)
			}))
			defer plex.Close()
			config := Config{}
			config.Plex.Host = plex.URL
			app := &Application{config: config}
			ctx := ContextWithAuthToken(context.Background(), "caller-token")
			if testCase.name == "timeout" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, 20*time.Millisecond)
				defer cancel()
			}
			results, err := app.GetLibraryMetadataChildren(ctx, "show-1")
			if err == nil || results != nil {
				t.Fatalf("results=%v err=%v; systemic failure returned partial success", results, err)
			}
			if testCase.name == "timeout" && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("timeout error = %v", err)
			}
		})
	}
}

func TestLibraryMetadataChildrenRejectsOversizedPagination(t *testing.T) {
	plex := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/library/metadata/show-1" {
			_, _ = io.WriteString(w, `{"MediaContainer":{"Metadata":[{"ratingKey":"show-1","type":"show","title":"Show"}]}}`)
			return
		}
		if r.URL.Path == "/library/metadata/show-1/children" {
			_, _ = io.WriteString(w, `{"MediaContainer":{"totalSize":101,"offset":0,"size":100,"Metadata":[]}}`)
			return
		}
		http.NotFound(w, r)
	}))
	defer plex.Close()
	config := Config{}
	config.Plex.Host = plex.URL
	app := &Application{config: config}
	results, err := app.GetLibraryMetadataChildren(ContextWithAuthToken(context.Background(), "caller-token"), "show-1")
	if err == nil || results != nil {
		t.Fatalf("oversized page results=%v err=%v; expected explicit rejection", results, err)
	}
	var validationErr *sourceValidationError
	if !errors.As(err, &validationErr) {
		t.Fatalf("oversized page error = %T %v, want validation error", err, err)
	}
}

func TestLibraryPlayableMetadataRejectsMalformedIdentityAndIDs(t *testing.T) {
	duration := 1000
	goodPart := &components.Part{ID: 2, Duration: &duration, Key: "/library/parts/2/file"}
	tests := []struct {
		name  string
		item  *components.Metadata
		media *components.Media
		part  *components.Part
	}{
		{name: "wrong rating key", item: &components.Metadata{RatingKey: stringPointer("other"), Type: "movie", Title: "Movie"}, media: &components.Media{ID: 1}, part: goodPart},
		{name: "wrong type", item: &components.Metadata{RatingKey: stringPointer("movie-1"), Type: "show", Title: "Show"}, media: &components.Media{ID: 1}, part: goodPart},
		{name: "missing title", item: &components.Metadata{RatingKey: stringPointer("movie-1"), Type: "movie"}, media: &components.Media{ID: 1}, part: goodPart},
		{name: "zero media ID", item: &components.Metadata{RatingKey: stringPointer("movie-1"), Type: "movie", Title: "Movie"}, media: &components.Media{ID: 0}, part: goodPart},
		{name: "zero part ID", item: &components.Metadata{RatingKey: stringPointer("movie-1"), Type: "movie", Title: "Movie"}, media: &components.Media{ID: 1}, part: &components.Part{Key: "/library/parts/0/file"}},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			if validPlayableLibraryMetadata(testCase.item, "movie-1", testCase.media, testCase.part) {
				t.Fatal("malformed metadata was accepted as playable")
			}
			if _, _, err := resolveLibraryMetadataSource(testCase.item, 0, 0); err == nil {
				t.Fatal("canonical resolver accepted malformed metadata")
			}
		})
	}
}

func TestLibraryHierarchyAPIAuthStatusAndSafeErrorEnvelope(t *testing.T) {
	var seenTokens []string
	plex := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenTokens = append(seenTokens, r.Header.Get("X-Plex-Token"))
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/library/metadata/show-1" {
			_, _ = io.WriteString(w, `{"MediaContainer":{"Metadata":[{"ratingKey":"show-1","type":"show","title":"Show"}]}}`)
			return
		}
		if r.URL.Path == "/library/metadata/show-1/children" {
			w.WriteHeader(http.StatusBadGateway)
			_, _ = io.WriteString(w, "upstream detail must not leak")
			return
		}
		http.NotFound(w, r)
	}))
	defer plex.Close()
	config := Config{}
	config.Plex.Host = plex.URL
	application := &Application{config: config}
	api := &API{app: application}

	unauthenticated := fiber.New()
	unauthenticated.Get("/library/metadata/:ratingKey/children", api.getLibraryMetadataChildren)
	unauthenticatedResponse, err := unauthenticated.Test(httptest.NewRequest(http.MethodGet, "/library/metadata/show-1/children", nil))
	if err != nil {
		t.Fatal(err)
	}
	if unauthenticatedResponse.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d, want 401", unauthenticatedResponse.StatusCode)
	}
	_ = unauthenticatedResponse.Body.Close()

	authenticated := fiber.New()
	authenticated.Get("/library/metadata/:ratingKey/children", func(ctx fiber.Ctx) error {
		ctx.SetUserContext(ContextWithUser(ContextWithAuthToken(context.Background(), "caller-a"), User{Uuid: "user-a"}))
		return api.getLibraryMetadataChildren(ctx)
	})
	response, err := authenticated.Test(httptest.NewRequest(http.MethodGet, "/library/metadata/show-1/children", nil))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("systemic hierarchy status = %d, want 503", response.StatusCode)
	}
	var envelope struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Error.Code != "metadata_unavailable" || strings.Contains(envelope.Error.Message, "upstream detail") {
		t.Fatalf("unsafe hierarchy error envelope = %+v", envelope.Error)
	}
	if len(seenTokens) != 2 || seenTokens[0] != "caller-a" || seenTokens[1] != "caller-a" {
		t.Fatalf("hierarchy calls used unexpected tokens: %v", seenTokens)
	}
}

func TestLibraryHierarchyUsesCurrentCallerTokenForEachRequest(t *testing.T) {
	var seenTokens []string
	plex := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenTokens = append(seenTokens, r.Header.Get("X-Plex-Token"))
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/library/metadata/show-1" {
			_, _ = io.WriteString(w, `{"MediaContainer":{"Metadata":[{"ratingKey":"show-1","type":"show","title":"Show"}]}}`)
			return
		}
		if r.URL.Path == "/library/metadata/show-1/children" {
			_, _ = io.WriteString(w, `{"MediaContainer":{"totalSize":0,"offset":0,"Metadata":[]}}`)
			return
		}
		http.NotFound(w, r)
	}))
	defer plex.Close()
	config := Config{}
	config.Plex.Host = plex.URL
	app := &Application{config: config}
	for _, token := range []string{"caller-a", "caller-b"} {
		if results, err := app.GetLibraryMetadataChildren(ContextWithAuthToken(context.Background(), token), "show-1"); err != nil || len(results) != 0 {
			t.Fatalf("caller %s results=%v err=%v", token, results, err)
		}
	}
	if len(seenTokens) != 4 || seenTokens[0] != "caller-a" || seenTokens[1] != "caller-a" || seenTokens[2] != "caller-b" || seenTokens[3] != "caller-b" {
		t.Fatalf("cross-caller token sequence = %v", seenTokens)
	}
}
