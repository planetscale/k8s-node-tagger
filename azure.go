package main

import (
	"context"
	"fmt"
)

// azureComputeClient is the minimum interface we need for interacting with the
// Azure compute API to read and write VM tags.
//
// TODO: Replace these stub method signatures with the real Azure SDK calls once
// the SDK package (armcompute vs. instance metadata service) is chosen. See the
// open questions in the PR that introduced this file.
type azureComputeClient interface {
	// GetInstanceTags returns the current tags on the Azure VM identified by the
	// parsed provider ID components.
	GetInstanceTags(ctx context.Context, subscriptionID, resourceGroup, vmName string) (map[string]string, error)
	// SetInstanceTags replaces the monitored tags on the Azure VM with desiredTags.
	SetInstanceTags(ctx context.Context, subscriptionID, resourceGroup, vmName string, desiredTags map[string]string) error
}

// azureComputeClientStub is a placeholder implementation of azureComputeClient.
//
// TODO: Wire up the real Azure SDK client (armcompute.Client or the instance
// metadata service) behind this interface. This stub exists only so the
// controller compiles and the azure provider can be selected end-to-end; it is
// NOT functional and returns an error from every call.
type azureComputeClientStub struct{}

var _ azureComputeClient = (*azureComputeClientStub)(nil)

func newAzureComputeClient() azureComputeClient {
	return &azureComputeClientStub{}
}

func (c *azureComputeClientStub) GetInstanceTags(ctx context.Context, subscriptionID, resourceGroup, vmName string) (map[string]string, error) {
	return nil, fmt.Errorf("azure compute client not yet implemented")
}

func (c *azureComputeClientStub) SetInstanceTags(ctx context.Context, subscriptionID, resourceGroup, vmName string, desiredTags map[string]string) error {
	return fmt.Errorf("azure compute client not yet implemented")
}

// parseAzureProviderID parses an Azure provider ID of the form:
//
//	azure://<subscriptionID>/resourceGroups/<resourceGroup>/providers/Microsoft.Compute/virtualMachines/<vmName>
//
// TODO: confirm the exact provider ID format emitted by the Azure cloud
// provider integration in production before relying on this in Reconcile.
func parseAzureProviderID(providerID string) (subscriptionID, resourceGroup, vmName string, err error) {
	return "", "", "", fmt.Errorf("parseAzureProviderID not yet implemented for %q", providerID)
}
