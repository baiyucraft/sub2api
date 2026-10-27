//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAdminGroupOptionalImageToolsCreateAndUpdate(t *testing.T) {
	ctx := context.Background()
	for _, enabled := range []bool{false, true} {
		repo := &groupRepoStubForAdmin{}
		svc := &adminServiceImpl{groupRepo: repo}
		group, err := svc.CreateGroup(ctx, &CreateGroupInput{
			Name: "image-tool-policy", Platform: PlatformOpenAI, RateMultiplier: 1,
			StripOptionalImageTools: enabled,
		})
		require.NoError(t, err)
		require.Equal(t, enabled, group.StripOptionalImageTools)
		require.Equal(t, enabled, repo.created.StripOptionalImageTools)
	}

	repo := &groupRepoStubForAdmin{getByID: &Group{
		ID: 7, Name: "image-tool-policy", Platform: PlatformOpenAI, RateMultiplier: 1,
		AllowImageGeneration: true, StripOptionalImageTools: true,
	}}
	svc := &adminServiceImpl{groupRepo: repo}

	group, err := svc.UpdateGroup(ctx, 7, &UpdateGroupInput{})
	require.NoError(t, err)
	require.True(t, group.StripOptionalImageTools, "omitted updates preserve the stored policy")

	disabled := false
	group, err = svc.UpdateGroup(ctx, 7, &UpdateGroupInput{StripOptionalImageTools: &disabled})
	require.NoError(t, err)
	require.False(t, group.StripOptionalImageTools, "explicit false disables the policy")
	require.True(t, group.AllowImageGeneration, "the optional-tool policy is independent of image permission")

	enabled := true
	group, err = svc.UpdateGroup(ctx, 7, &UpdateGroupInput{
		StripOptionalImageTools: &enabled, AllowImageGeneration: &disabled,
	})
	require.NoError(t, err)
	require.True(t, group.StripOptionalImageTools, "disabling image generation does not erase the stored preference")
	require.False(t, group.AllowImageGeneration)
}
