package access

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCanonicalLocalLocator(t *testing.T) {
	for _, tc := range []struct {
		input, want      string
		local, forbidden bool
	}{
		{"local://7/exports/./file", "local://7/exports/file", true, false},
		{"local://7/exports//file", "local://7/exports/file", true, false},
		{"local:////7/exports/./file", "local://7/exports/file", true, false},
		{"storage://backend-a/local://7/exports/./file", "storage://backend-a/local://7/exports/file", true, false},
		{"storage://backend-b/local://7/exports/./file", "storage://backend-b/local://7/exports/file", true, false},
		{"local://7/exports/../file", "local://7/file", true, false},
		{"local://7/exports/sub/../file", "local://7/exports/file", true, false},
		{
			"storage://backend-a/local://7/exports/sub/../file",
			"storage://backend-a/local://7/exports/file", true, false,
		},
		{"local://7/../7/file", "", true, true},
		{"local://7/exports/../../7/file", "", true, true},
		{"local://7/../../8/file", "", true, true},
		{"local://8/exports/../7/file", "", true, true},
		{"local://8/exports/file", "", true, true},
		{"s3://bucket/7/exports/./file", "", false, false},
		{"storage://backend-a/s3://bucket/7/exports/./file", "", false, false},
		{"resource://abcdefghijklmnopqrstuv", "", false, false},
	} {
		t.Run(tc.input, func(t *testing.T) {
			got, local, err := CanonicalLocalLocator(7, tc.input)
			require.Equal(t, tc.local, local)
			if tc.forbidden {
				require.ErrorIs(t, err, ErrForbidden)
			} else {
				require.NoError(t, err)
				require.Equal(t, tc.want, got)
			}
		})
	}
}
