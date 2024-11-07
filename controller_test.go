package main

import (
	"context"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
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

func TestReconcileAWS(t *testing.T) {
	tests := []struct {
		name         string
		labelsToCopy []string
		node         *corev1.Node
		currentTags  []types.TagDescription
		createsTags  []types.Tag
		deletesTags  []types.Tag
	}{
		{
			name:         "add new tag",
			labelsToCopy: []string{"env", "team"},
			node: createNode("node1",
				map[string]string{
					"env":  "prod",
					"team": "platform",
				},
				"aws:///us-east-1a/i-1234567890abcdef0",
			),
			currentTags: []types.TagDescription{
				{Key: aws.String("env"), Value: aws.String("staging")},
			},
			createsTags: []types.Tag{
				{Key: aws.String("env"), Value: aws.String("prod")},
				{Key: aws.String("team"), Value: aws.String("platform")},
			},
		},
		{
			name:         "remove tag",
			labelsToCopy: []string{"env"},
			node:         createNode("node1", nil, "aws:///us-east-1a/i-1234567890abcdef0"),
			currentTags: []types.TagDescription{
				{Key: aws.String("env"), Value: aws.String("prod")},
			},
			deletesTags: []types.Tag{
				{Key: aws.String("env")},
			},
		},
		{
			name:         "preserve unmanaged tags",
			labelsToCopy: []string{"env"},
			node: createNode("node1",
				map[string]string{
					"env": "prod",
				},
				"aws:///us-east-1a/i-1234567890abcdef0",
			),
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
				WithObjects(tt.node).
				Build()

			mock := &mockEC2Client{currentTags: tt.currentTags}

			r := &NodeLabelController{
				Client:       k8s,
				labels:       tt.labelsToCopy,
				cloud:        "aws",
				awsEC2Client: mock,
			}

			_, err := r.Reconcile(context.Background(), ctrl.Request{
				NamespacedName: client.ObjectKey{Name: tt.node.Name},
			})
			require.NoError(t, err)

			assert.Equal(t, tt.createsTags, mock.createdTags)
			assert.Equal(t, tt.deletesTags, mock.deletedTags)
		})
	}
}

