package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute/v8"
)

// azureVMResource identifies the Azure compute resource backing a Kubernetes
// node, parsed from the node's spec.ProviderID. AKS nodes are either:
//
//   - standalone VMs:
//     azure:///subscriptions/<sub>/resourceGroups/<rg>/providers/Microsoft.Compute/virtualMachines/<vm>
//   - Virtual Machine Scale Set instances (the AKS default for node pools):
//     azure:///subscriptions/<sub>/resourceGroups/<rg>/providers/Microsoft.Compute/virtualMachineScaleSets/<vmss>/virtualMachines/<instance>
type azureVMResource struct {
	SubscriptionID string
	ResourceGroup  string
	// Provider is "virtualMachines" or "virtualMachineScaleSets".
	Provider string
	// VMName is set for standalone VMs (Provider == "virtualMachines").
	VMName string
	// VMSSName is set for VMSS instances (Provider == "virtualMachineScaleSets").
	VMSSName string
	// InstanceID is the VMSS instance index (Provider == "virtualMachineScaleSets").
	InstanceID string
}

func (r azureVMResource) isVMSS() bool { return r.Provider == "virtualMachineScaleSets" }

// azureComputeClient is the minimum interface we need for reading and writing
// Azure VM tags. The production implementation wraps the armcompute
// VirtualMachinesClient and VirtualMachineScaleSetVMsClient; tests use a fake.
type azureComputeClient interface {
	// GetInstanceTags returns the current tags on the Azure VM (or VMSS VM
	// instance) identified by res.
	GetInstanceTags(ctx context.Context, res azureVMResource) (map[string]string, error)
	// SetInstanceTags replaces the resource's entire tag set with desiredTags.
	// Callers are responsible for preserving any unmanaged tags by including
	// them in desiredTags.
	SetInstanceTags(ctx context.Context, res azureVMResource, desiredTags map[string]string) error
}

// azureComputeClientImpl is the production azureComputeClient. It holds the
// Azure credential resolved at setup and lazily builds the per-subscription
// armcompute clients, because the subscription ID is not known until a node's
// provider ID is parsed in Reconcile.
type azureComputeClientImpl struct {
	cred azcore.TokenCredential
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
	return &azureComputeClientImpl{cred: cred}, nil
}

func (c *azureComputeClientImpl) vmClient(subscriptionID string) (*armcompute.VirtualMachinesClient, error) {
	return armcompute.NewVirtualMachinesClient(subscriptionID, c.cred, nil)
}

func (c *azureComputeClientImpl) vmssVMClient(subscriptionID string) (*armcompute.VirtualMachineScaleSetVMsClient, error) {
	return armcompute.NewVirtualMachineScaleSetVMsClient(subscriptionID, c.cred, nil)
}

// GetInstanceTags fetches the current tags on the VM (or VMSS VM instance).
func (c *azureComputeClientImpl) GetInstanceTags(ctx context.Context, res azureVMResource) (map[string]string, error) {
	if res.isVMSS() {
		client, err := c.vmssVMClient(res.SubscriptionID)
		if err != nil {
			return nil, err
		}
		resp, err := client.Get(ctx, res.ResourceGroup, res.VMSSName, res.InstanceID, nil)
		if err != nil {
			return nil, err
		}
		return derefAzureTags(resp.Tags), nil
	}

	client, err := c.vmClient(res.SubscriptionID)
	if err != nil {
		return nil, err
	}
	resp, err := client.Get(ctx, res.ResourceGroup, res.VMName, nil)
	if err != nil {
		return nil, err
	}
	return derefAzureTags(resp.Tags), nil
}

// SetInstanceTags replaces the resource's tags with desiredTags. It GETs the
// resource first (to preserve required fields like Location and Properties),
// overwrites Tags, and PUTs/PATCHes it back, polling the long-running
// operation to completion. Unmanaged tags must already be preserved by the
// caller in desiredTags.
func (c *azureComputeClientImpl) SetInstanceTags(ctx context.Context, res azureVMResource, desiredTags map[string]string) error {
	tagMap := toAzureTags(desiredTags)

	if res.isVMSS() {
		client, err := c.vmssVMClient(res.SubscriptionID)
		if err != nil {
			return err
		}
		resp, err := client.Get(ctx, res.ResourceGroup, res.VMSSName, res.InstanceID, nil)
		if err != nil {
			return err
		}
		vm := resp.VirtualMachineScaleSetVM
		vm.Tags = tagMap
		poller, err := client.BeginUpdate(ctx, res.ResourceGroup, res.VMSSName, res.InstanceID, vm, nil)
		if err != nil {
			return err
		}
		_, err = poller.PollUntilDone(ctx, nil)
		return err
	}

	client, err := c.vmClient(res.SubscriptionID)
	if err != nil {
		return err
	}
	resp, err := client.Get(ctx, res.ResourceGroup, res.VMName, nil)
	if err != nil {
		return err
	}
	vm := resp.VirtualMachine
	vm.Tags = tagMap
	poller, err := client.BeginCreateOrUpdate(ctx, res.ResourceGroup, res.VMName, vm, nil)
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
// provider integration for a Kubernetes node. Both standalone VM and VMSS
// instance forms are accepted:
//
//	azure:///subscriptions/<sub>/resourceGroups/<rg>/providers/Microsoft.Compute/virtualMachines/<vm>
//	azure:///subscriptions/<sub>/resourceGroups/<rg>/providers/Microsoft.Compute/virtualMachineScaleSets/<vmss>/virtualMachines/<instance>
//
// The leading "azure://" prefix and any immediately-following slash (the
// cloud provider emits three slashes) are both trimmed.
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
	// Expected: subscriptions <sub> resourceGroups <rg> providers Microsoft.Compute <provider> <name> [virtualMachines <instance>]
	const minLen = 8
	if len(parts) < minLen {
		return azureVMResource{}, fmt.Errorf("invalid Azure provider ID format: %q", providerID)
	}
	if parts[0] != "subscriptions" || parts[2] != "resourceGroups" || parts[4] != "providers" || parts[5] != "Microsoft.Compute" {
		return azureVMResource{}, fmt.Errorf("invalid Azure provider ID format: %q", providerID)
	}

	res := azureVMResource{
		SubscriptionID: parts[1],
		ResourceGroup:  parts[3],
		Provider:       parts[6],
	}

	switch res.Provider {
	case "virtualMachines":
		res.VMName = parts[7]
	case "virtualMachineScaleSets":
		// .../virtualMachineScaleSets/<vmss>/virtualMachines/<instance>
		if len(parts) < 10 || parts[8] != "virtualMachines" {
			return azureVMResource{}, fmt.Errorf("invalid Azure VMSS provider ID format: %q", providerID)
		}
		res.VMSSName = parts[7]
		res.InstanceID = parts[9]
	default:
		return azureVMResource{}, fmt.Errorf("unsupported Azure compute provider type %q in provider ID: %q", res.Provider, providerID)
	}

	return res, nil
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
