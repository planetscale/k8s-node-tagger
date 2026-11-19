package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/arm"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armresources/v4"
)

// azureVMResource identifies the standalone Azure VM backing a Kubernetes
// node, parsed from the node's spec.ProviderID:
//
//	azure:///subscriptions/<sub>/resourceGroups/<rg>/providers/Microsoft.Compute/virtualMachines/<vm>
type azureVMResource struct {
	SubscriptionID string
	ResourceGroup  string
	VMName         string
}

// scope returns the VM's ARM resource ID in the form the Tags API expects.
func (r azureVMResource) scope() string {
	return fmt.Sprintf("subscriptions/%s/resourceGroups/%s/providers/Microsoft.Compute/virtualMachines/%s",
		r.SubscriptionID, r.ResourceGroup, r.VMName)
}

// azureComputeClient is the minimum interface we need for reading and writing
// Azure VM tags. The production implementation wraps the armresources
// TagsClient; tests use a fake.
type azureComputeClient interface {
	// GetInstanceTags returns the current tags on the Azure VM identified by res.
	GetInstanceTags(ctx context.Context, res azureVMResource) (map[string]string, error)
	// SetInstanceTags replaces the resource's entire tag set with desiredTags.
	// Callers are responsible for preserving any unmanaged tags by including
	// them in desiredTags.
	SetInstanceTags(ctx context.Context, res azureVMResource, desiredTags map[string]string) error
}

// azureComputeClientImpl is the production azureComputeClient. It uses the
// Microsoft.Resources/tags API rather than a compute GET+PUT of the whole VM.
// A VM PUT resends the VM's user-assigned identities, which makes ARM require
// Microsoft.ManagedIdentity/userAssignedIdentities/assign/action; the Tags API
// only needs Microsoft.Resources/tags/{read,write} (e.g. "Tag Contributor").
type azureComputeClientImpl struct {
	tags *armresources.TagsClient
}

var _ azureComputeClient = (*azureComputeClientImpl)(nil)

// newAzureComputeClient builds an azureComputeClient using ambient Azure
// credentials. This mirrors how the AWS provider uses
// awsconfig.LoadDefaultConfig(ctx) and the GCP provider uses gce.NewService(ctx),
// both of which pick up workload-identity / ambient creds from the environment.
//
// On AKS with workload identity federation enabled, azidentity's
// NewDefaultAzureCredential resolves the WorkloadIdentityCredential from the
// AZURE_CLIENT_ID / AZURE_TENANT_ID / AZURE_FEDERATED_TOKEN_FILE env vars
// projected onto the pod by the Azure workload-identity mutating admission
// webhook. No client secret is required.
func newAzureComputeClient(ctx context.Context) (azureComputeClient, error) {
	cred, err := azidentity.NewDefaultAzureCredential(nil)
	if err != nil {
		return nil, fmt.Errorf("unable to load Azure credentials: %v", err)
	}
	return newAzureTagsClient(cred, nil)
}

func newAzureTagsClient(cred azcore.TokenCredential, opts *arm.ClientOptions) (*azureComputeClientImpl, error) {
	// The *AtScope methods address the VM by its full resource ID, so the
	// client's subscription ID is never used.
	tags, err := armresources.NewTagsClient("", cred, opts)
	if err != nil {
		return nil, fmt.Errorf("unable to create Azure tags client: %v", err)
	}
	return &azureComputeClientImpl{tags: tags}, nil
}

// GetInstanceTags fetches the current tags on the VM.
func (c *azureComputeClientImpl) GetInstanceTags(ctx context.Context, res azureVMResource) (map[string]string, error) {
	resp, err := c.tags.GetAtScope(ctx, res.scope(), nil)
	if err != nil {
		return nil, err
	}
	if resp.Properties == nil {
		return map[string]string{}, nil
	}
	return derefAzureTags(resp.Properties.Tags), nil
}

// SetInstanceTags replaces the VM's tags with desiredTags using a Tags API
// "Replace" patch. Unmanaged tags must already be preserved by the caller in
// desiredTags.
func (c *azureComputeClientImpl) SetInstanceTags(ctx context.Context, res azureVMResource, desiredTags map[string]string) error {
	poller, err := c.tags.BeginUpdateAtScope(ctx, res.scope(), armresources.TagsPatchResource{
		Operation:  to.Ptr(armresources.TagsPatchOperationReplace),
		Properties: &armresources.Tags{Tags: toAzureTags(desiredTags)},
	}, nil)
	if err != nil {
		return err
	}
	_, err = poller.PollUntilDone(ctx, nil)
	return err
}

