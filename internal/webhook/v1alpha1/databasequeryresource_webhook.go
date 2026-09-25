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
	"context"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	databasev1alpha1 "github.com/konnektr-io/db-query-operator/api/v1alpha1"
)

// +kubebuilder:webhook:path=/validate-konnektr-io-v1alpha1-databasequeryresource,mutating=false,failurePolicy=fail,sideEffects=None,groups=konnektr.io,resources=databasequeryresources,verbs=create;update,versions=v1alpha1,name=vdatabasequeryresource-v1alpha1.konnektr.io,admissionReviewVersions=v1

// DatabaseQueryResourceCustomValidator validates DatabaseQueryResource resources
// before the API server persists them, so an invalid spec is rejected at
// creation/update time instead of surfacing as a reconciliation error.
type DatabaseQueryResourceCustomValidator struct{}

var _ admission.Validator[*databasev1alpha1.DatabaseQueryResource] = &DatabaseQueryResourceCustomValidator{}

// ValidateCreate rejects invalid DatabaseQueryResources.
func (v *DatabaseQueryResourceCustomValidator) ValidateCreate(_ context.Context, obj *databasev1alpha1.DatabaseQueryResource) (admission.Warnings, error) {
	return ValidateDatabaseQueryResource(obj)
}

// ValidateUpdate rejects updates that would leave the resource invalid.
func (v *DatabaseQueryResourceCustomValidator) ValidateUpdate(_ context.Context, _, newObj *databasev1alpha1.DatabaseQueryResource) (admission.Warnings, error) {
	return ValidateDatabaseQueryResource(newObj)
}

// ValidateDelete accepts every deletion.
func (v *DatabaseQueryResourceCustomValidator) ValidateDelete(_ context.Context, _ *databasev1alpha1.DatabaseQueryResource) (admission.Warnings, error) {
	return nil, nil
}

// SetupWithManager registers the validating webhook with the given manager.
func (v *DatabaseQueryResourceCustomValidator) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr, &databasev1alpha1.DatabaseQueryResource{}).
		WithValidator(v).
		Complete()
}
