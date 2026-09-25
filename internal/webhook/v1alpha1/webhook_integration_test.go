// Copyright 2026 Konnektr.io
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package v1alpha1

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"

	databasev1alpha1 "github.com/konnektr-io/db-query-operator/api/v1alpha1"
)

// The specs below exercise the webhook through the API server, so they prove
// that an invalid DatabaseQueryResource is rejected at admission time rather
// than surfacing as a reconciliation error.
var _ = Describe("DatabaseQueryResource validating webhook", func() {
	Context("when creating a DatabaseQueryResource", func() {
		It("accepts a valid resource", func() {
			dbqr := validResource()
			dbqr.Name = "webhook-valid"
			Expect(k8sClient.Create(ctx, dbqr)).To(Succeed())
			DeferCleanup(func() {
				Expect(k8sClient.Delete(ctx, dbqr)).To(Succeed())
			})
		})

		DescribeTable("rejects an invalid resource",
			func(mutate func(*databasev1alpha1.DatabaseQueryResource), wantField string, rejectedByWebhook bool) {
				dbqr := validResource()
				dbqr.Name = "webhook-invalid"
				mutate(dbqr)

				err := k8sClient.Create(ctx, dbqr)
				Expect(apierrors.IsInvalid(err)).To(BeTrue(), "expected an Invalid error, got %v", err)
				Expect(err.Error()).To(ContainSubstring(wantField))
				if rejectedByWebhook {
					// Only the webhook can produce this message: without it the
					// API server would accept the resource.
					Expect(err.Error()).To(ContainSubstring("admission webhook"))
				}
			},
			Entry("unparseable pollInterval (already rejected by the CRD pattern)", func(r *databasev1alpha1.DatabaseQueryResource) {
				r.Spec.PollInterval = "5 minutes"
			}, "spec.pollInterval", false),
			Entry("zero pollInterval", func(r *databasev1alpha1.DatabaseQueryResource) {
				r.Spec.PollInterval = "0s"
			}, "spec.pollInterval", true),
			Entry("overflowing pollInterval", func(r *databasev1alpha1.DatabaseQueryResource) {
				r.Spec.PollInterval = "9999999999999999999h"
			}, "spec.pollInterval", true),
			Entry("empty query", func(r *databasev1alpha1.DatabaseQueryResource) {
				r.Spec.Query = "   "
			}, "spec.query", true),
			Entry("malformed template", func(r *databasev1alpha1.DatabaseQueryResource) {
				r.Spec.Template = "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: {{ .Row.username"
			}, "spec.template", true),
			Entry("template with an unknown function", func(r *databasev1alpha1.DatabaseQueryResource) {
				r.Spec.Template = "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: {{ noSuchFunction .Row.username }}"
			}, "spec.template", true),
			Entry("empty connectionSecretRef name", func(r *databasev1alpha1.DatabaseQueryResource) {
				r.Spec.Database.ConnectionSecretRef.Name = ""
			}, "spec.database.connectionSecretRef.name", true),
			Entry("unparseable changePollInterval (already rejected by the CRD pattern)", func(r *databasev1alpha1.DatabaseQueryResource) {
				r.Spec.ChangeDetection = &databasev1alpha1.ChangeDetectionConfig{
					Enabled:            true,
					TableName:          "users",
					TimestampColumn:    "updated_at",
					ChangePollInterval: "10 seconds",
				}
			}, "spec.changeDetection.changePollInterval", false),
		)
	})

	Context("when updating a DatabaseQueryResource", func() {
		const name = "webhook-update"

		BeforeEach(func() {
			dbqr := validResource()
			dbqr.Name = name
			Expect(k8sClient.Create(ctx, dbqr)).To(Succeed())
			DeferCleanup(func() {
				current := &databasev1alpha1.DatabaseQueryResource{}
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: "default"}, current); err == nil {
					Expect(k8sClient.Delete(ctx, current)).To(Succeed())
				}
			})
		})

		It("rejects an update that introduces an invalid template", func() {
			current := &databasev1alpha1.DatabaseQueryResource{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: "default"}, current)).To(Succeed())

			current.Spec.Template = "{{ .Row.username"
			err := k8sClient.Update(ctx, current)
			Expect(apierrors.IsInvalid(err)).To(BeTrue(), "expected an Invalid error, got %v", err)
			Expect(err.Error()).To(ContainSubstring("spec.template"))

			By("leaving the stored resource untouched")
			stored := &databasev1alpha1.DatabaseQueryResource{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: "default"}, stored)).To(Succeed())
			Expect(stored.Spec.Template).To(Equal(validTemplate))
		})

		It("accepts an update that keeps the resource valid", func() {
			current := &databasev1alpha1.DatabaseQueryResource{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: "default"}, current)).To(Succeed())

			current.Spec.PollInterval = "10m"
			Expect(k8sClient.Update(ctx, current)).To(Succeed())
		})
	})
})
