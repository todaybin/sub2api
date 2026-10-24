//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCodeBuddyBrandNameUsesRegion(t *testing.T) {
	t.Parallel()
	require.Equal(t, "CodeBuddy", CodeBuddyBrandName(nil))
	require.Equal(t, "CodeBuddy", CodeBuddyBrandName(&Account{Platform: PlatformCodeBuddy, Credentials: map[string]any{"region": CodeBuddyRegionDomestic}}))
	require.Equal(t, "WorkBuddy", CodeBuddyBrandName(&Account{Platform: PlatformCodeBuddy, Credentials: map[string]any{"region": CodeBuddyRegionInternational}}))
}
