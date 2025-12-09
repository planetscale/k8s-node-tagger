package main

import (
	"context"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gce "google.golang.org/api/compute/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// mockEC2Client is a mock implementation of ec2Client for testing
type mockEC2Client struct {
	currentTags []types.TagDescription
	createdTags []types.Tag
	deletedTags []types.Tag
}

func (m *mockEC2Client) DescribeTags(ctx context.Context, params *ec2.DescribeTagsInput, optFns ...func(*ec2.Options)) (*ec2.DescribeTagsOutput, error) {
	return &ec2.DescribeTagsOutput{Tags: m.currentTags}, nil
}

func (m *mockEC2Client) CreateTags(ctx context.Context, params *ec2.CreateTagsInput, optFns ...func(*ec2.Options)) (*ec2.CreateTagsOutput, error) {
	m.createdTags = params.Tags
	return &ec2.CreateTagsOutput{}, nil
}

func (m *mockEC2Client) DeleteTags(ctx context.Context, params *ec2.DeleteTagsInput, optFns ...func(*ec2.Options)) (*ec2.DeleteTagsOutput, error) {
	m.deletedTags = params.Tags
	return &ec2.DeleteTagsOutput{}, nil
}

// mockGCEClient is a mock implementation of gceClient for testing
type mockGCEClient struct {
	instance *gce.Instance
	labels   map[string]string
}

func (m *mockGCEClient) GetInstance(ctx context.Context, project, zone, instance string) (*gce.Instance, error) {
	return m.instance, nil
}

func (m *mockGCEClient) SetLabels(ctx context.Context, project, zone, instance string, req *gce.InstancesSetLabelsRequest) error {
	m.labels = req.Labels
	return nil
}

type mockNode struct {
	Name        string
	Labels      map[string]string
	Annotations map[string]string
	ProviderID  string
}

func TestReconcileAWS(t *testing.T) {
	tests := []struct {
		name              string
		labelsToCopy      []string
		annotationsToCopy []string
		node              mockNode
		currentTags       []types.TagDescription
		createsTags       []types.Tag
		deletesTags       []types.Tag
	}{
		{
			name:              "sync tags from --annotations",
			annotationsToCopy: []string{"region", "instance-type"},
			node: mockNode{
				Name:   "node1",
				Labels: map[string]string{},
				Annotations: map[string]string{
					"region":        "us-east-1",
					"instance-type": "c5.xlarge",
				},
				ProviderID: "aws:///us-east-1a/i-1234567890abcdef0",
			},
			currentTags: []types.TagDescription{},
			createsTags: []types.Tag{
				{Key: aws.String("region"), Value: aws.String("us-east-1")},
				{Key: aws.String("instance-type"), Value: aws.String("c5.xlarge")},
			},
		},
		{
			name:         "sync tags from --labels",
			labelsToCopy: []string{"region", "instance-type"},
			node: mockNode{
				Name: "node1",
				Labels: map[string]string{
					"region":        "us-east-1",
					"instance-type": "c5.xlarge",
				},
				Annotations: map[string]string{},
				ProviderID:  "aws:///us-east-1a/i-1234567890abcdef0",
			},
			currentTags: []types.TagDescription{},
			createsTags: []types.Tag{
				{Key: aws.String("region"), Value: aws.String("us-east-1")},
				{Key: aws.String("instance-type"), Value: aws.String("c5.xlarge")},
			},
		},
		{
			name:              "sync tags from --labels and --annotations",
			labelsToCopy:      []string{"region", "instance-type"},
			annotationsToCopy: []string{"team", "cost-center"},
			node: mockNode{
				Name: "node1",
				Labels: map[string]string{
					"region":        "us-east-1",
					"instance-type": "c5.xlarge",
				},
				Annotations: map[string]string{
					"team":        "infra",
					"cost-center": "rev",
				},
				ProviderID: "aws:///us-east-1a/i-1234567890abcdef0",
			},
			currentTags: []types.TagDescription{},
			createsTags: []types.Tag{
				{Key: aws.String("region"), Value: aws.String("us-east-1")},
				{Key: aws.String("instance-type"), Value: aws.String("c5.xlarge")},
				{Key: aws.String("team"), Value: aws.String("infra")},
				{Key: aws.String("cost-center"), Value: aws.String("rev")},
			},
		},
		{
			name:         "remove tag",
			labelsToCopy: []string{"env"},
			node: mockNode{
				Name:        "node1",
				ProviderID:  "aws:///us-east-1a/i-1234567890abcdef0",
				Labels:      map[string]string{}, // node has no labels
				Annotations: map[string]string{}, // node has no annotations
			},
			currentTags: []types.TagDescription{
				{Key: aws.String("env"), Value: aws.String("prod")},
			},
			deletesTags: []types.Tag{
				{Key: aws.String("env")},
			},
		},
		{
			name:              "preserve unmanaged tags",
			labelsToCopy:      []string{"env"},
			annotationsToCopy: []string{},
			node: mockNode{
				Name: "node1",
				Labels: map[string]string{
					"env": "prod",
				},
				Annotations: map[string]string{},
				ProviderID:  "aws:///us-east-1a/i-1234567890abcdef0",
			},
			currentTags: []types.TagDescription{
				{Key: aws.String("env"), Value: aws.String("staging")},
				{Key: aws.String("cost-center"), Value: aws.String("12345")},
			},
			createsTags: []types.Tag{
				{Key: aws.String("env"), Value: aws.String("prod")},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			require.NoError(t, corev1.AddToScheme(scheme))

			k8s := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(createNode(tt.node)).
				Build()

			mock := &mockEC2Client{currentTags: tt.currentTags}

			r := &NodeLabelController{
				Client:      k8s,
				Logger:      logr.Discard(),
				Labels:      tt.labelsToCopy,
				Annotations: tt.annotationsToCopy,
				Cloud:       "aws",
				EC2Client:   mock,
			}

			_, err := r.Reconcile(context.Background(), ctrl.Request{
				NamespacedName: client.ObjectKey{Name: tt.node.Name},
			})
			require.NoError(t, err)

			assert.ElementsMatch(t, tt.createsTags, mock.createdTags)
			assert.ElementsMatch(t, tt.deletesTags, mock.deletedTags)
		})
	}
}

func TestReconcileGCP(t *testing.T) {
	tests := []struct {
		name              string
		labelsToCopy      []string
		annotationsToCopy []string
		node              mockNode
		currentLabels     map[string]string
		wantLabels        map[string]string
	}{
		{
			name:              "sync single tag from --annotations",
			labelsToCopy:      []string{},
			annotationsToCopy: []string{"region", "instance-type"},
			node: mockNode{
				Name: "node1",
				Annotations: map[string]string{
					"region":        "us-central1",
					"instance-type": "n2-standard-4",
				},
				ProviderID: "gce://my-project/us-central1-a/instance-1",
			},
			currentLabels: map[string]string{},
			wantLabels: map[string]string{
				"region":        "us-central1",
				"instance-type": "n2-standard-4",
			},
		},
		{
			name:              "sync single tag from --label",
			labelsToCopy:      []string{"region", "instance-type"},
			annotationsToCopy: []string{},
			node: mockNode{
				Name: "node1",
				Labels: map[string]string{
					"region":        "us-central1",
					"instance-type": "n2-standard-4",
				},
				ProviderID: "gce://my-project/us-central1-a/instance-1",
			},
			currentLabels: map[string]string{},
			wantLabels: map[string]string{
				"region":        "us-central1",
				"instance-type": "n2-standard-4",
			},
		},
		{
			name:              "add new tag from labels",
			labelsToCopy:      []string{"env", "team"},
			annotationsToCopy: []string{},
			node: mockNode{
				Name: "node1",
				Labels: map[string]string{
					"env":  "prod",
					"team": "platform",
				},
				ProviderID: "gce://my-project/us-central1-a/instance-1",
			},
			currentLabels: map[string]string{
				"env": "staging",
			},
			wantLabels: map[string]string{
				"env":  "prod",
				"team": "platform",
			},
		},
		{
			name:              "remove tag",
			labelsToCopy:      []string{"env"},
			annotationsToCopy: []string{},
			node: mockNode{
				Name:       "node1",
				ProviderID: "gce://my-project/us-central1-a/instance-1",
			},
			currentLabels: map[string]string{
				"env": "prod",
			},
			wantLabels: map[string]string{},
		},
		{
			name:              "preserve unmanaged tags",
			labelsToCopy:      []string{"env"},
			annotationsToCopy: []string{},
			node: mockNode{
				Name: "node1",
				Labels: map[string]string{
					"env": "prod",
				},
				ProviderID: "gce://my-project/us-central1-a/instance-1",
			},
			currentLabels: map[string]string{
				"env":         "staging",
				"cost-center": "12345",
			},
			wantLabels: map[string]string{
				"env":         "prod",
				"cost-center": "12345",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			require.NoError(t, corev1.AddToScheme(scheme))

			k8s := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(createNode(tt.node)).
				Build()

			mock := &mockGCEClient{instance: &gce.Instance{Labels: tt.currentLabels}}

			r := &NodeLabelController{
				Client:      k8s,
				Logger:      logr.Discard(),
				Labels:      tt.labelsToCopy,
				Annotations: tt.annotationsToCopy,
				Cloud:       "gcp",
				GCEClient:   mock,
			}

			_, err := r.Reconcile(context.Background(), ctrl.Request{
				NamespacedName: client.ObjectKey{Name: tt.node.Name},
			})
			require.NoError(t, err)

			assert.Equal(t, tt.wantLabels, mock.labels)
		})
	}
}

func TestShouldProcessNodeUpdate(t *testing.T) {
	tests := []struct {
		name                 string
		oldLabels            map[string]string
		newLabels            map[string]string
		oldAnnotations       map[string]string
		newAnnotations       map[string]string
		monitoredLabels      []string
		monitoredAnnotations []string
		want                 bool
	}{
		{
			name:                 "monitored label added",
			oldLabels:            nil,
			newLabels:            map[string]string{"env": "prod"},
			oldAnnotations:       nil,
			newAnnotations:       nil,
			monitoredLabels:      []string{"env"},
			monitoredAnnotations: []string{},
			want:                 true,
		},
		{
			name:                 "monitored label removed",
			oldLabels:            map[string]string{"env": "prod"},
			newLabels:            nil,
			oldAnnotations:       nil,
			newAnnotations:       nil,
			monitoredLabels:      []string{"env"},
			monitoredAnnotations: []string{},
			want:                 true,
		},
		{
			name:                 "monitored label value changed",
			oldLabels:            map[string]string{"env": "staging"},
			newLabels:            map[string]string{"env": "prod"},
			oldAnnotations:       nil,
			newAnnotations:       nil,
			monitoredLabels:      []string{"env"},
			monitoredAnnotations: []string{},
			want:                 true,
		},
		{
			name:                 "monitored annotation added",
			oldLabels:            map[string]string{},
			newLabels:            map[string]string{},
			oldAnnotations:       nil,
			newAnnotations:       map[string]string{"region": "us-east-1"},
			monitoredLabels:      []string{},
			monitoredAnnotations: []string{"region"},
			want:                 true,
		},
		{
			name:                 "monitored annotation value changed",
			oldLabels:            map[string]string{},
			newLabels:            map[string]string{},
			oldAnnotations:       map[string]string{"region": "us-east-1"},
			newAnnotations:       map[string]string{"region": "us-west-2"},
			monitoredLabels:      []string{},
			monitoredAnnotations: []string{"region"},
			want:                 true,
		},
		{
			name:                 "unmonitored label changed",
			oldLabels:            map[string]string{"foo": "bar"},
			newLabels:            map[string]string{"foo": "baz"},
			oldAnnotations:       nil,
			newAnnotations:       nil,
			monitoredLabels:      []string{"env"},
			monitoredAnnotations: []string{},
			want:                 false,
		},
		{
			name:                 "multiple monitored labels, one changed",
			oldLabels:            map[string]string{"env": "prod", "team": "platform"},
			newLabels:            map[string]string{"env": "prod", "team": "infra"},
			oldAnnotations:       nil,
			newAnnotations:       nil,
			monitoredLabels:      []string{"env", "team"},
			monitoredAnnotations: []string{},
			want:                 true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			oldNode := createNode(mockNode{
				Name:        "node1",
				Labels:      tt.oldLabels,
				Annotations: tt.oldAnnotations,
			})
			newNode := createNode(mockNode{
				Name:        "node1",
				Labels:      tt.newLabels,
				Annotations: tt.newAnnotations,
			})
			got := shouldProcessNodeUpdate(oldNode, newNode, tt.monitoredLabels, tt.monitoredAnnotations)
			assert.Equal(t, tt.want, got)
		})
	}

	// extra safety test for nil node input
	assert.False(t, shouldProcessNodeUpdate(nil, nil, []string{"env"}, []string{}))
}

