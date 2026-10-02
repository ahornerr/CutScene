package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"unicode"

	pgx "github.com/jackc/pgx/v5"
	pgvector "github.com/pgvector/pgvector-go"
)

// Subtitle search queries.
//
// Turns a query into embedding vectors, retrieves and authorises candidates
// from the per-user and shared corpora, merges and diversifies them, and maps
// hits back onto Plex sources.

type subtitleSearchHit struct {
	RatingKey     string  `json:"ratingKey"`
	MediaID       int64   `json:"mediaId"`
	PartID        int64   `json:"partId"`
	SubtitleIndex int     `json:"subtitleIndex"`
	Title         string  `json:"title"`
	ShowTitle     string  `json:"showTitle,omitempty"`
	Season        *int    `json:"season,omitempty"`
	Episode       *int    `json:"episode,omitempty"`
	Year          *int    `json:"year,omitempty"`
	StartMs       int64   `json:"startMs"`
	EndMs         int64   `json:"endMs"`
	Text          string  `json:"text"`
	Score         float64 `json:"score"`
}

type subtitleSearchCandidate struct {
	Hit      subtitleSearchHit
	Shared   sharedSubtitleCandidate
	Tier     int // 0 literal, 1 phrase, 2 semantic
	Rank     int
	IsShared bool
}

type subtitleSearchIdentity struct {
	MachineIdentifier string
	SectionUUID       string
	ScanID            string
	RatingKey         string
	MediaID           int64
	PartID            int64
	SubtitleIndex     int
	StartMs           int64
	EndMs             int64
}

func subtitleSearchIdentityForHit(hit subtitleSearchHit) subtitleSearchIdentity {
	return subtitleSearchIdentity{RatingKey: hit.RatingKey, MediaID: hit.MediaID, PartID: hit.PartID, SubtitleIndex: hit.SubtitleIndex, StartMs: hit.StartMs, EndMs: hit.EndMs}
}

func subtitleSearchIdentityForCandidate(candidate subtitleSearchCandidate) subtitleSearchIdentity {
	if candidate.IsShared {
		return subtitleSearchIdentity{MachineIdentifier: candidate.Shared.MachineIdentifier, SectionUUID: candidate.Shared.SectionUUID, ScanID: candidate.Shared.ScanID, RatingKey: candidate.Shared.RatingKey, MediaID: candidate.Shared.MediaID, PartID: candidate.Shared.PartID, SubtitleIndex: candidate.Shared.SubtitleIndex, StartMs: candidate.Shared.StartMs, EndMs: candidate.Shared.EndMs}
	}
	return subtitleSearchIdentityForHit(candidate.Hit)
}

func normalizeSubtitleSearchLiteral(value string) string {
	var out strings.Builder
	for _, r := range strings.ToLower(value) {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			out.WriteRune(r)
		} else {
			out.WriteByte(' ')
		}
	}
	return strings.Join(strings.Fields(out.String()), " ")
}

func mergeSubtitleSearchCandidates(candidates []subtitleSearchCandidate) []subtitleSearchCandidate {
	best := make(map[subtitleSearchIdentity]subtitleSearchCandidate, len(candidates))
	for _, candidate := range candidates {
		identity := subtitleSearchIdentityForCandidate(candidate)
		previous, ok := best[identity]
		if !ok || candidate.Tier < previous.Tier || (candidate.Tier == previous.Tier && candidate.Rank < previous.Rank) {
			best[identity] = candidate
		}
	}
	merged := make([]subtitleSearchCandidate, 0, len(best))
	for _, candidate := range best {
		merged = append(merged, candidate)
	}
	sort.SliceStable(merged, func(i, j int) bool {
		if merged[i].Tier != merged[j].Tier {
			return merged[i].Tier < merged[j].Tier
		}
		if merged[i].Rank != merged[j].Rank {
			return merged[i].Rank < merged[j].Rank
		}
		left := subtitleSearchIdentityForCandidate(merged[i])
		right := subtitleSearchIdentityForCandidate(merged[j])
		return fmt.Sprintf("%s|%s|%s|%d|%d|%d|%d|%d", left.MachineIdentifier, left.SectionUUID, left.RatingKey, left.MediaID, left.PartID, left.SubtitleIndex, left.StartMs, left.EndMs) < fmt.Sprintf("%s|%s|%s|%d|%d|%d|%d|%d", right.MachineIdentifier, right.SectionUUID, right.RatingKey, right.MediaID, right.PartID, right.SubtitleIndex, right.StartMs, right.EndMs)
	})
	return merged
}

