package controller

import (
	"context"
	"testing"
	"time"

	"github.com/go-logr/logr"
	databasev1alpha1 "github.com/konnektr-io/db-query-operator/api/v1alpha1"
	"github.com/konnektr-io/db-query-operator/internal/util"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestShouldReconcile_SkipBug(t *testing.T) {
	g := NewWithT(t)

	// Setup Scheme
	scheme := runtime.NewScheme()
	_ = databasev1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)

	// Setup fake client with a secret for DB config (needed if detectChanges is called)
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "db-secret",
			Namespace: "default",
		},
		Data: map[string][]byte{
			"host": []byte("localhost"),
		},
	}

	k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(secret).Build()

	// Setup Reconciler
	r := &DatabaseQueryResourceReconciler{
		Client: k8sClient,
		Scheme: scheme,
		DBClientFactory: func(ctx context.Context, dbType string, dbConfig map[string]string) (util.DatabaseClient, error) {
			return &util.MockDatabaseClient{
				Rows: []util.RowResult{}, // No changes
			}, nil
		},
	}

	// Test case parameters
	pollInterval := 5 * time.Minute
	now := metav1.Now()

	// LastReconcileTime is RECENT (e.g. 10 seconds ago) - likely due to a child status update
	lastReconcile := metav1.NewTime(now.Time.Add(-10 * time.Second))

	// LastPollTime is OLD (e.g. 10 minutes ago) - full poll interval has passed!
	lastPoll := metav1.NewTime(now.Time.Add(-10 * time.Minute))

	dbqr := &databasev1alpha1.DatabaseQueryResource{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-dbqr",
			Namespace: "default",
		},
		Spec: databasev1alpha1.DatabaseQueryResourceSpec{
			PollInterval: "5m",
			Database: databasev1alpha1.DatabaseSpec{
				ConnectionSecretRef: databasev1alpha1.DatabaseConnectionSecretRef{
					Name: "db-secret",
				},
			},
			ChangeDetection: &databasev1alpha1.ChangeDetectionConfig{
				Enabled:            true,
				ChangePollInterval: "10s",
				TableName:          "foo",
				TimestampColumn:    "updated_at",
			},
		},
		Status: databasev1alpha1.DatabaseQueryResourceStatus{
			LastReconcileTime:   &lastReconcile,
			LastPollTime:        &lastPoll,
			LastChangeCheckTime: &lastReconcile, // Recently checked
		},
	}

	// Execute shouldReconcile
	shouldFullReconcile, _ := r.shouldReconcile(context.Background(), dbqr, logr.Discard(), pollInterval)

	// We EXPECT full reconciliation because 10m have passed since the last full poll
	g.Expect(shouldFullReconcile).To(BeTrue(), "Should trigger full reconciliation based on LastPollTime despite recent LastReconcileTime")
}

func TestShouldReconcile_ObservedGenerationForcesReconcile(t *testing.T) {
	g := NewWithT(t)

	// Setup Scheme
	scheme := runtime.NewScheme()
	_ = databasev1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)

	// Setup fake client
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).Build()

	r := &DatabaseQueryResourceReconciler{
		Client: k8sClient,
		Scheme: scheme,
		DBClientFactory: func(ctx context.Context, dbType string, dbConfig map[string]string) (util.DatabaseClient, error) {
			return &util.MockDatabaseClient{}, nil
		},
	}

	pollInterval := 1 * time.Minute

	dbqr := &databasev1alpha1.DatabaseQueryResource{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-dbqr-gen",
			Namespace:  "default",
			Generation: 5,
		},
		Spec: databasev1alpha1.DatabaseQueryResourceSpec{
			PollInterval: "1m",
			Database: databasev1alpha1.DatabaseSpec{
				ConnectionSecretRef: databasev1alpha1.DatabaseConnectionSecretRef{
					Name: "db-secret",
				},
			},
			ChangeDetection: &databasev1alpha1.ChangeDetectionConfig{
				Enabled:            true,
				ChangePollInterval: "10s",
				TableName:          "foo",
				TimestampColumn:    "updated_at",
			},
		},
		Status: databasev1alpha1.DatabaseQueryResourceStatus{
			ObservedGeneration: 4, // Less than Generation, should force reconcile
		},
	}

	shouldFullReconcile, _ := r.shouldReconcile(context.Background(), dbqr, logr.Discard(), pollInterval)
	g.Expect(shouldFullReconcile).To(BeTrue(), "Should trigger full reconciliation when ObservedGeneration < Generation")
}

