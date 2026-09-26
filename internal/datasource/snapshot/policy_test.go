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

func TestExplicitRecursiveExclusionsKeepLegacyGlobScope(t *testing.T) {
	tests := []struct {
		rule     string
		excluded []string
		kept     []string
	}{
		{
			rule:     "**/*.png",
			excluded: []string{"logo.png", "images/logo.png", "docs/assets/icons/logo.png"},
			kept:     []string{"logo.jpg", "docs/assets/icons/logo.webp", "docs/assets/icons/logo.png.txt"},
		},
		{
			rule:     "**/assets/*.webp",
			excluded: []string{"assets/banner.webp", "docs/assets/banner.webp", "docs/site/assets/banner.webp"},
			kept:     []string{"assets/sub/banner.webp", "docs/asset/banner.webp", "docs/assets/banner.png"},
		},
		{
			rule:     "**/assets",
			excluded: []string{"assets/banner.webp", "docs/assets/sub/banner.webp"},
			kept:     []string{"docs/asset/banner.webp", "docs/assets-old/banner.webp"},
		},
		{
			rule:     "*.png",
			excluded: []string{"logo.png"},
			kept:     []string{"images/logo.png", "docs/assets/logo.png"},
		},
		{
			rule:     "images/*.png",
			excluded: []string{"images/logo.png"},
			kept:     []string{"logo.png", "nested/images/logo.png", "images/icons/logo.png"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.rule, func(t *testing.T) {
			for _, p := range tt.excluded {
				require.True(t, Excluded(p, []string{tt.rule}), p)
			}
			for _, p := range tt.kept {
				require.False(t, Excluded(p, []string{tt.rule}), p)
			}
		})
	}
}

func TestEmptyRecursiveRuleIsRejectedBeforeSync(t *testing.T) {
	for _, mode := range []string{"source", "documents"} {
		config := &types.DataSourceConfig{Settings: map[string]interface{}{
			"mode": mode, "exclude": []string{"**/"},
		}}
		err := ValidateSettings(config)
		require.EqualError(t, err, "invalid exclusion rule")
		require.NotContains(t, err.Error(), "**/", "validation errors must not reveal rule content")
	}
}