func diversifyHybridSubtitleSearchCandidates(candidates []subtitleSearchCandidate, limit int) []subtitleSearchCandidate {
	if limit <= 0 {
		return []subtitleSearchCandidate{}
	}
	// Keep exact literal results protected from semantic diversity. The small
	// literal allowances prevent a broken/duplicated index from consuming the
	// entire result window, while still allowing more than one matching cue.
	const (
		literalSourceAllowance = 3
		literalTitleAllowance  = 5
		remainderSourceQuota   = 3
		remainderTitleQuota    = 5
	)
	result := make([]subtitleSearchCandidate, 0, minInt(limit, len(candidates)))
	literalSources := make(map[string]int)
	literalTitles := make(map[string]int)
	remainderSources := make(map[string]int)
	remainderTitles := make(map[string]int)

	selectCandidate := func(candidate subtitleSearchCandidate, sourceCounts, titleCounts map[string]int, sourceQuota, titleQuota int) bool {
		if len(result) >= limit {
			return false
		}
		sourceKey := subtitleSearchCandidateSourceKey(candidate)
		titleKey := subtitleSearchCandidateTitleKey(candidate)
		if sourceCounts[sourceKey] >= sourceQuota || titleCounts[titleKey] >= titleQuota {
			return false
		}
		result = append(result, candidate)
		sourceCounts[sourceKey]++
		titleCounts[titleKey]++
		return true
	}

	// The merged pool is tier/rank ordered. Walk each tier in that order and
	// keep scanning after a quota rejection so a later title can fill the slot.
	for _, candidate := range candidates {
		if candidate.Tier != 0 {
			continue
		}
		selectCandidate(candidate, literalSources, literalTitles, literalSourceAllowance, literalTitleAllowance)
		if len(result) >= limit {
			return result
		}
	}
	for tier := 1; tier <= 2 && len(result) < limit; tier++ {
		for _, candidate := range candidates {
			if candidate.Tier != tier {
				continue
			}
			selectCandidate(candidate, remainderSources, remainderTitles, remainderSourceQuota, remainderTitleQuota)
			if len(result) >= limit {
				return result
			}
		}
	}
	return result
}

// sharedSubtitleCandidate deliberately contains no presentation fields.  The
// shared corpus is untrusted with respect to the current caller, so the vector
// query may disclose only the coordinates needed to authorize a candidate.
type sharedSubtitleCandidate struct {
	MachineIdentifier string
	SectionUUID       string
	SectionKey        string
	ScanID            string
	SectionType       string
	RatingKey         string
	MediaID           int64
	PartID            int64
	SubtitleIndex     int
	StartMs           int64
	EndMs             int64
	Score             float64
	Tier              int
	Rank              int
}

type sharedSubtitleSourceKey struct {
	MachineIdentifier string
	SectionUUID       string
	ScanID            string
	RatingKey         string
	MediaID           int64
	PartID            int64
}

// authorizeSharedSubtitleCandidates is a small seam for the security
// boundary: each source is checked once, while every candidate is retained
// only if that source check succeeded.  A checker error is systemic and must
// fail the whole search closed; a false result is an ordinary source denial.
func authorizeSharedSubtitleCandidates(ctx context.Context, candidates []sharedSubtitleCandidate, check func(context.Context, sharedSubtitleCandidate) (bool, error)) ([]sharedSubtitleCandidate, error) {
	if check == nil {
		return nil, errors.New("shared subtitle authorization is unavailable")
	}
	checked := make(map[sharedSubtitleSourceKey]bool)
	authorized := make([]sharedSubtitleCandidate, 0, minInt(maxSharedSubtitleAuthorizedHits, len(candidates)))
	for _, candidate := range candidates {
		if strings.TrimSpace(candidate.MachineIdentifier) == "" || strings.TrimSpace(candidate.SectionUUID) == "" {
			continue
		}
		key := sharedSubtitleSourceKey{candidate.MachineIdentifier, candidate.SectionUUID, candidate.ScanID, candidate.RatingKey, candidate.MediaID, candidate.PartID}
		allowed, ok := checked[key]
		if !ok {
			if len(checked) >= maxSharedSubtitleSourceChecks {
				break
			}
			var err error
			allowed, err = check(ctx, candidate)
			if err != nil {
				return nil, err
			}
			checked[key] = allowed
		}
		if allowed {
			authorized = append(authorized, candidate)
			if len(authorized) >= maxSharedSubtitleAuthorizedHits {
				break
			}
		}
	}
	return authorized, nil
}

func authorizeAndFetchSharedSubtitleHits(ctx context.Context, candidates []sharedSubtitleCandidate, check func(context.Context, sharedSubtitleCandidate) (bool, error), fetch func(context.Context, sharedSubtitleCandidate) (subtitleSearchHit, error)) ([]subtitleSearchHit, error) {
	authorized, err := authorizeSharedSubtitleCandidates(ctx, candidates, check)
	if err != nil {
		return nil, err
	}
	hits := make([]subtitleSearchHit, 0, len(authorized))
	for _, candidate := range authorized {
		hit, err := fetch(ctx, candidate)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		hits = append(hits, hit)
	}
	return hits, nil
}