// derefAzureTags converts the SDK's map[string]*string into a plain
// map[string]string, dropping nil-valued entries.
func derefAzureTags(tags map[string]*string) map[string]string {
	out := make(map[string]string, len(tags))
	for k, v := range tags {
		if v == nil {
			continue
		}
		out[k] = *v
	}
	return out
}

// toAzureTags converts a plain map[string]string into the SDK's
// map[string]*string.
func toAzureTags(tags map[string]string) map[string]*string {
	out := make(map[string]*string, len(tags))
	for k, v := range tags {
		kv := v
		out[k] = to.Ptr(kv)
	}
	return out
}

// parseAzureProviderID parses an Azure provider ID emitted by the Azure cloud
// provider integration for a Kubernetes node:
//
//	azure:///subscriptions/<sub>/resourceGroups/<rg>/providers/Microsoft.Compute/virtualMachines/<vm>
//
// The leading "azure://" prefix and any immediately-following slash (the
// cloud provider emits three slashes) are both trimmed.
//
// Uniform VMSS instances (.../virtualMachineScaleSets/<vmss>/virtualMachines/<id>)
// are rejected: the Tags API does not support that resource type. Flexible
// VMSS instances are standalone virtualMachines resources and are supported.
func parseAzureProviderID(providerID string) (azureVMResource, error) {
	const prefix = "azure://"
	if !strings.HasPrefix(providerID, prefix) {
		return azureVMResource{}, fmt.Errorf("providerID missing %q prefix, this might not be an Azure node? %q", prefix, providerID)
	}
	trimmed := strings.TrimPrefix(providerID, prefix)
	// The Azure cloud provider emits a third slash ("azure:///..."); trim any
	// leading slash so the path starts at "subscriptions/...".
	trimmed = strings.TrimLeft(trimmed, "/")

	parts := strings.Split(trimmed, "/")
	// Expected: subscriptions <sub> resourceGroups <rg> providers Microsoft.Compute <provider> <name>
	const minLen = 8
	if len(parts) < minLen {
		return azureVMResource{}, fmt.Errorf("invalid Azure provider ID format: %q", providerID)
	}
	if parts[0] != "subscriptions" || parts[2] != "resourceGroups" || parts[4] != "providers" || parts[5] != "Microsoft.Compute" {
		return azureVMResource{}, fmt.Errorf("invalid Azure provider ID format: %q", providerID)
	}

	switch parts[6] {
	case "virtualMachines":
		return azureVMResource{
			SubscriptionID: parts[1],
			ResourceGroup:  parts[3],
			VMName:         parts[7],
		}, nil
	case "virtualMachineScaleSets":
		return azureVMResource{}, fmt.Errorf("uniform VMSS instances are not supported (the Azure Tags API cannot tag virtualMachineScaleSets/virtualMachines): %q", providerID)
	default:
		return azureVMResource{}, fmt.Errorf("unsupported Azure compute provider type %q in provider ID: %q", parts[6], providerID)
	}
}

// sanitizeKeyForAzure sanitizes a Kubernetes label/annotation key to fit Azure's
// tag key constraints. Azure tag keys disallow <, >, %, &, \, ?, / and have a
// maximum length of 512 characters. Kubernetes label keys frequently contain
// "/" (e.g. "kubernetes.io/hostname", "topology.kubernetes.io/region",
// "node-role.kubernetes.io/control-plane"), which Azure would otherwise reject.
func sanitizeKeyForAzure(key string) string {
	key = strings.NewReplacer(
		"/", "_",
		"<", "_",
		">", "_",
		"%", "_",
		"&", "_",
		"\\", "_",
		"?", "_",
	).Replace(key)
	if len(key) > 512 {
		key = key[:512]
	}
	return key
}

// sanitizeValueForAzure sanitizes a Kubernetes label/annotation value to fit
// Azure's tag value constraints. Azure tag values disallow <, >, %, &, \, ?
// and have a maximum length of 256 characters.
func sanitizeValueForAzure(value string) string {
	value = strings.NewReplacer(
		"<", "_",
		">", "_",
		"%", "_",
		"&", "_",
		"\\", "_",
		"?", "_",
	).Replace(value)
	if len(value) > 256 {
		value = value[:256]
	}
	return value
}
