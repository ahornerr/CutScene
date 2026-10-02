package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Embedding clients for subtitle semantic search.
//
// Covers the Text Embeddings Inference and OpenAI-compatible providers,
// provider and tuning-value normalisation, and the embedding validation shared
// by the indexing pipeline and the query path.
type teiClient struct {
	url        string
	provider   string
	apiKey     string
	model      string
	dimensions int
	client     *http.Client
}

func newTEIClient(rawURL string) (*teiClient, error) {
	return newEmbeddingClient(rawURL, embeddingsProviderTEI, "", "", defaultSubtitleEmbeddingDimensions)
}

func normalizeEmbeddingsProvider(value string) (string, error) {
	provider := strings.ToLower(strings.TrimSpace(value))
	if provider == "" {
		return embeddingsProviderTEI, nil
	}
	switch provider {
	case embeddingsProviderTEI, embeddingsProviderOpenAI:
		return provider, nil
	default:
		return "", fmt.Errorf("unsupported embeddings provider %q (want tei or openai)", provider)
	}
}

func normalizeSubtitleEmbeddingBatchSize(value int) int {
	if value <= 0 {
		return defaultSubtitleEmbeddingBatchSize
	}
	if value < minSubtitleEmbeddingBatchSize {
		return minSubtitleEmbeddingBatchSize
	}
	if value > maxSubtitleEmbeddingBatchSize {
		return maxSubtitleEmbeddingBatchSize
	}
	return value
}

func normalizeSubtitleEmbeddingConcurrency(value int) int {
	if value <= 0 {
		return defaultSubtitleEmbeddingConcurrency
	}
	if value < minSubtitleEmbeddingConcurrency {
		return minSubtitleEmbeddingConcurrency
	}
	if value > maxSubtitleEmbeddingConcurrency {
		return maxSubtitleEmbeddingConcurrency
	}
	return value
}

func resolveQueryInstruction(cfg SemanticSearchConfig, dimensions int) string {
	if cfg.QueryInstruction != nil {
		return *cfg.QueryInstruction
	}
	modelLower := strings.ToLower(strings.TrimSpace(cfg.EmbeddingsModel))
	if strings.Contains(modelLower, "bge") {
		return bgeQueryInstruction
	}
	if modelLower == "" && cfg.EmbeddingsProvider != embeddingsProviderOpenAI && dimensions == defaultSubtitleEmbeddingDimensions {
		return bgeQueryInstruction
	}
	return ""
}

func (s *subtitleSearchStore) queryInput(query string) string {
	if s != nil && s.queryInstruction != "" {
		return s.queryInstruction + query
	}
	return query
}

