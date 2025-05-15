// SPDX-License-Identifier: Apache-2.0
// Copyright The Kubernetes authors.

package controller

import (
	"context"
	"time"

	databasev1alpha1 "github.com/konnektr-io/db-query-operator/api/v1alpha1"
	"github.com/konnektr-io/db-query-operator/internal/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

var _ = Describe("DatabaseQueryResource controller", func() {
	const (
		ResourceNamespace = "default"
		SecretName        = "test-db-secret"
		timeout  = time.Second * 10
		interval = time.Millisecond * 250
	)

	Context("When reconciling a DatabaseQueryResource", func() {
		It("Should create a ConfigMap with correct labels and update status using mock DB", func() {
			ctx := context.Background()
			mock := &MockDatabaseClient{
				Rows:    []util.RowResult{{"id": 42}},
				Columns: []string{"id"},
			}

			// Patch the running reconciler's DBClientFactory for this test
			TestReconciler.DBClientFactory = func(ctx context.Context, dbType string, dbConfig map[string]string) (util.DatabaseClient, error) {
				return mock, nil
			}

			// Create a dummy Secret required by the controller
			dummySecret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "irrelevant",
					Namespace: ResourceNamespace,
				},
				Data: map[string][]byte{
					"host":     []byte("localhost"),
					"port":     []byte("5432"),
					"username": []byte("testuser"),
					"password": []byte("testpass"),
					"dbname":   []byte("testdb"),
					"sslmode":  []byte("disable"),
				},
			}
			Expect(k8sClient.Create(ctx, dummySecret)).To(Succeed())

			// Create the DatabaseQueryResource
			dbqr := &databasev1alpha1.DatabaseQueryResource{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "mock-dbqr",
					Namespace: ResourceNamespace,
				},
				Spec: databasev1alpha1.DatabaseQueryResourceSpec{
					PollInterval: "10s",
					Database: databasev1alpha1.DatabaseSpec{
						Type: "postgres",
						ConnectionSecretRef: databasev1alpha1.DatabaseConnectionSecretRef{
							Name:      "irrelevant",
							Namespace: ResourceNamespace,
						},
					},
					Query:    "SELECT 42 as id",
					Template: `apiVersion: v1
kind: ConfigMap
metadata:
  name: test-cm-{{ .Row.id }}
  namespace: default
data:
  foo: bar`,
				},
			}
			Expect(k8sClient.Create(ctx, dbqr)).To(Succeed())

			// Check the DatabaseQueryResource is created and reconciled
			lookupKey := types.NamespacedName{Name: "mock-dbqr", Namespace: ResourceNamespace}
			created := &databasev1alpha1.DatabaseQueryResource{}
			By("Checking the DatabaseQueryResource is created and reconciled")
			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, lookupKey, created)).To(Succeed())
				g.Expect(created.Status.Conditions).NotTo(BeEmpty())
			}, timeout, interval).Should(Succeed())

			// Check the ConfigMap is created with correct labels and data
			cmName := "test-cm-42"
			cmLookup := types.NamespacedName{Name: cmName, Namespace: ResourceNamespace}
			createdCM := &corev1.ConfigMap{}
			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, cmLookup, createdCM)).To(Succeed())
				labels := createdCM.GetLabels()
				g.Expect(labels).To(HaveKeyWithValue(ManagedByLabel, "mock-dbqr"))
				// Assert ConfigMap data
				g.Expect(createdCM.Data).To(HaveKeyWithValue("foo", "bar"))
			}, timeout, interval).Should(Succeed())

			// Remove all rows from the mock DB to simulate the resource disappearing from the database
			mock.Rows = []util.RowResult{}

			// Wait for more than the poll interval to allow the controller to reconcile and prune
			time.Sleep(12 * time.Second)

			// Assert that the ConfigMap is deleted
			Eventually(func(g Gomega) {
				err := k8sClient.Get(ctx, cmLookup, createdCM)
				g.Expect(err).To(HaveOccurred())
			}, timeout, interval).Should(Succeed())
		})
	})
})

// MockDatabaseClient implements util.DatabaseClient for testing
// It returns configurable results and errors

type MockDatabaseClient struct {
	Rows      []util.RowResult
	Columns   []string
	ExecCalls []string
	FailQuery bool
}

func (m *MockDatabaseClient) Connect(ctx context.Context, config map[string]string) error { return nil }
func (m *MockDatabaseClient) Query(ctx context.Context, query string) ([]util.RowResult, []string, error) {
	if m.FailQuery {
		return nil, nil, context.DeadlineExceeded
	}
	return m.Rows, m.Columns, nil
}
func (m *MockDatabaseClient) Exec(ctx context.Context, query string) error {
	m.ExecCalls = append(m.ExecCalls, query)
	return nil
}
func (m *MockDatabaseClient) Close(ctx context.Context) error { return nil }
