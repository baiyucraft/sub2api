package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCompareVersionsIgnoresLocalBuildSuffix(t *testing.T) {
	tests := []struct {
		name    string
		current string
		latest  string
		want    int
	}{
		{
			name:    "baiyu suffix same as upstream",
			current: "0.1.144-baiyu",
			latest:  "0.1.144",
			want:    0,
		},
		{
			name:    "v prefix and baiyu suffix same as upstream",
			current: "v0.1.144-baiyu",
			latest:  "0.1.144",
			want:    0,
		},
		{
			name:    "build metadata same as upstream",
			current: "0.1.144+build",
			latest:  "0.1.144",
			want:    0,
		},
		{
			name:    "baiyu suffix still detects newer upstream",
			current: "0.1.144-baiyu",
			latest:  "0.1.145",
			want:    -1,
		},
		{
			name:    "baiyu suffix still detects newer current",
			current: "0.1.145-baiyu",
			latest:  "0.1.144",
			want:    1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, compareVersions(tt.current, tt.latest))
		})
	}
}
