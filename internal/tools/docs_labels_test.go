package tools

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// A word that is not a section heading: an H3 is found too, and a miss says
// which sections there are and which mention the word (an agent asked for
// "candle", which is documented under types).
func TestSDKDocsSectionLookup(t *testing.T) {
	h3 := extractSection(sdkDocs, "fill semantics")
	assert.True(t, strings.HasPrefix(h3, "### fill semantics in backtest"), "an H3 heading is found")
	assert.NotContains(t, h3, "### brackets", "it stops at the next H3")
	assert.Contains(t, h3, "A leg you cancel in `SetOnOrderUpdate` stays live until the bar closes")

	h2 := extractSection(sdkDocs, "types")
	assert.True(t, strings.HasPrefix(h2, "## types"), "an H2 still wins")

	assert.Empty(t, extractSection(sdkDocs, "candle"), "no heading says candle")
	miss := sectionMiss(sdkDocs, "candle")
	assert.Contains(t, miss, `section "candle" not found. Sections: entry, types,`)
	assert.Contains(t, miss, `"candle" appears in: `)
	assert.Contains(t, miss[strings.Index(miss, "appears in"):], "types")
	assert.NotContains(t, sectionMiss(sdkDocs, "zzqx"), "appears in", "a word nowhere in the docs")
}

// What costs took, and what the 5 best trades are of net, say why there is no
// share when gross or net is not positive, instead of "—".
func TestShareLabels(t *testing.T) {
	assert.Equal(t, "fees + funding 24% of gross", costShare(24, 100))
	assert.Equal(t, "gross -3463.15, fees + funding 10358.16", costShare(10358.16, -3463.15))

	share := 1.25
	assert.Equal(t, "they are 125% of net", top5Note(&share, 10))
	assert.Equal(t, "no share: net is not positive", top5Note(nil, -6895))
	assert.Equal(t, "they are — of net", top5Note(nil, 10))
}
