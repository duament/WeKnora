package handler

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestNonEmptyStrings guards the scope normalisation used by
// SearchKnowledgeContext: empty entries are dropped and duplicates are
// removed, so a caller passing knowledge_base_ids:[""] is treated as "no
// scope" (falls back to every accessible KB) instead of erroring.
func TestNonEmptyStrings(t *testing.T) {
	assert.Equal(t, []string{}, nonEmptyStrings(nil))
	assert.Equal(t, []string{}, nonEmptyStrings([]string{}))
	assert.Equal(t, []string{}, nonEmptyStrings([]string{"", ""}))
	assert.Equal(t, []string{"a", "b"}, nonEmptyStrings([]string{"a", "", "b", ""}))
	assert.Equal(t, []string{"a", "b"}, nonEmptyStrings([]string{"a", "a", "b", "a"}))
	assert.Equal(t, []string{"a", "b", "c"}, nonEmptyStrings([]string{"a", "b", "c", "c", "a"}))
}
