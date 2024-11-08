package main

import (
	"context"
	"fmt"
	"maps"
	"path"
	"slices"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	gce "google.golang.org/api/compute/v1"
	corev1 "k8s.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
)

type NodeLabelController struct {
	client.Client
	EC2Client ec2Client
	GCEClient gceClient

	// Labels is a list of label keys to sync from the node to the cloud provider
	Labels []string

	// Cloud is the cloud provider (aws or gcp)
	Cloud string
}

func (r *NodeLabelController) SetupCloudProvider(ctx context.Context) error {
	switch r.Cloud {
	case "aws":
		cfg, err := awsconfig.LoadDefaultConfig(ctx)
		if err != nil {
			return fmt.Errorf("unable to load AWS config: %v", err)
		}
		r.EC2Client = ec2.NewFromConfig(cfg)
	case "gcp":
		c, err := gce.NewService(ctx)
		if err != nil {
			return fmt.Errorf("unable to create GCP client: %v", err)
		}
		r.GCEClient = newGCEComputeClient(c)
	default:
		return fmt.Errorf("unsupported cloud provider: %q", r.Cloud)
	}
	return nil
}

func (r *NodeLabelController) SetupWithManager(mgr ctrl.Manager) error {
	// to reduce the number of API calls to AWS and GCP, filter out node events that
	// do not involve changes to the monitored label set (r.labels).
	labelChangePredicate := predicate.Funcs{
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldNode, ok := e.ObjectOld.(*corev1.Node)
			if !ok {
				return false
			}
			newNode, ok := e.ObjectNew.(*corev1.Node)
			if !ok {
				return false
			}
			return shouldProcessNodeUpdate(oldNode, newNode, r.Labels)
		},

		CreateFunc: func(e event.CreateEvent) bool {
			node, ok := e.Object.(*corev1.Node)
			if !ok {
				return false
			}
			return shouldProcessNodeCreate(node, r.Labels)
		},

		DeleteFunc: func(e event.DeleteEvent) bool {
			return false
		},

		GenericFunc: func(e event.GenericEvent) bool {
			return false
		},
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Node{}).
		WithEventFilter(labelChangePredicate).
		Complete(r)
}

// shouldProcessNodeUpdate determines if a node update event should trigger reconciliation
// based on whether any monitored labels have changed.
func shouldProcessNodeUpdate(oldNode, newNode *corev1.Node, monitoredLabels []string) bool {
	if oldNode == nil || newNode == nil {
		return false
	}

	// Check if any monitored labels changed
	for _, k := range monitoredLabels {
		newVal, newExists := newNode.Labels[k]
		oldVal, oldExists := oldNode.Labels[k]
		if newExists != oldExists || (newExists && newVal != oldVal) {
			return true
		}
	}
	return false
}

// shouldProcessNodeCreate determines if a newly created node should trigger reconciliation
// based on whether it has any of the monitored labels.
func shouldProcessNodeCreate(node *corev1.Node, monitoredLabels []string) bool {
	if node == nil {
		return false
	}

	for _, k := range monitoredLabels {
		if _, ok := node.Labels[k]; ok {
			return true
		}
	}
	return false
}

func (r *NodeLabelController) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := ctrl.Log.WithName("reconcile").WithValues("node", req.NamespacedName)

	var node corev1.Node
	if err := r.Get(ctx, req.NamespacedName, &node); err != nil {
		logger.Error(err, "unable to fetch Node")
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	providerID := node.Spec.ProviderID
	if providerID == "" {
		logger.Info("Node is missing a spec.ProviderID", "node", node.Name)
		return ctrl.Result{}, nil
	}

	labels := make(map[string]string)
	for _, k := range r.Labels {
		if value, exists := node.Labels[k]; exists {
			labels[k] = value
		}
	}

	var err error
	switch r.Cloud {
	case "aws":
		err = r.syncAWSTags(ctx, providerID, labels)
	case "gcp":
		err = r.syncGCPLabels(ctx, providerID, labels)
	}

	if err != nil {
		logger.Error(err, "failed to sync labels")
		return ctrl.Result{}, err
	}

	logger.Info("Successfully synced labels to cloud provider", "labels", labels)
	return ctrl.Result{}, nil
}