func TestShouldProcessNodeCreate(t *testing.T) {
	tests := []struct {
		name                 string
		labels               map[string]string
		annotations          map[string]string
		monitoredLabels      []string
		monitoredAnnotations []string
		want                 bool
	}{
		{
			name:                 "node with monitored label",
			labels:               map[string]string{"env": "prod"},
			annotations:          nil,
			monitoredLabels:      []string{"env"},
			monitoredAnnotations: []string{},
			want:                 true,
		},
		{
			name:                 "node with monitored annotation",
			labels:               nil,
			annotations:          map[string]string{"region": "us-east-1"},
			monitoredLabels:      []string{},
			monitoredAnnotations: []string{"region"},
			want:                 true,
		},
		{
			name:                 "node without monitored label",
			labels:               map[string]string{"foo": "bar"},
			annotations:          nil,
			monitoredLabels:      []string{"env"},
			monitoredAnnotations: []string{},
			want:                 false,
		},
		{
			name:                 "node with some monitored labels",
			labels:               map[string]string{"env": "prod", "foo": "bar", "team": "platform"},
			annotations:          nil,
			monitoredLabels:      []string{"env", "team", "region"},
			monitoredAnnotations: []string{},
			want:                 true,
		},
		{
			name:                 "empty labels map",
			labels:               nil,
			annotations:          nil,
			monitoredLabels:      []string{"env"},
			monitoredAnnotations: []string{},
			want:                 false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			node := createNode(mockNode{
				Name:        "node1",
				Labels:      tt.labels,
				Annotations: tt.annotations,
			})
			got := shouldProcessNodeCreate(node, tt.monitoredLabels, tt.monitoredAnnotations)
			assert.Equal(t, tt.want, got)
		})
	}

	// extra safety test for nil node input
	assert.False(t, shouldProcessNodeCreate(nil, []string{"env"}, []string{}))
}