func newEmbeddingClient(rawURL, provider, apiKey, model string, dimensions int) (*teiClient, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil {
		return nil, errors.New("semantic_search.embeddings_url must be an HTTP(S) URL")
	}
	provider, err = normalizeEmbeddingsProvider(provider)
	if err != nil {
		return nil, err
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	if provider == embeddingsProviderTEI {
		if !strings.HasSuffix(parsed.Path, "/embed") {
			parsed.Path += "/embed"
		}
	} else {
		if strings.HasSuffix(parsed.Path, "/v1") {
			parsed.Path += "/embeddings"
		} else if !strings.HasSuffix(parsed.Path, "/v1/embeddings") {
			parsed.Path += "/v1/embeddings"
		}
	}
	if dimensions <= 0 {
		dimensions = defaultSubtitleEmbeddingDimensions
	}
	trimmedModel := strings.TrimSpace(model)
	if trimmedModel == "" && provider == embeddingsProviderOpenAI {
		trimmedModel = openAIEmbeddingModel
	}
	return &teiClient{
		url:        parsed.String(),
		provider:   provider,
		apiKey:     strings.TrimSpace(apiKey),
		model:      trimmedModel,
		dimensions: dimensions,
		client:     &http.Client{Timeout: 30 * time.Second},
	}, nil
}

func validateEmbeddings(embeddings [][]float32, expected int, dimensions int) error {
	if len(embeddings) != expected {
		return fmt.Errorf("embedding response count %d does not match request count %d", len(embeddings), expected)
	}
	if dimensions <= 0 {
		dimensions = defaultSubtitleEmbeddingDimensions
	}
	for i, embedding := range embeddings {
		if len(embedding) != dimensions {
			return fmt.Errorf("embedding %d has dimension %d, want %d", i, len(embedding), dimensions)
		}
	}
	return nil
}

func (c *teiClient) embed(ctx context.Context, inputs []string) ([][]float32, error) {
	if len(inputs) == 0 {
		return [][]float32{}, nil
	}
	vecs, err := c.embedRaw(ctx, inputs)
	if err != nil {
		return nil, err
	}
	expectedDim := c.dimensions
	if expectedDim <= 0 {
		expectedDim = defaultSubtitleEmbeddingDimensions
	}
	if err := validateEmbeddings(vecs, len(inputs), expectedDim); err != nil {
		return nil, err
	}
	return vecs, nil
}

func (c *teiClient) probeDimensions(ctx context.Context) (int, error) {
	probeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	vecs, err := c.embedRaw(probeCtx, []string{"probe"})
	if err != nil {
		return 0, err
	}
	if len(vecs) == 0 || len(vecs[0]) == 0 {
		return 0, errors.New("embedding probe returned empty vector")
	}
	return len(vecs[0]), nil
}

func (c *teiClient) embedRaw(ctx context.Context, inputs []string) ([][]float32, error) {
	if len(inputs) == 0 {
		return [][]float32{}, nil
	}
	if c.provider == embeddingsProviderOpenAI {
		return c.embedRawOpenAI(ctx, inputs)
	}
	return c.embedRawTEI(ctx, inputs)
}

func (c *teiClient) embedRawTEI(ctx context.Context, inputs []string) ([][]float32, error) {
	payload, err := json.Marshal(struct {
		Inputs    []string `json:"inputs"`
		Normalize bool     `json:"normalize"`
	}{inputs, true})
	if err != nil {
		return nil, errors.New("could not encode embedding request")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(payload))
	if err != nil {
		return nil, errors.New("could not create embedding request")
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, errors.New("embedding service unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("embedding service returned status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, errors.New("could not read embedding response")
	}
	var raw json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, errors.New("embedding response is invalid JSON")
	}
	var embeddings [][]float32
	if len(raw) > 0 && raw[0] == '[' {
		if err := json.Unmarshal(raw, &embeddings); err != nil {
			return nil, errors.New("embedding response has invalid vectors")
		}
	} else {
		var wrapped struct {
			Embeddings [][]float32 `json:"embeddings"`
		}
		if err := json.Unmarshal(raw, &wrapped); err != nil {
			return nil, errors.New("embedding response has invalid vectors")
		}
		embeddings = wrapped.Embeddings
	}
	return embeddings, nil
}

type openAIEmbeddingItem struct {
	Embedding []float32 `json:"embedding"`
	Index     int       `json:"index"`
}

type openAIEmbeddingResponse struct {
	Data []openAIEmbeddingItem `json:"data"`
}

func (c *teiClient) embedRawOpenAI(ctx context.Context, inputs []string) ([][]float32, error) {
	model := c.model
	if strings.TrimSpace(model) == "" {
		model = openAIEmbeddingModel
	}
	payload, err := json.Marshal(struct {
		Model          string   `json:"model"`
		Input          []string `json:"input"`
		EncodingFormat string   `json:"encoding_format"`
	}{model, inputs, "float"})
	if err != nil {
		return nil, errors.New("could not encode embedding request")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(payload))
	if err != nil {
		return nil, errors.New("could not create embedding request")
	}
	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, errors.New("embedding service unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("embedding service returned status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, errors.New("could not read embedding response")
	}
	var response openAIEmbeddingResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, errors.New("embedding response is invalid JSON")
	}
	if len(response.Data) != len(inputs) {
		return nil, fmt.Errorf("embedding response count %d does not match request count %d", len(response.Data), len(inputs))
	}
	ordered := make([][]float32, len(inputs))
	seen := make([]bool, len(inputs))
	for _, item := range response.Data {
		if item.Index < 0 || item.Index >= len(inputs) || seen[item.Index] {
			return nil, errors.New("embedding response indexes are invalid")
		}
		seen[item.Index] = true
		ordered[item.Index] = item.Embedding
	}
	for _, present := range seen {
		if !present {
			return nil, errors.New("embedding response indexes are incomplete")
		}
	}
	return ordered, nil
}