func authorizeAndFetchSharedSubtitleCandidates(ctx context.Context, candidates []sharedSubtitleCandidate, check func(context.Context, sharedSubtitleCandidate) (bool, error), fetch func(context.Context, sharedSubtitleCandidate) (subtitleSearchHit, error)) ([]subtitleSearchCandidate, error) {
	authorized, err := authorizeSharedSubtitleCandidates(ctx, candidates, check)
	if err != nil {
		return nil, err
	}
	result := make([]subtitleSearchCandidate, 0, len(authorized))
	for _, candidate := range authorized {
		hit, fetchErr := fetch(ctx, candidate)
		if errors.Is(fetchErr, pgx.ErrNoRows) {
			continue
		}
		if fetchErr != nil {
			return nil, fetchErr
		}
		result = append(result, subtitleSearchCandidate{Hit: hit, Shared: candidate, IsShared: true, Tier: candidate.Tier, Rank: candidate.Rank})
	}
	return result, nil
}

func fetchAuthorizedSharedSubtitleCandidates(ctx context.Context, candidates []sharedSubtitleCandidate, fetch func(context.Context, sharedSubtitleCandidate) (subtitleSearchHit, error)) ([]subtitleSearchCandidate, error) {
	result := make([]subtitleSearchCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		hit, err := fetch(ctx, candidate)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		result = append(result, subtitleSearchCandidate{Hit: hit, Shared: candidate, IsShared: true, Tier: candidate.Tier, Rank: candidate.Rank})
	}
	return result, nil
}

// diversifySubtitleSearchHits applies a soft, deterministic source-diversity
// preference to the already relevance-ordered candidate window. A source is
// never capped: each previously selected hit from that source adds a bounded
// penalty to its next selection, so sufficiently relevant hits can still
// occupy as much of the result window as warranted.
//
// The variadic argument is retained for source compatibility with the initial
// implementation, where it was a hard per-source limit. It is intentionally
// ignored; diversity is a ranking preference, not a filter.
func diversifySubtitleSearchHits(candidates []subtitleSearchHit, limit int, _ ...int) []subtitleSearchHit {
	if limit <= 0 {
		return []subtitleSearchHit{}
	}
	selected := make([]subtitleSearchHit, 0, minInt(limit, len(candidates)))
	counts := make(map[string]int)
	used := make([]bool, len(candidates))
	const (
		diversityPenaltyStep = 0.20
		diversityPenaltyMax  = 0.60
	)
	for len(selected) < limit && len(selected) < len(candidates) {
		bestIndex := -1
		bestAdjustedScore := 0.0
		for index, candidate := range candidates {
			if used[index] {
				continue
			}
			key := subtitleSearchHitSourceKey(candidate)
			penalty := float64(counts[key]) * diversityPenaltyStep
			if penalty > diversityPenaltyMax {
				penalty = diversityPenaltyMax
			}
			adjustedScore := candidate.Score - penalty
			// Keeping the first candidate on ties makes the ranking stable
			// relative to the database's relevance ordering.
			if bestIndex < 0 || adjustedScore > bestAdjustedScore {
				bestIndex = index
				bestAdjustedScore = adjustedScore
			}
		}
		if bestIndex < 0 {
			break
		}
		used[bestIndex] = true
		selected = append(selected, candidates[bestIndex])
		counts[subtitleSearchHitSourceKey(candidates[bestIndex])]++
	}
	return selected
}

func subtitleSearchHitSourceKey(hit subtitleSearchHit) string {
	return fmt.Sprintf("%s:%d:%d", hit.RatingKey, hit.MediaID, hit.PartID)
}

func subtitleSearchCandidateSourceKey(candidate subtitleSearchCandidate) string {
	if candidate.IsShared {
		ratingKey := candidate.Shared.RatingKey
		if ratingKey == "" {
			ratingKey = candidate.Hit.RatingKey
		}
		return fmt.Sprintf("shared:%s:%s:%s", candidate.Shared.MachineIdentifier, candidate.Shared.SectionUUID, ratingKey)
	}
	return fmt.Sprintf("local:%s", candidate.Hit.RatingKey)
}

func subtitleSearchCandidateTitleKey(candidate subtitleSearchCandidate) string {
	hit := candidate.Hit
	key := "title:" + normalizeSubtitleSearchLiteral(hit.Title)
	showTitle := normalizeSubtitleSearchLiteral(hit.ShowTitle)
	if showTitle != "" {
		key += "|show:" + showTitle
	}
	if hit.Season != nil {
		key += fmt.Sprintf("|season:%d", *hit.Season)
	}
	if hit.Episode != nil {
		key += fmt.Sprintf("|episode:%d", *hit.Episode)
	}
	// Year separates same-named movie remakes without splitting versions of
	// the same catalog item, whose metadata normally has the same year.
	if showTitle == "" && hit.Year != nil {
		key += fmt.Sprintf("|year:%d", *hit.Year)
	}
	return key
}

