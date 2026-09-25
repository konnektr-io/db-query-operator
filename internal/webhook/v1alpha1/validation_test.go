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
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	databasev1alpha1 "github.com/konnektr-io/db-query-operator/api/v1alpha1"
)

const validTemplate = `apiVersion: v1
kind: ConfigMap
metadata:
  name: user-{{ .Row.username | lower }}-config
data:
  email: "{{ .Row.email }}"
`

func validResource() *databasev1alpha1.DatabaseQueryResource {
	return &databasev1alpha1.DatabaseQueryResource{
		ObjectMeta: metav1.ObjectMeta{Name: "test-resource", Namespace: "default"},
		Spec: databasev1alpha1.DatabaseQueryResourceSpec{
			PollInterval: "1m",
			Database: databasev1alpha1.DatabaseSpec{
				Type: "postgres",
				ConnectionSecretRef: databasev1alpha1.DatabaseConnectionSecretRef{
					Name: "db-credentials",
				},
			},
			Query:    "SELECT username FROM users;",
			Template: validTemplate,
		},
	}
}

func TestValidateDatabaseQueryResource(t *testing.T) {
	tests := []struct {
		name string
		// mutate is applied to a valid resource before validation.
		mutate func(*databasev1alpha1.DatabaseQueryResource)
		// wantErrFields lists the field paths that must appear in the error.
		wantErrFields []string
		wantWarnings  []string
	}{
		{
			name:   "valid resource is accepted",
			mutate: func(*databasev1alpha1.DatabaseQueryResource) {},
		},
		{
			name: "pollInterval with a valid compound duration is accepted",
			mutate: func(r *databasev1alpha1.DatabaseQueryResource) {
				r.Spec.PollInterval = "1h30m"
			},
		},
		{
			name: "empty pollInterval is rejected",
			mutate: func(r *databasev1alpha1.DatabaseQueryResource) {
				r.Spec.PollInterval = ""
			},
			wantErrFields: []string{"spec.pollInterval"},
		},
		{
			name: "unparseable pollInterval is rejected",
			mutate: func(r *databasev1alpha1.DatabaseQueryResource) {
				r.Spec.PollInterval = "5 minutes"
			},
			wantErrFields: []string{"spec.pollInterval"},
		},
		{
			name: "zero pollInterval is rejected",
			mutate: func(r *databasev1alpha1.DatabaseQueryResource) {
				r.Spec.PollInterval = "0s"
			},
			wantErrFields: []string{"spec.pollInterval"},
		},
		{
			name: "negative pollInterval is rejected",
			mutate: func(r *databasev1alpha1.DatabaseQueryResource) {
				r.Spec.PollInterval = "-1m"
			},
			wantErrFields: []string{"spec.pollInterval"},
		},
		{
			name: "empty query is rejected",
			mutate: func(r *databasev1alpha1.DatabaseQueryResource) {
				r.Spec.Query = ""
			},
			wantErrFields: []string{"spec.query"},
		},
		{
			name: "whitespace only query is rejected",
			mutate: func(r *databasev1alpha1.DatabaseQueryResource) {
				r.Spec.Query = "   \n\t"
			},
			wantErrFields: []string{"spec.query"},
		},
		{
			name: "empty template is rejected",
			mutate: func(r *databasev1alpha1.DatabaseQueryResource) {
				r.Spec.Template = ""
			},
			wantErrFields: []string{"spec.template"},
		},
		{
			name: "whitespace only template is rejected",
			mutate: func(r *databasev1alpha1.DatabaseQueryResource) {
				r.Spec.Template = "   \n"
			},
			wantErrFields: []string{"spec.template"},
		},
		{
			name: "template with unclosed action is rejected",
			mutate: func(r *databasev1alpha1.DatabaseQueryResource) {
				r.Spec.Template = "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: {{ .Row.username"
			},
			wantErrFields: []string{"spec.template"},
		},
		{
			name: "template with an unknown function is rejected",
			mutate: func(r *databasev1alpha1.DatabaseQueryResource) {
				r.Spec.Template = "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: {{ noSuchFunction .Row.username }}"
			},
			wantErrFields: []string{"spec.template"},
		},
		{
			name: "template may use the functions available to the reconciler",
			mutate: func(r *databasev1alpha1.DatabaseQueryResource) {
				r.Spec.Template = `apiVersion: v1
kind: ConfigMap
metadata:
  name: {{ .Row.username | lower | trunc 20 }}
  labels: {{ .Row | toYaml | nindent 4 }}
data:
  created: "{{ now | date "2006-01-02T15:04:05Z07:00" }}"
  hash: {{ .Row.email | sha256sum }}
`
			},
		},
		{
			name: "empty connectionSecretRef name is rejected",
			mutate: func(r *databasev1alpha1.DatabaseQueryResource) {
				r.Spec.Database.ConnectionSecretRef.Name = ""
			},
			wantErrFields: []string{"spec.database.connectionSecretRef.name"},
		},
		{
			name: "whitespace only connectionSecretRef name is rejected",
			mutate: func(r *databasev1alpha1.DatabaseQueryResource) {
				r.Spec.Database.ConnectionSecretRef.Name = "  "
			},
			wantErrFields: []string{"spec.database.connectionSecretRef.name"},
		},
		{
			name: "template together with a status update query template is accepted",
			mutate: func(r *databasev1alpha1.DatabaseQueryResource) {
				r.Spec.StatusUpdateQueryTemplate = "UPDATE users SET synced = true WHERE id = {{ .Row.id }};"
			},
		},
		{
			name: "invalid statusUpdateQueryTemplate is rejected",
			mutate: func(r *databasev1alpha1.DatabaseQueryResource) {
				r.Spec.StatusUpdateQueryTemplate = "UPDATE users SET synced = {{ .Row.id "
			},
			wantErrFields: []string{"spec.statusUpdateQueryTemplate"},
		},
		{
			name: "change detection with a valid configuration is accepted",
			mutate: func(r *databasev1alpha1.DatabaseQueryResource) {
				r.Spec.ChangeDetection = &databasev1alpha1.ChangeDetectionConfig{
					Enabled:            true,
					TableName:          "users",
					TimestampColumn:    "updated_at",
					ChangePollInterval: "10s",
				}
			},
		},
		{
			name: "change detection with an unparseable changePollInterval is rejected",
			mutate: func(r *databasev1alpha1.DatabaseQueryResource) {
				r.Spec.ChangeDetection = &databasev1alpha1.ChangeDetectionConfig{
					Enabled:            true,
					TableName:          "users",
					TimestampColumn:    "updated_at",
					ChangePollInterval: "10 seconds",
				}
			},
			wantErrFields: []string{"spec.changeDetection.changePollInterval"},
		},
		{
			name: "change detection with a non-positive changePollInterval is rejected",
			mutate: func(r *databasev1alpha1.DatabaseQueryResource) {
				r.Spec.ChangeDetection = &databasev1alpha1.ChangeDetectionConfig{
					Enabled:            true,
					TableName:          "users",
					TimestampColumn:    "updated_at",
					ChangePollInterval: "0s",
				}
			},
			wantErrFields: []string{"spec.changeDetection.changePollInterval"},
		},
		{
			name: "changePollInterval not shorter than pollInterval produces a warning",
			mutate: func(r *databasev1alpha1.DatabaseQueryResource) {
				r.Spec.ChangeDetection = &databasev1alpha1.ChangeDetectionConfig{
					Enabled:            true,
					TableName:          "users",
					TimestampColumn:    "updated_at",
					ChangePollInterval: "2m",
				}
			},
			wantWarnings: []string{"changePollInterval"},
		},
		{
			name: "every violation is reported at once",
			mutate: func(r *databasev1alpha1.DatabaseQueryResource) {
				r.Spec.PollInterval = "nope"
				r.Spec.Query = ""
				r.Spec.Template = "{{ .Row.name"
				r.Spec.Database.ConnectionSecretRef.Name = ""
			},
			wantErrFields: []string{
				"spec.pollInterval",
				"spec.query",
				"spec.template",
				"spec.database.connectionSecretRef.name",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dbqr := validResource()
			tt.mutate(dbqr)

			warnings, err := ValidateDatabaseQueryResource(dbqr)

			if len(tt.wantErrFields) == 0 {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
				assert.True(t, apierrors.IsInvalid(err), "expected an Invalid error, got %v", err)
				for _, field := range tt.wantErrFields {
					assert.Contains(t, err.Error(), field)
				}
			}
			for _, want := range tt.wantWarnings {
				assert.Contains(t, strings.Join(warnings, "\n"), want)
			}
			if len(tt.wantWarnings) == 0 {
				assert.Empty(t, warnings)
			}
		})
	}
}

