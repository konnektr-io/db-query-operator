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
	"k8s.io/apimachinery/pkg/runtime"
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

