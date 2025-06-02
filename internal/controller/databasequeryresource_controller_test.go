package controller

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	databasev1alpha1 "github.com/konnektr-io/db-query-operator/api/v1alpha1"
	"github.com/konnektr-io/db-query-operator/internal/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"golang.org/x/text/cases"
	"golang.org/x/text/language"
	appsv1 "k8s.io/api/apps/v1"
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

	Describe("When reconciling a DatabaseQueryResource", func() {
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
    {{- if eq (mod .Row.id 2) 0 }}
    even: "true"
    {{- else }}
    odd: "true"
    {{- end }}
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

			// Debug: Assert all three ConfigMaps exist before DB update
			for _, id := range []int{1, 2, 3} {
				cmName := "user-cm-" + strconv.Itoa(id)
				cmLookup := types.NamespacedName{Name: cmName, Namespace: ResourceNamespace}
				createdCM := &corev1.ConfigMap{}
				Ω(k8sClient.Get(ctx, cmLookup, createdCM)).Should(Succeed(), "ConfigMap %s should exist before DB update", cmName)
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

			// Debug: Assert all three ConfigMaps still exist after DB update (prune=false)
			for _, id := range []int{1, 2, 3} {
				cmName := "user-cm-" + strconv.Itoa(id)
				cmLookup := types.NamespacedName{Name: cmName, Namespace: ResourceNamespace}
				createdCM := &corev1.ConfigMap{}
				Ω(k8sClient.Get(ctx, cmLookup, createdCM)).Should(Succeed(), "ConfigMap %s should still exist after DB update", cmName)
			}

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

	Describe("DatabaseQueryResource child resource state change", func() {
		It("should update parent status and execute status update query when a Deployment changes state", func() {
			ctx := context.Background()
			mock := &MockDatabaseClient{
				Rows: []util.RowResult{
					{"name": "my-deploy", "status": "Pending"},
				},
				Columns: []string{"name", "status"},
			}

			TestReconciler.DBClientFactory = func(ctx context.Context, dbType string, dbConfig map[string]string) (util.DatabaseClient, error) {
				return mock, nil
			}

			// Create a dummy Secret required by the controller
			dummySecret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "deploy-secret",
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

			// Create the DatabaseQueryResource with a Deployment template and status update query
			statusUpdateQuery := `UPDATE deployments SET status = '{{ .Resource.status.availableReplicas | default 0 }}' WHERE name = '{{ .Resource.metadata.name }}';`
			dbqr := &databasev1alpha1.DatabaseQueryResource{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "deploy-dbqr",
					Namespace: ResourceNamespace,
				},
				Spec: databasev1alpha1.DatabaseQueryResourceSpec{
					PollInterval: "10s",
					Prune:        ptrBool(true),
					Database: databasev1alpha1.DatabaseSpec{
						Type: "postgres",
						ConnectionSecretRef: databasev1alpha1.DatabaseConnectionSecretRef{
							Name:      "deploy-secret",
							Namespace: ResourceNamespace,
						},
					},
					Query: "SELECT 'my-deploy' as name, 'Pending' as status",
					Template: `apiVersion: apps/v1
kind: Deployment
metadata:
  name: {{ .Row.name }}
  namespace: default
spec:
  replicas: 1
  selector:
    matchLabels:
      app: {{ .Row.name }}
  template:
    metadata:
      labels:
        app: {{ .Row.name }}
    spec:
      containers:
      - name: nginx
        image: nginx:1.14.2
        ports:
        - containerPort: 80`,
					StatusUpdateQueryTemplate: statusUpdateQuery,
				},
			}
			Expect(k8sClient.Create(ctx, dbqr)).To(Succeed())

			// Wait for the Deployment to be created
			deployName := "my-deploy"
			deployLookup := types.NamespacedName{Name: deployName, Namespace: ResourceNamespace}
			createdDeploy := &appsv1.Deployment{}
			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, deployLookup, createdDeploy)).To(Succeed())
			}, timeout, interval).Should(Succeed())

			// Patch the Deployment status to simulate readiness
			createdDeploy.Status.Replicas = 1
			createdDeploy.Status.ReadyReplicas = 1
			createdDeploy.Status.AvailableReplicas = 1
			Expect(k8sClient.Status().Update(ctx, createdDeploy)).To(Succeed())

			// Wait for the Deployment to become available (ready)
			Eventually(func(g Gomega) {
				g.Expect(createdDeploy.Status.AvailableReplicas).To(BeNumerically(">=", 1))
			}, timeout*2, interval).Should(Succeed())

			// Wait for the parent dbqr status to be updated
			lookupKey := types.NamespacedName{Name: "deploy-dbqr", Namespace: ResourceNamespace}
			created := &databasev1alpha1.DatabaseQueryResource{}
			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, lookupKey, created)).To(Succeed())
				found := false
				for _, cond := range created.Status.Conditions {
					if cond.Reason == "Success" {
						found = true
					}
				}
				if !found {
					fmt.Printf("Current conditions: %+v\n", created.Status.Conditions)
				}
				g.Expect(found).To(BeTrue())
			}, timeout*2, interval).Should(Succeed())

			// Check that the mock DB Exec was called with the expected status update query
			Eventually(func(g Gomega) {
				mock.mu.RLock()
				defer mock.mu.RUnlock()
				found := false
				for _, q := range mock.ExecCalls {
					if strings.Contains(q, "UPDATE deployments SET status") && strings.Contains(q, "my-deploy") {
						found = true
					}
				}
				g.Expect(found).To(BeTrue())
			}, timeout*2, interval).Should(Succeed())
		})
	})

	Describe("Finalizer cleanup logic", func() {
		It("should delete managed resources and remove the finalizer when the CR is deleted and the finalizer is set", func() {
			ctx := context.Background()
			mock := &MockDatabaseClient{
				Rows:    []util.RowResult{{"id": 99}},
				Columns: []string{"id"},
			}

			TestReconciler.DBClientFactory = func(ctx context.Context, dbType string, dbConfig map[string]string) (util.DatabaseClient, error) {
				return mock, nil
			}

			dummySecret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "finalizer-secret",
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

			// Create the DatabaseQueryResource with the finalizer set
			dbqr := &databasev1alpha1.DatabaseQueryResource{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "finalizer-dbqr",
					Namespace:  ResourceNamespace,
					Finalizers: []string{"konnektr.io/databasequeryresource-finalizer"},
				},
				Spec: databasev1alpha1.DatabaseQueryResourceSpec{
					PollInterval: "10s",
					Prune:        ptrBool(true),
					Database: databasev1alpha1.DatabaseSpec{
						Type: "postgres",
						ConnectionSecretRef: databasev1alpha1.DatabaseConnectionSecretRef{
							Name:      "finalizer-secret",
							Namespace: ResourceNamespace,
						},
					},
					Query:    "SELECT 99 as id",
					Template: `apiVersion: v1
kind: ConfigMap
metadata:
  name: finalizer-cm-{{ .Row.id }}
  namespace: default
data:
  foo: bar`,
				},
			}
			Expect(k8sClient.Create(ctx, dbqr)).To(Succeed())

			// Wait for the ConfigMap to be created
			cmName := "finalizer-cm-99"
			cmLookup := types.NamespacedName{Name: cmName, Namespace: ResourceNamespace}
			createdCM := &corev1.ConfigMap{}
			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, cmLookup, createdCM)).To(Succeed())
			}, timeout, interval).Should(Succeed())

			// Delete the DatabaseQueryResource
			lookupKey := types.NamespacedName{Name: "finalizer-dbqr", Namespace: ResourceNamespace}
			Expect(k8sClient.Delete(ctx, dbqr)).To(Succeed())

			// The ConfigMap should be deleted by the finalizer logic
			Eventually(func(g Gomega) {
				err := k8sClient.Get(ctx, cmLookup, createdCM)
				g.Expect(err).To(HaveOccurred())
			}, timeout*2, interval).Should(Succeed())

			// The DatabaseQueryResource should eventually be deleted (finalizer removed)
			Eventually(func(g Gomega) {
				err := k8sClient.Get(ctx, lookupKey, &databasev1alpha1.DatabaseQueryResource{})
				g.Expect(err).To(HaveOccurred())
			}, timeout*2, interval).Should(Succeed())
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