// scanSubtitleSearchHit keeps nullable catalog metadata out of the pgx scan
// destinations. Plex metadata is legitimately incomplete for some movies and
// episodes, and PostgreSQL represents those fields as NULL.
func scanSubtitleSearchHit(scan func(...any) error) (subtitleSearchHit, error) {
	var hit subtitleSearchHit
	var showTitle sql.NullString
	var season, episode, year sql.NullInt64
	if err := scan(&hit.RatingKey, &hit.MediaID, &hit.PartID, &hit.SubtitleIndex, &hit.Title, &showTitle, &season, &episode, &year, &hit.StartMs, &hit.EndMs, &hit.Text, &hit.Score); err != nil {
		return subtitleSearchHit{}, err
	}
	if showTitle.Valid {
		hit.ShowTitle = showTitle.String
	}
	if season.Valid {
		value := int(season.Int64)
		hit.Season = &value
	}
	if episode.Valid {
		value := int(episode.Int64)
		hit.Episode = &value
	}
	if year.Valid {
		value := int(year.Int64)
		hit.Year = &value
	}
	return hit, nil
}

func scanSharedSubtitleCandidate(scan func(...any) error) (sharedSubtitleCandidate, error) {
	var candidate sharedSubtitleCandidate
	if err := scan(&candidate.MachineIdentifier, &candidate.SectionUUID, &candidate.SectionKey, &candidate.ScanID, &candidate.SectionType, &candidate.RatingKey, &candidate.MediaID, &candidate.PartID, &candidate.SubtitleIndex, &candidate.StartMs, &candidate.EndMs, &candidate.Score); err != nil {
		return sharedSubtitleCandidate{}, err
	}
	if strings.TrimSpace(candidate.SectionUUID) == "" || strings.TrimSpace(candidate.ScanID) == "" || !validSharedSectionKey(candidate.SectionKey) {
		return sharedSubtitleCandidate{}, errors.New("shared subtitle candidate has invalid section identity")
	}
	return candidate, nil
}

func (a *Application) searchSubtitleIndex(ctx context.Context, query string) ([]subtitleSearchHit, error) {
	if a.subtitleSearch == nil {
		return nil, errSubtitleSearchDisabled
	}
	owner, err := subtitleSearchOwner(ctx)
	if err != nil {
		return nil, err
	}
	if a.sharedCorpus {
		return a.searchSharedSubtitleIndex(ctx, query)
	}
	trimmedQuery := strings.TrimSpace(query)
	if trimmedQuery == "" {
		return []subtitleSearchHit{}, nil
	}
	normalized := normalizeSubtitleSearchLiteral(trimmedQuery)
	candidates := make([]subtitleSearchCandidate, 0, maxSubtitleLexicalCandidates*2+maxSubtitleSemanticCandidates)
	load := func(statement string, args []any, tier int) error {
		rows, err := a.subtitleSearch.pool.Query(ctx, statement, args...)
		if err != nil {
			return errors.New("could not search subtitle index")
		}
		defer rows.Close()
		rank := 0
		for rows.Next() {
			hit, scanErr := scanSubtitleSearchHit(rows.Scan)
			if scanErr != nil {
				return errors.New("could not read subtitle index")
			}
			candidates = append(candidates, subtitleSearchCandidate{Hit: hit, Tier: tier, Rank: rank})
			rank++
		}
		if err := rows.Err(); err != nil {
			return errors.New("could not read subtitle index")
		}
		return nil
	}
	if normalized != "" {
		if err := load(`SELECT rating_key, media_id, part_id, subtitle_index, title, show_title, season, episode, year, start_ms, end_ms, text, 1.0 AS score FROM subtitle_chunks WHERE owner_uuid=$1 AND btrim(regexp_replace(lower(text), '[^[:alnum:]]+', ' ', 'g')) LIKE '%' || $2 || '%' ORDER BY id LIMIT $3`, []any{owner, normalized, maxSubtitleLexicalCandidates}, 0); err != nil {
			return nil, err
		}
	}
	if err := load(`SELECT rating_key, media_id, part_id, subtitle_index, title, show_title, season, episode, year, start_ms, end_ms, text, ts_rank_cd(text_search, phraseto_tsquery('simple', $2)) AS score FROM subtitle_chunks WHERE owner_uuid=$1 AND text_search @@ phraseto_tsquery('simple', $2) ORDER BY score DESC, id LIMIT $3`, []any{owner, trimmedQuery, maxSubtitleLexicalCandidates}, 1); err != nil {
		return nil, err
	}
	embeddings, err := a.subtitleSearch.embeddings.embed(ctx, []string{a.subtitleSearch.queryInput(trimmedQuery)})
	if err != nil {
		return nil, err
	}
	if err := load(`SELECT rating_key, media_id, part_id, subtitle_index, title, show_title, season, episode, year, start_ms, end_ms, text, 1-(embedding <=> $1) AS score FROM subtitle_chunks WHERE owner_uuid=$2 ORDER BY embedding <=> $1 LIMIT $3`, []any{pgvector.NewVector(embeddings[0]), owner, maxSubtitleSemanticCandidates}, 2); err != nil {
		return nil, err
	}
	merged := mergeSubtitleSearchCandidates(candidates)
	result := diversifyHybridSubtitleSearchCandidates(merged, 50)
	hits := make([]subtitleSearchHit, 0, len(result))
	for _, candidate := range result {
		hits = append(hits, candidate.Hit)
	}
	return hits, nil
}

