package snapshot

import (
	"encoding/json"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestNullExcludeCompatibilityUsesDefaultRules(t *testing.T) {
	t.Run("decoded JSON null", func(t *testing.T) {
		var config types.DataSourceConfig
		require.NoError(t, json.Unmarshal([]byte(`{"settings":{"mode":"source","exclude":null}}`), &config))
		assertDefaultExclusions(t, &config)
	})

	t.Run("typed nil slice", func(t *testing.T) {
		config := &types.DataSourceConfig{Settings: map[string]interface{}{
			"mode":    "source",
			"exclude": []string(nil),
		}}
		assertDefaultExclusions(t, config)
	})
}

func assertDefaultExclusions(t *testing.T, config *types.DataSourceConfig) {
	t.Helper()
	require.NoError(t, ValidateSettings(config))
	rules := Excludes(config)
	require.Equal(t, DefaultExcludes, rules)
	require.True(t, Excluded("node_modules/package/index.js", rules))
}
