package octo

import (
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/im/octo/wire"
	"github.com/stretchr/testify/require"
)

func TestSplitOctoMessagePreservesParagraphsAndCodeFences(t *testing.T) {
	p1 := strings.Repeat("甲", 180)
	p2 := "```go\n" + strings.Repeat("fmt.Println(1)\n", 15) + "```"
	p3 := strings.Repeat("乙", 160)
	text := p1 + "\n\n" + p2 + "\n\n" + p3

	segs := SplitOctoMessage(text, 250)
	require.GreaterOrEqual(t, len(segs), 3)
	for _, s := range segs {
		require.LessOrEqual(t, len([]rune(s)), 260)
		// Every segment has balanced triple backticks
		require.Equal(t, 0, strings.Count(s, "```")%2, "unbalanced fence in segment: %s", s)
	}
}

func TestBuildBridgeTextPayloadNativeMention(t *testing.T) {
	// Subarea (5) and Group (2) must include native mention UID + UTF-16 entity
	p := buildBridgeTextPayload(5, "你好", "uid-42", "丘彬彬")
	require.Equal(t, "@丘彬彬 你好", p["content"])
	m, ok := p["mention"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, []string{"uid-42"}, m["uids"])
	ents, ok := m["entities"].([]wire.Entity)
	require.True(t, ok)
	require.Len(t, ents, 1)
	require.Equal(t, "uid-42", ents[0].UID)
	require.Equal(t, 4, ents[0].Length) // "@丘彬彬" = 4 UTF-16 code units

	// Private DM (1) must NOT add @mention
	dm := buildBridgeTextPayload(1, "你好", "uid-42", "丘彬彬")
	require.Equal(t, "你好", dm["content"])
	_, hasMention := dm["mention"]
	require.False(t, hasMention)
}