func (a *Application) searchSharedSubtitleIndex(ctx context.Context, query string) ([]subtitleSearchHit, error) {
	if a.subtitleSearch == nil {
		return nil, errSubtitleSearchDisabled
	}
	if a.plexResources == nil || strings.TrimSpace(a.machineIdentifier) == "" {
		return nil, errors.New("shared subtitle search is unavailable")
	}
	trimmedQuery := strings.TrimSpace(query)
	if trimmedQuery == "" {
		return []subtitleSearchHit{}, nil
	}
	access, resolver, err := a.callerPlexAccess(ctx, "")
	if err != nil {
		return nil, errors.New("shared subtitle visibility is unavailable")
	}
	sections, err := a.indexLibrarySections(ContextWithPlexAccess(ctx, access), "")
	if err != nil {
		return nil, errors.New("shared subtitle visibility is unavailable")
	}
	sectionTypes := make(map[string]string, len(sections))
	sectionKeys := make(map[string]string, len(sections))
	for _, section := range sections {
		if validSharedSectionKey(section.Key) && strings.TrimSpace(section.UUID) != "" && (section.Type == "movie" || section.Type == "show") {
			sectionTypes[section.UUID] = section.Type
			sectionKeys[section.UUID] = section.Key
		}
	}
	if len(sectionTypes) == 0 {
		return []subtitleSearchHit{}, nil
	}
	uuidKeys := make([]string, 0, len(sectionTypes))
	for sectionUUID := range sectionTypes {
		uuidKeys = append(uuidKeys, sectionUUID)
	}
	candidates := make([]sharedSubtitleCandidate, 0, maxSharedSubtitleCandidates*3)
	load := func(statement string, args []any, tier int) error {
		rows, queryErr := a.subtitleSearch.pool.Query(ctx, statement, args...)
		if queryErr != nil {
			return errors.New("could not search subtitle index")
		}
		defer rows.Close()
		rank := 0
		for rows.Next() {
			candidate, scanErr := scanSharedSubtitleCandidate(rows.Scan)
			if scanErr != nil {
				return errors.New("could not read subtitle index")
			}
			candidate.Tier, candidate.Rank = tier, rank
			candidates = append(candidates, candidate)
			rank++
		}
		if rowsErr := rows.Err(); rowsErr != nil {
			return errors.New("could not read subtitle index")
		}
		return nil
	}
	normalized := normalizeSubtitleSearchLiteral(trimmedQuery)
	const sharedChunksJoin = `FROM subtitle_shared_chunks c JOIN subtitle_shared_sections s ON s.machine_identifier=c.machine_identifier AND s.section_uuid=c.section_uuid AND s.state<>'failed' AND (c.scan_id=s.scan_id OR (s.ready_scan_id IS NOT NULL AND c.scan_id=s.ready_scan_id AND NOT EXISTS (SELECT 1 FROM subtitle_shared_chunks n WHERE n.machine_identifier=c.machine_identifier AND n.section_uuid=c.section_uuid AND n.scan_id=s.scan_id AND n.rating_key=c.rating_key AND n.media_id=c.media_id AND n.part_id=c.part_id AND n.subtitle_index=c.subtitle_index)))`
	if normalized != "" {
		if err := load(`SELECT c.machine_identifier, c.section_uuid, c.section_key, c.scan_id, s.section_type, c.rating_key, c.media_id, c.part_id, c.subtitle_index, c.start_ms, c.end_ms, 1.0 AS score `+sharedChunksJoin+` WHERE c.machine_identifier=$1 AND c.section_uuid=ANY($2) AND btrim(regexp_replace(lower(c.text), '[^[:alnum:]]+', ' ', 'g')) LIKE '%' || $3 || '%' AND EXISTS (SELECT 1 FROM subtitle_shared_sources v WHERE v.machine_identifier=c.machine_identifier AND v.section_uuid=c.section_uuid AND v.scan_id=c.scan_id AND v.rating_key=c.rating_key AND v.media_id=c.media_id AND v.part_id=c.part_id AND v.subtitle_index=c.subtitle_index AND v.chunk_count > 0) ORDER BY c.id LIMIT $4`, []any{a.machineIdentifier, uuidKeys, normalized, maxSharedSubtitleCandidates}, 0); err != nil {
			return nil, err
		}
	}
	if err := load(`SELECT c.machine_identifier, c.section_uuid, c.section_key, c.scan_id, s.section_type, c.rating_key, c.media_id, c.part_id, c.subtitle_index, c.start_ms, c.end_ms, ts_rank_cd(c.text_search, phraseto_tsquery('simple', $3)) AS score `+sharedChunksJoin+` WHERE c.machine_identifier=$1 AND c.section_uuid=ANY($2) AND c.text_search @@ phraseto_tsquery('simple', $3) AND EXISTS (SELECT 1 FROM subtitle_shared_sources v WHERE v.machine_identifier=c.machine_identifier AND v.section_uuid=c.section_uuid AND v.scan_id=c.scan_id AND v.rating_key=c.rating_key AND v.media_id=c.media_id AND v.part_id=c.part_id AND v.subtitle_index=c.subtitle_index AND v.chunk_count > 0) ORDER BY score DESC, c.id LIMIT $4`, []any{a.machineIdentifier, uuidKeys, trimmedQuery, maxSharedSubtitleCandidates}, 1); err != nil {
		return nil, err
	}
	embeddings, err := a.subtitleSearch.embeddings.embed(ctx, []string{a.subtitleSearch.queryInput(trimmedQuery)})
	if err != nil {
		return nil, err
	}
	// This first corpus query is intentionally limited to source identity and
	// relevance data.  Subtitle text is fetched only after the caller checks
	// below have completed successfully.
	if err := load(`SELECT c.machine_identifier, c.section_uuid, c.section_key, c.scan_id, s.section_type, c.rating_key, c.media_id, c.part_id, c.subtitle_index, c.start_ms, c.end_ms, 1-(c.embedding <=> $1) AS score `+sharedChunksJoin+` WHERE c.machine_identifier=$2 AND c.section_uuid=ANY($3) AND EXISTS (SELECT 1 FROM subtitle_shared_sources v WHERE v.machine_identifier=c.machine_identifier AND v.section_uuid=c.section_uuid AND v.scan_id=c.scan_id AND v.rating_key=c.rating_key AND v.media_id=c.media_id AND v.part_id=c.part_id AND v.subtitle_index=c.subtitle_index AND v.chunk_count > 0) ORDER BY c.embedding <=> $1 LIMIT $4`, []any{pgvector.NewVector(embeddings[0]), a.machineIdentifier, uuidKeys, maxSharedSubtitleCandidates}, 2); err != nil {
		return nil, err
	}
	mergedRaw := make([]subtitleSearchCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		mergedRaw = append(mergedRaw, subtitleSearchCandidate{Shared: candidate, IsShared: true, Tier: candidate.Tier, Rank: candidate.Rank})
	}
	merged := mergeSubtitleSearchCandidates(mergedRaw)
	authorizedCandidates := make([]sharedSubtitleCandidate, 0, len(merged))
	for _, candidate := range merged {
		authorizedCandidates = append(authorizedCandidates, candidate.Shared)
	}
	verifiedContext := ContextWithPlexAccess(ctx, access)
	authorizationContext, cancelAuthorization := context.WithTimeout(verifiedContext, sharedSubtitleAuthorizationTimeout)
	defer cancelAuthorization()
	authorizedCandidates, err = a.authorizeSharedSubtitleCandidatesBySection(authorizationContext, resolver, authorizedCandidates, sectionTypes, sectionKeys)
	if err != nil {
		return nil, errors.New("shared subtitle visibility is unavailable")
	}
	fetched, err := fetchAuthorizedSharedSubtitleCandidates(authorizationContext, authorizedCandidates, a.fetchAuthorizedSharedSubtitleHit)
	if err != nil {
		return nil, errors.New("shared subtitle visibility is unavailable")
	}
	resultCandidates := make([]subtitleSearchCandidate, 0, len(fetched))
	for _, candidate := range fetched {
		resultCandidates = append(resultCandidates, candidate)
	}
	resultCandidates = diversifyHybridSubtitleSearchCandidates(resultCandidates, 50)
	hits := make([]subtitleSearchHit, 0, len(resultCandidates))
	for _, candidate := range resultCandidates {
		hits = append(hits, candidate.Hit)
	}
	return hits, nil
}

