package main

import (
	"context"

	gce "google.golang.org/api/compute/v1"
)

// minimal interface we need for interacting with the GCP GCE API:
type gceClient interface {
	GetInstance(ctx context.Context, project, zone, instance string) (*gce.Instance, error)
	SetLabels(ctx context.Context, project, zone, instance string, req *gce.InstancesSetLabelsRequest) error
}

var _ gceClient = (*gceComputeClient)(nil)

// GCE client implementation that wraps the compute service
type gceComputeClient struct {
	*gce.Service
}

func newGCEComputeClient(client *gce.Service) *gceComputeClient {
	return &gceComputeClient{client}
}

func (c *gceComputeClient) GetInstance(ctx context.Context, project, zone, instance string) (*gce.Instance, error) {
	return c.Instances.Get(project, zone, instance).Context(ctx).Do()
}

func (c *gceComputeClient) SetLabels(ctx context.Context, project, zone, instance string, req *gce.InstancesSetLabelsRequest) error {
	_, err := c.Instances.SetLabels(project, zone, instance, req).Context(ctx).Do()
	return err
}
