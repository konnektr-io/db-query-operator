// SPDX-License-Identifier: Apache-2.0
// Copyright The Kubernetes authors.

package controller

import (
	"context"
	"time"

	databasev1alpha1 "github.com/konnektr-io/db-query-operator/api/v1alpha1"
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
	})
})