func TestValidatorMethods(t *testing.T) {
	validator := &DatabaseQueryResourceCustomValidator{}
	ctx := context.Background()

	t.Run("ValidateCreate accepts a valid resource", func(t *testing.T) {
		warnings, err := validator.ValidateCreate(ctx, validResource())
		require.NoError(t, err)
		assert.Empty(t, warnings)
	})

	t.Run("ValidateCreate rejects an invalid resource", func(t *testing.T) {
		dbqr := validResource()
		dbqr.Spec.Query = ""
		_, err := validator.ValidateCreate(ctx, dbqr)
		require.Error(t, err)
		assert.True(t, apierrors.IsInvalid(err))
	})

	t.Run("ValidateUpdate rejects an invalid update", func(t *testing.T) {
		oldObj := validResource()
		newObj := validResource()
		newObj.Spec.Template = "{{ .Row.username"
		_, err := validator.ValidateUpdate(ctx, oldObj, newObj)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "spec.template")
	})

	t.Run("ValidateUpdate accepts a valid update", func(t *testing.T) {
		oldObj := validResource()
		newObj := validResource()
		newObj.Spec.PollInterval = "5m"
		_, err := validator.ValidateUpdate(ctx, oldObj, newObj)
		require.NoError(t, err)
	})

	t.Run("ValidateDelete accepts every deletion", func(t *testing.T) {
		_, err := validator.ValidateDelete(ctx, validResource())
		require.NoError(t, err)
	})
}