func TestFieldRemovalOnUpdate(t *testing.T) {
	g := NewWithT(t)

	scheme := runtime.NewScheme()
	_ = databasev1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)

	// Use a real k8s resource (ConfigMap) for testing
	existing := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-cm",
			Namespace: "default",
			Labels: map[string]string{
				ManagedByLabel: "test-dbqr",
			},
			Annotations: map[string]string{
				"existing-annotation": "should-survive",
			},
		},
		Data: map[string]string{
			"old-field":  "old-value",
			"keep-field": "keep-value",
		},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(existing).Build()

	// Retrieve the existing ConfigMap
	got := &corev1.ConfigMap{}
	err := fakeClient.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: "test-cm"}, got)
	g.Expect(err).ToNot(HaveOccurred())

	// Verify old-field exists initially
	g.Expect(got.Data).To(HaveKey("old-field"))
	g.Expect(got.Data).To(HaveKey("keep-field"))

	// Create the "desired" object (simulating what the template would produce)
	// with old-field removed
	desired := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-cm",
			Namespace: "default",
			Labels: map[string]string{
				ManagedByLabel: "test-dbqr",
			},
		},
		Data: map[string]string{
			"keep-field": "keep-value",
		},
	}

	// Convert to unstructured for testing the update logic
	desiredUnstructured := &unstructured.Unstructured{}
	desiredUnstructured.Object, _ = runtime.DefaultUnstructuredConverter.ToUnstructured(desired)
	desiredUnstructured.SetGroupVersionKind(schema.GroupVersionKind{Group: "", Version: "v1", Kind: "ConfigMap"})

	existingUnstructured := &unstructured.Unstructured{}
	existingUnstructured.Object, _ = runtime.DefaultUnstructuredConverter.ToUnstructured(got)
	existingUnstructured.SetGroupVersionKind(schema.GroupVersionKind{Group: "", Version: "v1", Kind: "ConfigMap"})

	// Simulate the update logic from the controller:
	// Copy desired spec/data into existing, preserving system annotations
	existingUnstructured.Object["data"] = desiredUnstructured.Object["data"]
	existingUnstructured.SetLabels(desiredUnstructured.GetLabels())

	// Merge annotations
	existingAnns := existingUnstructured.GetAnnotations()
	if existingAnns == nil {
		existingAnns = make(map[string]string)
	}
	desiredAnns := desiredUnstructured.GetAnnotations()
	if desiredAnns != nil {
		for k, v := range desiredAnns {
			existingAnns[k] = v
		}
	}
	existingUnstructured.SetAnnotations(existingAnns)

	// Apply via Update
	err = fakeClient.Update(context.Background(), existingUnstructured)
	g.Expect(err).ToNot(HaveOccurred())

	// Verify the update went through correctly
	updated := &corev1.ConfigMap{}
	err = fakeClient.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: "test-cm"}, updated)
	g.Expect(err).ToNot(HaveOccurred())

	// The key assertion: old-field must be GONE after update
	g.Expect(updated.Data).ToNot(HaveKey("old-field"),
		"Field 'old-field' should be removed after update when it's absent from the desired state")
	g.Expect(updated.Data).To(HaveKeyWithValue("keep-field", "keep-value"),
		"Field 'keep-field' should be preserved")

	// System annotations should survive
	g.Expect(updated.Annotations).To(HaveKey("existing-annotation"),
		"Existing system annotation should survive merge")

	// Managed-by label should be set
	g.Expect(updated.Labels).To(HaveKeyWithValue(ManagedByLabel, "test-dbqr"),
		"Managed-by label should be set")
}

func TestSanitizePGIdentifier(t *testing.T) {
	tests := []struct {
		name       string
		identifier string
		want       string
		wantErr    bool
	}{
		{"simple table name", "users", `"users"`, false},
		{"with underscore", "my_table", `"my_table"`, false},
		{"with dollar sign", "my$table", `"my$table"`, false},
		{"schema qualified", "public.users", `"public"."users"`, false},
		{"already quoted", `"users"`, `"users"`, false},
		{"already quoted with schema", `"public"."users"`, `"public"."users"`, false},
		{"quoted with space", `"my table"`, `"my table"`, false},
		{"quoted with embedded quote", `"my""table"`, `"my""table"`, false},
		{"numeric start", "1table", "", true},
		{"embedded double quote in unquoted", `my"table`, "", true},
		{"special chars", "table;name", "", true},
		{"SQL injection attempt", "users; DROP TABLE users", "", true},
		{"empty string", "", "", true},
		{"just a dot", ".", "", true},
		{"dot at end", "table.", "", true},
		{"dot at start", ".table", "", true},
		{"unicode letters", "über", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := NewWithT(t)
			got, err := sanitizePGIdentifier(tt.identifier)
			if tt.wantErr {
				g.Expect(err).To(HaveOccurred())
			} else {
				g.Expect(err).ToNot(HaveOccurred())
				g.Expect(got).To(Equal(tt.want))
			}
		})
	}
}