func isSharedSourceRejection(err error) bool {
	if err == nil {
		return false
	}
	var validationErr *sourceValidationError
	if errors.As(err, &validationErr) {
		return true
	}
	if errors.Is(err, errPlexAccessDenied) {
		return true
	}
	status := plexMetadataStatus(err)
	return status == http.StatusBadRequest || status == http.StatusUnauthorized || status == http.StatusForbidden || status == http.StatusNotFound || status == http.StatusGone || status >= 300 && status < 400
}

func sharedSubtitleSourceCoordinate(candidate sharedSubtitleCandidate) string {
	return fmt.Sprintf("%s:%s:%d:%d", candidate.SectionUUID, candidate.RatingKey, candidate.MediaID, candidate.PartID)
}

func (a *Application) callerSectionSourceSet(ctx context.Context, resolver *PlexResourceResolver, sectionKey, sectionType string) (map[string]struct{}, int, int, error) {
	access := PlexAccessFromContext(ctx)
	if access == nil || resolver == nil || !validSharedSectionKey(sectionKey) {
		return nil, 0, 0, errors.New("shared subtitle visibility is unavailable")
	}
	typeValue := map[string]string{"movie": "1", "show": "4"}[sectionType]
	if typeValue == "" {
		return nil, 0, 0, errors.New("shared subtitle visibility is unavailable")
	}
	sources := make(map[string]struct{})
	seenPages := make(map[string]bool)
	previousStart := -1
	itemsSeen := 0
	for pageCount, start := 1, 0; pageCount <= maxSharedSubtitleAuthorizationPages; pageCount++ {
		if start == previousStart {
			return nil, pageCount, itemsSeen, errors.New("shared subtitle visibility is unavailable")
		}
		previousStart = start
		values := url.Values{}
		values.Set("type", typeValue)
		values.Set("X-Plex-Container-Start", strconv.Itoa(start))
		values.Set("X-Plex-Container-Size", strconv.Itoa(indexLibraryPageSize))
		path := "/library/sections/" + url.PathEscape(sectionKey) + "/all?" + values.Encode()
		request, err := newPlexRequest(ctx, access, http.MethodGet, path)
		if err != nil {
			return nil, pageCount, itemsSeen, errors.New("shared subtitle visibility is unavailable")
		}
		request.Header.Set("Accept", "application/json")
		request.Header.Set("X-Plex-Accept", "application/json")
		response, err := resolver.DoPlexRequest(ctx, access, request)
		if err != nil {
			return nil, pageCount, itemsSeen, errors.New("shared subtitle visibility is unavailable")
		}
		if response == nil {
			return nil, pageCount, itemsSeen, errors.New("shared subtitle visibility is unavailable")
		}
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			response.Body.Close()
			return nil, pageCount, itemsSeen, errors.New("shared subtitle visibility is unavailable")
		}
		if err := validatePlexJSONResponse(response); err != nil {
			response.Body.Close()
			return nil, pageCount, itemsSeen, errors.New("shared subtitle visibility is unavailable")
		}
		var page plexMetadataResponse
		decodeErr := readBoundedJSON(response.Body, &page)
		response.Body.Close()
		if decodeErr != nil || page.MediaContainer == nil {
			return nil, pageCount, itemsSeen, errors.New("shared subtitle visibility is unavailable")
		}
		if page.MediaContainer.Offset != 0 && page.MediaContainer.Offset != start {
			return nil, pageCount, itemsSeen, errors.New("shared subtitle visibility is unavailable")
		}
		items := page.MediaContainer.Metadata
		itemsSeen += len(items)
		if itemsSeen > maxSharedSubtitleAuthorizationItems {
			return nil, pageCount, itemsSeen, errors.New("shared subtitle authorization budget exceeded")
		}
		if len(items) > 0 {
			pageKey := stringValue(items[0].RatingKey) + ":" + stringValue(items[len(items)-1].RatingKey)
			if seenPages[pageKey] {
				return sources, pageCount, itemsSeen, nil
			}
			seenPages[pageKey] = true
		}
		for i := range items {
			item := &items[i]
			if item.Type != "movie" && item.Type != "episode" {
				continue
			}
			media, part, err := selectLibraryMetadataSourceForDiscovery(item)
			if err != nil || media == nil || part == nil {
				continue
			}
			sources[fmt.Sprintf("%s:%d:%d", stringValue(item.RatingKey), media.ID, part.ID)] = struct{}{}
		}
		if len(items) == 0 || len(items) < indexLibraryPageSize {
			return sources, pageCount, itemsSeen, nil
		}
		if page.MediaContainer.TotalSize > 0 && start+len(items) >= page.MediaContainer.TotalSize {
			return sources, pageCount, itemsSeen, nil
		}
		start += len(items)
	}
	return nil, maxSharedSubtitleAuthorizationPages, itemsSeen, errors.New("shared subtitle authorization page budget exceeded")
}

