package chatpipeline

import (
	"context"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/assert"
)

func TestRenderContexts(t *testing.T) {
	results := []*types.SearchResult{
		{
			KnowledgeID:          "doc-1",
			KnowledgeTitle:       "Doc One",
			KnowledgeDescription: "first doc",
			Content:              "hello world",
		},
		{
			KnowledgeID: "doc-1",
			Content:     "second chunk",
		},
		{
			KnowledgeID: "doc-2",
			Content:     "another document",
		},
	}
	ctx := context.Background()
	got := RenderContexts(ctx, results)

	assert.Contains(t, got, "<documents>")
	assert.Contains(t, got, "<title>Doc One</title>")
	assert.Contains(t, got, `<context id="1">hello world</context>`)
	assert.Contains(t, got, `<context id="2">second chunk</context>`)
	assert.Contains(t, got, `<context id="3">another document</context>`)
}

func TestRenderContextsNoDocumentHeader(t *testing.T) {
	results := []*types.SearchResult{{Content: "bare chunk"}}
	got := RenderContexts(context.Background(), results)
	assert.NotContains(t, got, "<documents>")
	assert.Equal(t, `<context id="1">bare chunk</context>`, got)
}

func TestDeduplicateSearchResults(t *testing.T) {
	a := &types.SearchResult{ID: "a", Content: "hello"}
	b := &types.SearchResult{ID: "b", Content: "hello"}
	c := &types.SearchResult{ID: "c", Content: "world"}
	d := &types.SearchResult{ID: "a", Content: "hello again"}

	got := DeduplicateSearchResults([]*types.SearchResult{a, b, c, d})
	assert.Len(t, got, 2)
	ids := map[string]bool{}
	for _, r := range got {
		ids[r.ID] = true
	}
	assert.True(t, ids["a"])
	assert.True(t, ids["c"])
}

func TestSortSearchResults(t *testing.T) {
	results := []*types.SearchResult{
		{ID: "low", Score: 0.1},
		{ID: "high", Score: 0.9},
		{ID: "mid", Score: 0.5},
	}
	SortSearchResults(results)
	assert.Equal(t, "high", results[0].ID)
	assert.Equal(t, "mid", results[1].ID)
	assert.Equal(t, "low", results[2].ID)
}
