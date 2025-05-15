package util

import (
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestParseWatchedGVKs(t *testing.T) {
	t.Run("valid patterns", func(t *testing.T) {
		gvks, err := ParseGVKs("v1/ConfigMap;apps/v1/Deployment")
		require.NoError(t, err)
		require.Len(t, gvks, 2)
		require.Equal(t, schema.GroupVersionKind{Group: "", Version: "v1", Kind: "ConfigMap"}, gvks[0])
		require.Equal(t, schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"}, gvks[1])
	})

	t.Run("invalid and empty patterns", func(t *testing.T) {
		_, err := ParseGVKs("")
		require.Error(t, err)
		_, err = ParseGVKs("invalidpattern")
		require.Error(t, err)
		_, err = ParseGVKs("/")
		require.Error(t, err)
	})

	t.Run("mixed valid and invalid", func(t *testing.T) {
		gvks, err := ParseGVKs("v1/ConfigMap;bad;apps/v1/Deployment;;/")
		require.NoError(t, err)
		require.Len(t, gvks, 2)
	})
}