func (a *Application) authorizeSharedSubtitleCandidatesBySection(ctx context.Context, resolver *PlexResourceResolver, candidates []sharedSubtitleCandidate, sectionTypes, sectionKeys map[string]string) ([]sharedSubtitleCandidate, error) {
	if len(candidates) == 0 {
		return []sharedSubtitleCandidate{}, nil
	}
	bySection := make(map[string][]sharedSubtitleCandidate)
	for _, candidate := range candidates {
		if candidate.MachineIdentifier != a.machineIdentifier || strings.TrimSpace(candidate.SectionUUID) == "" || strings.TrimSpace(candidate.ScanID) == "" || !validSharedSectionKey(candidate.SectionKey) {
			continue
		}
		if sectionTypes[candidate.SectionUUID] != candidate.SectionType || sectionKeys[candidate.SectionUUID] == "" {
			continue
		}
		bySection[candidate.SectionUUID] = append(bySection[candidate.SectionUUID], candidate)
	}
	if len(bySection) > maxSharedSubtitleAuthorizationSections {
		return nil, errors.New("shared subtitle authorization budget exceeded")
	}
	checked := make(map[string]bool)
	allowed := make(map[string]bool)
	sectionSources := make(map[string]map[string]struct{}, len(bySection))
	totalPages, totalItems := 0, 0
	for sectionUUID, sectionCandidates := range bySection {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		sources, pages, items, err := a.callerSectionSourceSet(ctx, resolver, sectionKeys[sectionUUID], sectionTypes[sectionUUID])
		if err != nil {
			return nil, err
		}
		totalPages += pages
		totalItems += items
		if totalPages > maxSharedSubtitleAuthorizationPages || totalItems > maxSharedSubtitleAuthorizationItems {
			return nil, errors.New("shared subtitle authorization budget exceeded")
		}
		sectionSources[sectionUUID] = sources
		for _, candidate := range sectionCandidates {
			key := sharedSubtitleSourceCoordinate(candidate)
			if _, seen := checked[key]; seen {
				continue
			}
			if len(checked) >= maxSharedSubtitleSourceChecks {
				return nil, errors.New("shared subtitle authorization budget exceeded")
			}
			checked[key] = true
			if _, visible := sources[fmt.Sprintf("%s:%d:%d", candidate.RatingKey, candidate.MediaID, candidate.PartID)]; !visible {
				allowed[key] = false
				continue
			}
			if _, err := a.GetLibrarySource(ctx, candidate.RatingKey, candidate.MediaID, candidate.PartID); err != nil {
				if isSharedSourceRejection(err) {
					allowed[key] = false
					continue
				}
				return nil, errors.New("shared subtitle visibility is unavailable")
			}
			allowed[key] = true
		}
	}
	result := make([]sharedSubtitleCandidate, 0, minInt(maxSharedSubtitleAuthorizedHits, len(candidates)))
	for _, candidate := range candidates {
		if allowed[sharedSubtitleSourceCoordinate(candidate)] {
			result = append(result, candidate)
			if len(result) >= maxSharedSubtitleAuthorizedHits {
				break
			}
		}
	}
	return result, nil
}