func (r *NodeLabelController) syncAWSTags(ctx context.Context, providerID string, desiredLabels map[string]string) error {
	instanceID := path.Base(providerID)
	if instanceID == "" {
		return fmt.Errorf("invalid AWS provider ID format: %q", providerID)
	}

	result, err := r.EC2Client.DescribeTags(ctx, &ec2.DescribeTagsInput{
		Filters: []types.Filter{
			{
				Name:   aws.String("resource-id"),
				Values: []string{instanceID},
			},
		},
	})
	if err != nil {
		return fmt.Errorf("failed to fetch node's current AWS tags: %v", err)
	}

	currentTags := make(map[string]string)
	for _, tag := range result.Tags {
		if key := aws.ToString(tag.Key); key != "" && slices.Contains(r.Labels, key) {
			currentTags[key] = aws.ToString(tag.Value)
		}
	}

	toAdd := make([]types.Tag, 0)
	toDelete := make([]types.Tag, 0)

	// find tags to add or update
	for k, v := range desiredLabels {
		if curr, exists := currentTags[k]; !exists || curr != v {
			toAdd = append(toAdd, types.Tag{
				Key:   aws.String(k),
				Value: aws.String(v),
			})
		}
	}

	// find monitored tags to remove
	for k := range currentTags {
		if slices.Contains(r.Labels, k) {
			if _, exists := desiredLabels[k]; !exists {
				toDelete = append(toDelete, types.Tag{
					Key: aws.String(k),
				})
			}
		}
	}

	if len(toAdd) > 0 {
		_, err := r.EC2Client.CreateTags(ctx, &ec2.CreateTagsInput{
			Resources: []string{instanceID},
			Tags:      toAdd,
		})
		if err != nil {
			return fmt.Errorf("failed to create AWS tags: %v", err)
		}
	}

	if len(toDelete) > 0 {
		_, err := r.EC2Client.DeleteTags(ctx, &ec2.DeleteTagsInput{
			Resources: []string{instanceID},
			Tags:      toDelete,
		})
		if err != nil {
			return fmt.Errorf("failed to delete AWS tags: %v", err)
		}
	}

	return nil
}

func (r *NodeLabelController) syncGCPLabels(ctx context.Context, providerID string, desiredLabels map[string]string) error {
	project, zone, name, err := parseGCPProviderID(providerID)
	if err != nil {
		return fmt.Errorf("failed to parse GCP provider ID: %v", err)
	}

	instance, err := r.GCEClient.GetInstance(ctx, project, zone, name)
	if err != nil {
		return fmt.Errorf("failed to get GCP instance: %v", err)
	}

	newLabels := maps.Clone(instance.Labels)
	if newLabels == nil {
		newLabels = make(map[string]string)
	}

	// create a set of sanitized monitored keys for easy lookup
	monitoredKeys := make(map[string]string) // sanitized -> original
	for _, k := range r.Labels {
		monitoredKeys[sanitizeKeyForGCP(k)] = k
	}

	// remove any existing monitored labels that are no longer desired
	for k := range newLabels {
		if orig, isMonitored := monitoredKeys[k]; isMonitored {
			if _, exists := desiredLabels[orig]; !exists {
				delete(newLabels, k)
			}
		}
	}

	// add or update desired labels
	for k, v := range desiredLabels {
		newLabels[sanitizeKeyForGCP(k)] = sanitizeValueForGCP(v)
	}

	// skip update if no changes
	if maps.Equal(instance.Labels, newLabels) {
		return nil
	}

	err = r.GCEClient.SetLabels(ctx, project, zone, name, &gce.InstancesSetLabelsRequest{
		Labels:           newLabels,
		LabelFingerprint: instance.LabelFingerprint,
	})
	if err != nil {
		return fmt.Errorf("failed to update GCP instance labels: %v", err)
	}

	return nil
}

func parseGCPProviderID(providerID string) (string, string, string, error) {
	if !strings.HasPrefix(providerID, "gce://") {
		return "", "", "", fmt.Errorf("providerID missing \"gce://\" prefix, this might not be a GCE node? %q", providerID)
	}

	trimmed := strings.TrimPrefix(providerID, "gce://")
	parts := strings.Split(trimmed, "/")

	if len(parts) < 3 {
		return "", "", "", fmt.Errorf("invalid GCP provider ID format: %q", providerID)
	}
	return parts[0], parts[1], parts[2], nil
}

func sanitizeLabelsForGCP(labels map[string]string) map[string]string {
	newLabels := make(map[string]string, len(labels))
	for k, v := range labels {
		newLabels[sanitizeKeyForGCP(k)] = sanitizeValueForGCP(v)
	}
	return newLabels
}

// sanitizeKeyForGCP sanitizes a Kubernetes label key to fit GCP's label key constraints
func sanitizeKeyForGCP(key string) string {
	key = strings.ToLower(key)
	key = strings.NewReplacer("/", "_", ".", "-").Replace(key) // Replace disallowed characters
	key = strings.TrimRight(key, "-_")                         // Ensure it does not end with '-' or '_'

	if len(key) > 63 {
		key = key[:63]
	}
	return key
}

// sanitizeKeyForGCP sanitizes a Kubernetes label value to fit GCP's label value constraints
func sanitizeValueForGCP(value string) string {
	if len(value) > 63 {
		value = value[:63]
	}
	return value
}