func TestParseGCPProviderID(t *testing.T) {
	tests := []struct {
		name         string
		providerID   string
		wantProject  string
		wantZone     string
		wantInstance string
		wantErr      bool
	}{
		{
			name:         "valid provider ID",
			providerID:   "gce://my-project/us-central1-a/instance-1",
			wantProject:  "my-project",
			wantZone:     "us-central1-a",
			wantInstance: "instance-1",
			wantErr:      false,
		},
		{
			name:       "missing gce prefix",
			providerID: "invalid://my-project/us-central1-a/instance-1",
			wantErr:    true,
		},
		{
			name:       "empty provider ID",
			providerID: "",
			wantErr:    true,
		},
		{
			name:       "insufficient parts",
			providerID: "gce://my-project/us-central1-a",
			wantErr:    true,
		},
		{
			name:         "extra parts should be ignored",
			providerID:   "gce://my-project/us-central1-a/instance-1/extra/parts",
			wantProject:  "my-project",
			wantZone:     "us-central1-a",
			wantInstance: "instance-1",
			wantErr:      false,
		},
		{
			name:         "project with hyphens and numbers",
			providerID:   "gce://my-project-123/us-central1-a/instance-1",
			wantProject:  "my-project-123",
			wantZone:     "us-central1-a",
			wantInstance: "instance-1",
			wantErr:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotProject, gotZone, gotInstance, err := parseGCPProviderID(tt.providerID)

			if tt.wantErr {
				assert.Error(t, err)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.wantProject, gotProject)
			assert.Equal(t, tt.wantZone, gotZone)
			assert.Equal(t, tt.wantInstance, gotInstance)
		})
	}
}