func TestReconcileGCP(t *testing.T) {
	tests := []struct {
		name          string
		labelsToCopy  []string
		node          *corev1.Node
		currentLabels map[string]string
		wantLabels    map[string]string
	}{
		{
			name:          "sync new labels",
			labelsToCopy:  []string{"env", "team"},
			node:          createNode("node1", map[string]string{"env": "prod", "team": "platform"}, "gce://my-project/us-central1-a/instance-1"),
			currentLabels: map[string]string{"env": "staging"},
			wantLabels: map[string]string{
				"env":  "prod",
				"team": "platform",
			},
		},
		{
			name:         "preserve unmanaged labels",
			labelsToCopy: []string{"env"},
			node:         createNode("node1", map[string]string{"env": "prod"}, "gce://my-project/us-central1-a/instance-1"),
			currentLabels: map[string]string{
				"env":         "staging",
				"cost-center": "12345",
			},
			wantLabels: map[string]string{
				"env":         "prod",
				"cost-center": "12345",
			},
		},
		{
			name:         "remove label",
			labelsToCopy: []string{"env"},
			node:         createNode("node1", nil, "gce://my-project/us-central1-a/instance-1"),
			currentLabels: map[string]string{
				"env":         "prod",
				"cost-center": "12345",
			},
			wantLabels: map[string]string{
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
				WithObjects(tt.node).
				Build()

			mock := &mockGCEClient{instance: &gce.Instance{Labels: tt.currentLabels}}

			r := &NodeLabelController{
				Client:       k8s,
				labels:       tt.labelsToCopy,
				cloud:        "gcp",
				gcpGCEClient: mock,
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
		name            string
		oldLabels       map[string]string
		newLabels       map[string]string
		monitoredLabels []string
		want            bool
	}{
		{
			name:            "monitored label added",
			oldLabels:       nil,
			newLabels:       map[string]string{"env": "prod"},
			monitoredLabels: []string{"env"},
			want:            true,
		},
		{
			name:            "monitored label removed",
			oldLabels:       map[string]string{"env": "prod"},
			newLabels:       nil,
			monitoredLabels: []string{"env"},
			want:            true,
		},
		{
			name:            "monitored label value changed",
			oldLabels:       map[string]string{"env": "staging"},
			newLabels:       map[string]string{"env": "prod"},
			monitoredLabels: []string{"env"},
			want:            true,
		},
		{
			name:            "unmonitored label changed",
			oldLabels:       map[string]string{"foo": "bar"},
			newLabels:       map[string]string{"foo": "baz"},
			monitoredLabels: []string{"env"},
			want:            false,
		},
		{
			name:            "multiple monitored labels, one changed",
			oldLabels:       map[string]string{"env": "prod", "team": "platform"},
			newLabels:       map[string]string{"env": "prod", "team": "infra"},
			monitoredLabels: []string{"env", "team"},
			want:            true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			oldNode := createNode("node1", tt.oldLabels, "")
			newNode := createNode("node1", tt.newLabels, "")
			got := shouldProcessNodeUpdate(oldNode, newNode, tt.monitoredLabels)
			assert.Equal(t, tt.want, got)
		})
	}

	// extra safety test for nil node input
	assert.False(t, shouldProcessNodeUpdate(nil, nil, []string{"env"}))
}

func TestShouldProcessNodeCreate(t *testing.T) {
	tests := []struct {
		name            string
		labels          map[string]string
		monitoredLabels []string
		want            bool
	}{
		{
			name:            "node with monitored label",
			labels:          map[string]string{"env": "prod"},
			monitoredLabels: []string{"env"},
			want:            true,
		},
		{
			name:            "node without monitored label",
			labels:          map[string]string{"foo": "bar"},
			monitoredLabels: []string{"env"},
			want:            false,
		},
		{
			name:            "node with some monitored labels",
			labels:          map[string]string{"env": "prod", "foo": "bar", "team": "platform"},
			monitoredLabels: []string{"env", "team", "region"},
			want:            true,
		},
		{
			name:            "empty labels map",
			labels:          nil,
			monitoredLabels: []string{"env"},
			want:            false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			node := createNode("node1", tt.labels, "")
			got := shouldProcessNodeCreate(node, tt.monitoredLabels)
			assert.Equal(t, tt.want, got)
		})
	}

	// extra safety test for nil node input
	assert.False(t, shouldProcessNodeCreate(nil, []string{"env"}))
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

// ----

// func TestReconcile(t *testing.T) {
// 	tests := []struct {
// 		name      string
// 		node      *corev1.Node
// 		labelKeys []string
// 		wantErr   bool
// 		wantSync  bool // whether we expect cloud API calls
// 	}{
// 		{
// 			name: "node missing provider id",
// 			node: &corev1.Node{
// 				ObjectMeta: metav1.ObjectMeta{
// 					Name:   "node1",
// 					Labels: map[string]string{"environment": "prod"},
// 				},
// 			},
// 			labelKeys: []string{"environment"},
// 			wantErr:   false,
// 			wantSync:  false,
// 		},
// 		{
// 			name: "node with no matching labels",
// 			node: &corev1.Node{
// 				ObjectMeta: metav1.ObjectMeta{
// 					Name:   "node1",
// 					Labels: map[string]string{"foo": "bar"},
// 				},
// 				Spec: corev1.NodeSpec{
// 					ProviderID: "aws:///us-east-1c/i-123456",
// 				},
// 			},
// 			labelKeys: []string{"environment"},
// 			wantErr:   false,
// 			wantSync:  false,
// 		},
// 		{
// 			name: "successful sync",
// 			node: &corev1.Node{
// 				ObjectMeta: metav1.ObjectMeta{
// 					Name: "node1",
// 					Labels: map[string]string{
// 						"environment": "prod",
// 						"team":        "platform",
// 					},
// 				},
// 				Spec: corev1.NodeSpec{
// 					ProviderID: "aws:///us-east-1c/i-123456",
// 				},
// 			},
// 			labelKeys: []string{"environment", "team"},
// 			wantErr:   false,
// 			wantSync:  true,
// 		},
// 	}

// 	for _, tt := range tests {
// 		t.Run(tt.name, func(t *testing.T) {
// 			// Create a fake client
// 			scheme := runtime.NewScheme()
// 			_ = clientgoscheme.AddToScheme(scheme)
// 			fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(tt.node).Build()

// 			// Create a mock AWS client that tracks if CreateTags was called
// 			mockAWS := &mockEC2Client{
// 				createTagsCalled: false,
// 			}

// 			c := &NodeLabelController{
// 				Client:        fakeClient,
// 				awsEC2Client:  mockAWS,
// 				labelKeys:     tt.labelKeys,
// 				cloudProvider: "aws",
// 			}

// 			_, err := c.Reconcile(context.Background(), ctrl.Request{
// 				NamespacedName: types.NamespacedName{
// 					Name: tt.node.Name,
// 				},
// 			})

// 			if (err != nil) != tt.wantErr {
// 				t.Errorf("Reconcile() error = %v, wantErr %v", err, tt.wantErr)
// 			}

// 			if mockAWS.createTagsCalled != tt.wantSync {
// 				t.Errorf("Expected cloud sync = %v, got %v", tt.wantSync, mockAWS.createTagsCalled)
// 			}
// 		})
// 	}
// }

// // Mock AWS EC2 client
// type mockEC2Client struct {
// 	createTagsCalled bool
// }

// func (m *mockEC2Client) CreateTags(ctx context.Context, params *ec2.CreateTagsInput, optFns ...func(*ec2.Options)) (*ec2.CreateTagsOutput, error) {
// 	m.createTagsCalled = true
// 	return &ec2.CreateTagsOutput{}, nil
// }

func createNode(name string, labels map[string]string, providerID string) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:   name,
			Labels: labels,
		},
		Spec: corev1.NodeSpec{
			ProviderID: providerID,
		},
	}
}
