package controller

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/text/cases"
	"golang.org/x/text/language"

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
			Expect(k8sClient.Create(ctx, dummySecret)).To(Succeed())

			// Create the DatabaseQueryResource
			dbqr := &databasev1alpha1.DatabaseQueryResource{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "mock-dbqr",
					Namespace: ResourceNamespace,
				},
				Spec: databasev1alpha1.DatabaseQueryResourceSpec{
					PollInterval: "10s",
					Prune:        ptrBool(true),
					Database: databasev1alpha1.DatabaseSpec{
						Type: "postgres",
						ConnectionSecretRef: databasev1alpha1.DatabaseConnectionSecretRef{
							Name:      SecretName,
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
			mock.mu.Lock()
			mock.Rows = []util.RowResult{}
			mock.mu.Unlock()

			// Wait for more than the poll interval to allow the controller to reconcile and prune
			time.Sleep(12 * time.Second)

			// Assert that the ConfigMap is deleted
			Eventually(func(g Gomega) {
				err := k8sClient.Get(ctx, cmLookup, createdCM)
				g.Expect(err).To(HaveOccurred())
			}, timeout, interval).Should(Succeed())
		})
	})

	Describe("with multiple rows and advanced templating", func() {
		It("should create/update resources for each row and not prune when prune=false", func() {
			ctx := context.Background()
			mock := &MockDatabaseClient{
				Rows: []util.RowResult{
					{"id": 1, "name": "Alice", "age": 30},
					{"id": 2, "name": "Bob", "age": 25},
					{"id": 3, "name": "Charlie", "age": 40},
				},
				Columns: []string{"id", "name", "age"},
			}

			TestReconciler.DBClientFactory = func(ctx context.Context, dbType string, dbConfig map[string]string) (util.DatabaseClient, error) {
				return mock, nil
			}

			dummySecret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "multi-secret",
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

			dbqr := &databasev1alpha1.DatabaseQueryResource{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "multi-dbqr",
					Namespace: ResourceNamespace,
				},
				Spec: databasev1alpha1.DatabaseQueryResourceSpec{
					PollInterval: "10s",
					Prune:        ptrBool(false),
					Database: databasev1alpha1.DatabaseSpec{
						Type: "postgres",
						ConnectionSecretRef: databasev1alpha1.DatabaseConnectionSecretRef{
							Name:      "multi-secret",
							Namespace: ResourceNamespace,
						},
					},
					Query:    "SELECT id, name, age FROM users",
					Template: `apiVersion: v1
kind: ConfigMap
metadata:
  name: user-cm-{{ .Row.id }}
  namespace: default
  labels:
    {{- if eq (mod .Row.id 2) 0 }}even: "true"{{ else }}odd: "true"{{ end }}
    user-name: {{ .Row.name | lower }}
data:
  greeting: "Hello, {{ .Row.name | title }}! You are {{ .Row.age }} years old."
  id: "{{ .Row.id }}"
  age: "{{ .Row.age }}"`,
				},
			}
			Expect(k8sClient.Create(ctx, dbqr)).To(Succeed())

			// Wait for all ConfigMaps to be created
			for _, row := range mock.Rows {
				cmName := "user-cm-" + toString(row["id"])
				cmLookup := types.NamespacedName{Name: cmName, Namespace: ResourceNamespace}
				createdCM := &corev1.ConfigMap{}
				Eventually(func(g Gomega) {
					g.Expect(k8sClient.Get(ctx, cmLookup, createdCM)).To(Succeed())
					labels := createdCM.GetLabels()
					g.Expect(labels).To(HaveKeyWithValue(ManagedByLabel, "multi-dbqr"))
					g.Expect(labels).To(HaveKeyWithValue("user-name", strings.ToLower(row["name"].(string))))
					if row["id"].(int) % 2 == 0 {
						g.Expect(labels).To(HaveKeyWithValue("even", "true"))
					} else {
						g.Expect(labels).To(HaveKeyWithValue("odd", "true"))
					}
					greeting := "Hello, " + cases.Title(language.English).String(row["name"].(string)) + "! You are " + toString(row["age"]) + " years old."
					g.Expect(createdCM.Data).To(HaveKeyWithValue("greeting", greeting))
					g.Expect(createdCM.Data).To(HaveKeyWithValue("id", toString(row["id"])))
					g.Expect(createdCM.Data).To(HaveKeyWithValue("age", toString(row["age"])))
				}, timeout, interval).Should(Succeed())
			}

			// Now update the mock DB: change Bob's age, remove Charlie
			mock.mu.Lock()
			mock.Rows = []util.RowResult{
				{"id": 1, "name": "Alice", "age": 30},
				{"id": 2, "name": "Bob", "age": 26}, // Bob's age changed
			}
			mock.mu.Unlock()

			// Wait for more than the poll interval
			time.Sleep(12 * time.Second)

			// Alice and Bob should still exist, Charlie should still exist (prune=false), Bob's age should be updated
			for _, row := range []util.RowResult{
				{"id": 1, "name": "Alice", "age": 30},
				{"id": 2, "name": "Bob", "age": 26},
				{"id": 3, "name": "Charlie", "age": 40},
			} {
				cmName := "user-cm-" + toString(row["id"])
				cmLookup := types.NamespacedName{Name: cmName, Namespace: ResourceNamespace}
				createdCM := &corev1.ConfigMap{}
				Eventually(func(g Gomega) {
					g.Expect(k8sClient.Get(ctx, cmLookup, createdCM)).To(Succeed())
					if row["id"] == 2 {
						g.Expect(createdCM.Data).To(HaveKeyWithValue("age", "26"))
						g.Expect(createdCM.Data).To(HaveKeyWithValue("greeting", "Hello, Bob! You are 26 years old."))
					}
				}, timeout, interval).Should(Succeed())
			}
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
	mu        sync.RWMutex
}

func (m *MockDatabaseClient) Connect(ctx context.Context, config map[string]string) error { return nil }
func (m *MockDatabaseClient) Query(ctx context.Context, query string) ([]util.RowResult, []string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.FailQuery {
		return nil, nil, context.DeadlineExceeded
	}
	rowsCopy := make([]util.RowResult, len(m.Rows))
	copy(rowsCopy, m.Rows)
	columnsCopy := make([]string, len(m.Columns))
	copy(columnsCopy, m.Columns)
	return rowsCopy, columnsCopy, nil
}
func (m *MockDatabaseClient) Exec(ctx context.Context, query string) error {
	m.mu.Lock()
	m.ExecCalls = append(m.ExecCalls, query)
	m.mu.Unlock()
	return nil
}
func (m *MockDatabaseClient) Close(ctx context.Context) error { return nil }

// Helper for pointer to bool
func ptrBool(b bool) *bool { return &b }

// Helper for string conversion
func toString(val interface{}) string {
	switch v := val.(type) {
	case int:
		return strconv.Itoa(v)
	case int32:
		return strconv.Itoa(int(v))
	case int64:
		return strconv.FormatInt(v, 10)
	case string:
		return v
	default:
		return fmt.Sprintf("%v", v)
	}
}