func (a *Application) fetchAuthorizedSharedSubtitleHit(ctx context.Context, candidate sharedSubtitleCandidate) (subtitleSearchHit, error) {
	var hit subtitleSearchHit
	var showTitle sql.NullString
	var season, episode, year sql.NullInt64
	err := a.subtitleSearch.pool.QueryRow(ctx, `SELECT c.title, c.show_title, c.season, c.episode, c.year, c.text FROM subtitle_shared_chunks c JOIN subtitle_shared_sections sec ON sec.machine_identifier=c.machine_identifier AND sec.section_uuid=c.section_uuid AND sec.state<>'failed' AND (sec.scan_id=c.scan_id OR (sec.ready_scan_id IS NOT NULL AND sec.ready_scan_id=c.scan_id)) JOIN subtitle_shared_sources src ON src.machine_identifier=c.machine_identifier AND src.section_uuid=c.section_uuid AND src.scan_id=c.scan_id AND src.rating_key=c.rating_key AND src.media_id=c.media_id AND src.part_id=c.part_id AND src.subtitle_index=c.subtitle_index AND src.chunk_count > 0 WHERE c.machine_identifier=$1 AND c.section_uuid=$2 AND c.section_key=$3 AND c.scan_id=$4 AND c.rating_key=$5 AND c.media_id=$6 AND c.part_id=$7 AND c.subtitle_index=$8 AND c.start_ms=$9 AND c.end_ms=$10`, candidate.MachineIdentifier, candidate.SectionUUID, candidate.SectionKey, candidate.ScanID, candidate.RatingKey, candidate.MediaID, candidate.PartID, candidate.SubtitleIndex, candidate.StartMs, candidate.EndMs).Scan(&hit.Title, &showTitle, &season, &episode, &year, &hit.Text)
	if err != nil {
		return subtitleSearchHit{}, err
	}
	hit.RatingKey, hit.MediaID, hit.PartID = candidate.RatingKey, candidate.MediaID, candidate.PartID
	hit.SubtitleIndex, hit.StartMs, hit.EndMs, hit.Score = candidate.SubtitleIndex, candidate.StartMs, candidate.EndMs, candidate.Score
	if showTitle.Valid {
		hit.ShowTitle = showTitle.String
	}
	if season.Valid {
		value := int(season.Int64)
		hit.Season = &value
	}
	if episode.Valid {
		value := int(episode.Int64)
		hit.Episode = &value
	}
	if year.Valid {
		value := int(year.Int64)
		hit.Year = &value
	}
	return hit, nil
}
