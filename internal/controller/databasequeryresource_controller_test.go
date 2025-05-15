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
		ResourceName      = "test-dbqr"
		ResourceNamespace = "default"
		SecretName        = "test-db-secret"
		timeout  = time.Second * 10
		interval = time.Millisecond * 250
	)

	Context("When reconciling a DatabaseQueryResource", func() {
		It("Should create the resource and update status", func() {
			By("Creating a Secret for DB connection")
			ctx := context.Background()
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      SecretName,
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
			Expect(k8sClient.Create(ctx, secret)).To(Succeed())

			By("Creating a DatabaseQueryResource")
			dbqr := &databasev1alpha1.DatabaseQueryResource{
				ObjectMeta: metav1.ObjectMeta{
					Name:      ResourceName,
					Namespace: ResourceNamespace,
				},
				Spec: databasev1alpha1.DatabaseQueryResourceSpec{
					PollInterval: "10s",
					Database: databasev1alpha1.DatabaseSpec{
						Type: "postgres",
						ConnectionSecretRef: databasev1alpha1.DatabaseConnectionSecretRef{
							Name:      SecretName,
							Namespace: ResourceNamespace,
						},
					},
					Query:    "SELECT 1 as id", // Simple query for test
					Template: `apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: test-cm-{{ .Row.id }}\n  namespace: default\ndata:\n  foo: bar`,
				},
			}
			Expect(k8sClient.Create(ctx, dbqr)).To(Succeed())

			lookupKey := types.NamespacedName{Name: ResourceName, Namespace: ResourceNamespace}
			created := &databasev1alpha1.DatabaseQueryResource{}

			By("Checking the DatabaseQueryResource is created and reconciled")
			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, lookupKey, created)).To(Succeed())
				g.Expect(created.Status.Conditions).NotTo(BeEmpty())
			}, timeout, interval).Should(Succeed())
		})

		It("Should create a ConfigMap with correct labels from mock DB", func() {
			ctx := context.Background()
			mock := &MockDatabaseClient{
				Rows:    []util.RowResult{{"id": 42}},
				Columns: []string{"id"},
			}

			// Patch the running reconciler's DBClientFactory for this test
			// (Assumes the reconciler is accessible as a package/global var, or you can set it in BeforeEach)
			// For demonstration, we'll assume a global variable TestReconciler is used in the manager setup
			TestReconciler.DBClientFactory = func(ctx context.Context, dbType string, dbConfig map[string]string) (util.DatabaseClient, error) {
				return mock, nil
			}

			dbqr := &databasev1alpha1.DatabaseQueryResource{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "mock-dbqr",
					Namespace: ResourceNamespace,
				},
				Spec: databasev1alpha1.DatabaseQueryResourceSpec{
					PollInterval: "10s",
					Database: databasev1alpha1.DatabaseSpec{
						Type: "mock",
						ConnectionSecretRef: databasev1alpha1.DatabaseConnectionSecretRef{
							Name:      "irrelevant",
							Namespace: ResourceNamespace,
						},
					},
					Query:    "SELECT 42 as id",
					Template: `apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: test-cm-{{ .Row.id }}\n  namespace: default\ndata:\n  foo: bar`,
				},
			}
			Expect(k8sClient.Create(ctx, dbqr)).To(Succeed())

			cmName := "test-cm-42"
			cmLookup := types.NamespacedName{Name: cmName, Namespace: ResourceNamespace}
			createdCM := &corev1.ConfigMap{}
			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, cmLookup, createdCM)).To(Succeed())
				labels := createdCM.GetLabels()
				g.Expect(labels).To(HaveKeyWithValue(ManagedByLabel, "mock-dbqr"))
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