func TestSanitizeLabelsForGCP(t *testing.T) {
	tests := []struct {
		name   string
		labels map[string]string
		want   map[string]string
	}{
		{
			name: "simple labels",
			labels: map[string]string{
				"Example/Key": "Example Value",
				"Another.Key": "Another Value",
			},
			want: map[string]string{
				"example_key": "Example Value",
				"another-key": "Another Value",
			},
		},
		{
			name: "labels with special characters",
			labels: map[string]string{
				"Domain.com/Key":  "Value_1",
				"Project.Version": "Version-1.2.3",
			},
			want: map[string]string{
				"domain-com_key":  "Value_1",
				"project-version": "Version-1.2.3",
			},
		},
		{
			name: "labels exceeding maximum length",
			labels: map[string]string{
				strings.Repeat("a", 70): strings.Repeat("b", 70),
			},
			want: map[string]string{
				strings.Repeat("a", 63): strings.Repeat("b", 63),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sanitizeLabelsForGCP(tt.labels)
			assert.Equal(t, tt.want, got, "sanitizeLabelsForGCP() returned unexpected result")
		})
	}
}

func TestSanitizeKeysForGCP(t *testing.T) {
	tests := []struct {
		name string
		key  string
		want string
	}{
		{
			name: "simple key",
			key:  "Example/Key",
			want: "example_key",
		},
		{
			name: "key with special characters",
			key:  "Domain.com/Key",
			want: "domain-com_key",
		},
		{
			name: "key exceeding maximum length",
			key:  strings.Repeat("a", 70),
			want: strings.Repeat("a", 63),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sanitizeKeyForGCP(tt.key)
			assert.Equal(t, tt.want, got, "sanitizeKeyForGCP() returned unexpected result")
		})
	}
}

