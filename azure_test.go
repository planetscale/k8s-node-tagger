package main

import (
	"context"
	"net/http"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/arm"
	azfake "github.com/Azure/azure-sdk-for-go/sdk/azcore/fake"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armresources/v4"
	armfake "github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armresources/v4/fake"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAzureTagsClient verifies that the production client reads and writes
// tags through the Tags API scoped to the VM's resource ID, and writes with
// the Replace operation.
func TestAzureTagsClient(t *testing.T) {
	res := azureVMResource{SubscriptionID: "sub-123", ResourceGroup: "mc-rg", VMName: "aks-node-1"}
	wantScope := "subscriptions/sub-123/resourceGroups/mc-rg/providers/Microsoft.Compute/virtualMachines/aks-node-1"

	var gotScope string
	var gotPatch armresources.TagsPatchResource
	server := armfake.TagsServer{
		GetAtScope: func(ctx context.Context, scope string, _ *armresources.TagsClientGetAtScopeOptions) (resp azfake.Responder[armresources.TagsClientGetAtScopeResponse], errResp azfake.ErrorResponder) {
			assert.Equal(t, wantScope, scope)
			resp.SetResponse(http.StatusOK, armresources.TagsClientGetAtScopeResponse{
				TagsResource: armresources.TagsResource{
					Properties: &armresources.Tags{Tags: map[string]*string{"cost-center": to.Ptr("12345")}},
				},
			}, nil)
			return
		},
		BeginUpdateAtScope: func(ctx context.Context, scope string, parameters armresources.TagsPatchResource, _ *armresources.TagsClientBeginUpdateAtScopeOptions) (resp azfake.PollerResponder[armresources.TagsClientUpdateAtScopeResponse], errResp azfake.ErrorResponder) {
			gotScope = scope
			gotPatch = parameters
			resp.SetTerminalResponse(http.StatusOK, armresources.TagsClientUpdateAtScopeResponse{}, nil)
			return
		},
	}

	c, err := newAzureTagsClient(&azfake.TokenCredential{}, &arm.ClientOptions{
		ClientOptions: azcore.ClientOptions{Transport: armfake.NewTagsServerTransport(&server)},
	})
	require.NoError(t, err)

	tags, err := c.GetInstanceTags(context.Background(), res)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"cost-center": "12345"}, tags)

	desired := map[string]string{"cost-center": "12345", "env": "prod"}
	require.NoError(t, c.SetInstanceTags(context.Background(), res, desired))
	assert.Equal(t, wantScope, gotScope)
	require.NotNil(t, gotPatch.Operation)
	assert.Equal(t, armresources.TagsPatchOperationReplace, *gotPatch.Operation)
	require.NotNil(t, gotPatch.Properties)
	assert.Equal(t, desired, derefAzureTags(gotPatch.Properties.Tags))
}