func createNode(config mockNode) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:        config.Name,
			Labels:      config.Labels,
			Annotations: config.Annotations,
		},
		Spec: corev1.NodeSpec{
			ProviderID: config.ProviderID,
		},
	}
}

// TestPredicateToReconcileFlow tests the full flow from event through predicate to reconcile.
// This simulates what controller-runtime does: events are filtered by predicates, and only
// if the predicate allows, reconcile is called.
func TestPredicateToReconcileFlow(t *testing.T) {
	tests := []struct {
		name                    string
		monitoredLabels         []string
		initialNode             mockNode
		updatedNode             *mockNode // nil means no update step
		expectReconcileOnCreate bool
		expectReconcileOnUpdate bool
		expectTagsCreated       []string // tag keys we expect to be created
	}{
		{
			name:            "node created without monitored labels then labels added",
			monitoredLabels: []string{"env", "team"},
			initialNode: mockNode{
				Name:       "node1",
				Labels:     map[string]string{"kubernetes.io/hostname": "node1"},
				ProviderID: "aws:///us-east-1a/i-1234567890abcdef0",
			},
			updatedNode: &mockNode{
				Name:       "node1",
				Labels:     map[string]string{"kubernetes.io/hostname": "node1", "env": "prod", "team": "platform"},
				ProviderID: "aws:///us-east-1a/i-1234567890abcdef0",
			},
			expectReconcileOnCreate: false, // no monitored labels yet
			expectReconcileOnUpdate: true,  // monitored labels added
			expectTagsCreated:       []string{"env", "team"},
		},
		{
			name:            "node created with monitored labels already present",
			monitoredLabels: []string{"env"},
			initialNode: mockNode{
				Name:       "node1",
				Labels:     map[string]string{"env": "prod"},
				ProviderID: "aws:///us-east-1a/i-1234567890abcdef0",
			},
			updatedNode:             nil,
			expectReconcileOnCreate: true,
			expectReconcileOnUpdate: false,
			expectTagsCreated:       []string{"env"},
		},
		{
			name:            "node created without labels then only some monitored labels added",
			monitoredLabels: []string{"env", "team", "region"},
			initialNode: mockNode{
				Name:       "node1",
				Labels:     map[string]string{},
				ProviderID: "aws:///us-east-1a/i-1234567890abcdef0",
			},
			updatedNode: &mockNode{
				Name:       "node1",
				Labels:     map[string]string{"env": "prod"}, // only env, not team or region
				ProviderID: "aws:///us-east-1a/i-1234567890abcdef0",
			},
			expectReconcileOnCreate: false,
			expectReconcileOnUpdate: true,
			expectTagsCreated:       []string{"env"}, // only env should be synced
		},
		{
			name:            "node update that does not change monitored labels triggers resync",
			monitoredLabels: []string{"env"},
			initialNode: mockNode{
				Name:       "node1",
				Labels:     map[string]string{"env": "prod"},
				ProviderID: "aws:///us-east-1a/i-1234567890abcdef0",
			},
			updatedNode: &mockNode{
				Name:       "node1",
				Labels:     map[string]string{"env": "prod", "unrelated": "change"},
				ProviderID: "aws:///us-east-1a/i-1234567890abcdef0",
			},
			expectReconcileOnCreate: true,
			expectReconcileOnUpdate: true, // resync: node has monitored labels
			expectTagsCreated:       []string{"env"},
		},
		{
			name:            "multiple labels added in single update",
			monitoredLabels: []string{"env", "team", "cost-center"},
			initialNode: mockNode{
				Name:       "node1",
				Labels:     map[string]string{},
				ProviderID: "aws:///us-east-1a/i-1234567890abcdef0",
			},
			updatedNode: &mockNode{
				Name: "node1",
				Labels: map[string]string{
					"env":         "prod",
					"team":        "platform",
					"cost-center": "12345",
				},
				ProviderID: "aws:///us-east-1a/i-1234567890abcdef0",
			},
			expectReconcileOnCreate: false,
			expectReconcileOnUpdate: true,
			expectTagsCreated:       []string{"env", "team", "cost-center"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			require.NoError(t, corev1.AddToScheme(scheme))

			// Start with initial node in the fake client
			k8s := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(createNode(tt.initialNode)).
				Build()

			mock := &mockEC2Client{currentTags: []types.TagDescription{}}

			controller := &NodeLabelController{
				Client:      k8s,
				Logger:      logr.Discard(),
				Labels:      tt.monitoredLabels,
				Annotations: []string{},
				Cloud:       "aws",
				EC2Client:   mock,
			}

			// Simulate CREATE event
			initialNodeObj := createNode(tt.initialNode)
			createAllowed := shouldProcessNodeCreate(initialNodeObj, tt.monitoredLabels, []string{})

			assert.Equal(t, tt.expectReconcileOnCreate, createAllowed,
				"Create predicate returned unexpected result")

			if createAllowed {
				_, err := controller.Reconcile(context.Background(), ctrl.Request{
					NamespacedName: client.ObjectKey{Name: tt.initialNode.Name},
				})
				require.NoError(t, err)

				// Verify tags were created
				createdKeys := make([]string, 0, len(mock.createdTags))
				for _, tag := range mock.createdTags {
					createdKeys = append(createdKeys, aws.ToString(tag.Key))
				}
				assert.ElementsMatch(t, tt.expectTagsCreated, createdKeys,
					"Created tags don't match expected")
			}

			// Simulate UPDATE event if provided
			if tt.updatedNode != nil {
				// Reset mock for update test
				mock.createdTags = nil
				mock.currentTags = []types.TagDescription{} // EC2 has no tags yet (simulating missed create)

				// Update the node in the fake client
				updatedNodeObj := createNode(*tt.updatedNode)
				err := k8s.Update(context.Background(), updatedNodeObj)
				require.NoError(t, err)

				// Match the actual predicate logic: allow if labels changed OR if node has monitored labels (resync)
				updateAllowed := shouldProcessNodeUpdate(initialNodeObj, updatedNodeObj, tt.monitoredLabels, []string{}) ||
					shouldProcessNodeCreate(updatedNodeObj, tt.monitoredLabels, []string{})

				assert.Equal(t, tt.expectReconcileOnUpdate, updateAllowed,
					"Update predicate returned unexpected result")

				if updateAllowed {
					_, err := controller.Reconcile(context.Background(), ctrl.Request{
						NamespacedName: client.ObjectKey{Name: tt.updatedNode.Name},
					})
					require.NoError(t, err)

					// Verify tags were created
					createdKeys := make([]string, 0, len(mock.createdTags))
					for _, tag := range mock.createdTags {
						createdKeys = append(createdKeys, aws.ToString(tag.Key))
					}
					assert.ElementsMatch(t, tt.expectTagsCreated, createdKeys,
						"Created tags on update don't match expected")
				}
			}
		})
	}
}
