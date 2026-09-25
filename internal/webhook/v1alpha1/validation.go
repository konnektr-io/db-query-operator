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

// Package v1alpha1 contains the admission webhooks for the konnektr.io/v1alpha1 API group.
package v1alpha1

import (
	"fmt"
	"strings"
	"text/template"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	databasev1alpha1 "github.com/konnektr-io/db-query-operator/api/v1alpha1"
	"github.com/konnektr-io/db-query-operator/internal/controller"
)

// maxErrorValueRunes caps how much of an offending value is echoed back in a
// validation error. Templates and queries can be several kilobytes long and
// repeating them in the admission response only makes the message unreadable.
const maxErrorValueRunes = 80

// DatabaseQueryResourceGroupKind is the GroupKind used in admission errors.
var DatabaseQueryResourceGroupKind = databasev1alpha1.GroupVersion.WithKind("DatabaseQueryResource").GroupKind()

// ValidateDatabaseQueryResource checks a DatabaseQueryResource against the
// invariants the reconciler relies on. It returns an Invalid error carrying one
// field cause per violation, or nil when the resource is valid.
func ValidateDatabaseQueryResource(dbqr *databasev1alpha1.DatabaseQueryResource) (admission.Warnings, error) {
	allErrs, warnings := validateSpec(&dbqr.Spec)
	if len(allErrs) == 0 {
		return warnings, nil
	}
	return warnings, apierrors.NewInvalid(DatabaseQueryResourceGroupKind, dbqr.Name, allErrs)
}

// validateSpec collects every violated invariant of the given spec, plus
// non-fatal warnings for configurations that are accepted but suspicious.
func validateSpec(spec *databasev1alpha1.DatabaseQueryResourceSpec) (field.ErrorList, admission.Warnings) {
	var allErrs field.ErrorList
	var warnings admission.Warnings

	specPath := field.NewPath("spec")

	// pollInterval must be a positive Go duration: the reconciler parses it with
	// time.ParseDuration and requeues every interval, so an unparseable or
	// non-positive value either parks the resource on a status condition or
	// turns the controller into a hot loop against the database.
	pollInterval, pollIntervalErr := validateDuration(specPath.Child("pollInterval"), "pollInterval", spec.PollInterval)
	if pollIntervalErr != nil {
		allErrs = append(allErrs, pollIntervalErr)
	}

	// The Secret reference is mandatory: without a name there is nothing to read
	// the connection details from and every reconcile fails to connect.
	if strings.TrimSpace(spec.Database.ConnectionSecretRef.Name) == "" {
		allErrs = append(allErrs, field.Required(
			specPath.Child("database", "connectionSecretRef", "name"),
			"name of the Secret holding the database connection details must not be empty",
		))
	}

	// The query has to contain something: an empty or whitespace-only statement
	// reaches the database as a protocol error on every poll.
	queryPath := specPath.Child("query")
	switch {
	case spec.Query == "":
		allErrs = append(allErrs, field.Required(queryPath, "query must not be empty"))
	case strings.TrimSpace(spec.Query) == "":
		allErrs = append(allErrs, field.Invalid(queryPath, spec.Query, "query must not consist of whitespace only"))
	}

	// Templates must parse with exactly the function set the reconciler uses,
	// otherwise a typo only surfaces as a TemplateError status condition after
	// the resource has been accepted.
	templatePath := specPath.Child("template")
	switch {
	case spec.Template == "":
		allErrs = append(allErrs, field.Required(templatePath, "template must not be empty"))
	case strings.TrimSpace(spec.Template) == "":
		allErrs = append(allErrs, field.Invalid(templatePath, spec.Template, "template must not consist of whitespace only"))
	default:
		if err := validateTemplate(templatePath, "resourceTemplate", spec.Template); err != nil {
			allErrs = append(allErrs, err)
		}
	}
	if strings.TrimSpace(spec.StatusUpdateQueryTemplate) != "" {
		if err := validateTemplate(specPath.Child("statusUpdateQueryTemplate"), "statusUpdateQuery", spec.StatusUpdateQueryTemplate); err != nil {
			allErrs = append(allErrs, err)
		}
	}

	// Change detection mirrors pollInterval: the interval is parsed at runtime
	// and a misconfiguration silently falls back to the 10s default, hiding the
	// typo from the user.
	if cd := spec.ChangeDetection; cd != nil {
		changePollInterval, changeIntervalErr := validateDuration(
			specPath.Child("changeDetection", "changePollInterval"), "changePollInterval", cd.ChangePollInterval,
		)
		if changeIntervalErr != nil {
			allErrs = append(allErrs, changeIntervalErr)
		} else if cd.Enabled && pollIntervalErr == nil && changePollInterval >= pollInterval {
			warnings = append(warnings, fmt.Sprintf(
				"spec.changeDetection.changePollInterval (%s) is not shorter than spec.pollInterval (%s): "+
					"change detection cannot make reconciliation more responsive than the full poll interval",
				cd.ChangePollInterval, spec.PollInterval,
			))
		}
	}

	return allErrs, warnings
}

// validateDuration validates a required duration field of the spec and returns
// its parsed value.
func validateDuration(path *field.Path, name, value string) (time.Duration, *field.Error) {
	if strings.TrimSpace(value) == "" {
		return 0, field.Required(path, name+" must not be empty")
	}
	d, err := time.ParseDuration(value)
	if err != nil {
		return 0, field.Invalid(path, value, fmt.Sprintf("must be a valid Go duration such as \"30s\", \"5m\" or \"1h\": %v", err))
	}
	if d <= 0 {
		return d, field.Invalid(path, value, "must be greater than zero; a non-positive interval would poll the database continuously")
	}
	return d, nil
}

// validateTemplate reports whether tmpl parses as a Go template using the same
// function map the reconciler renders templates with.
func validateTemplate(path *field.Path, tmplName, tmpl string) *field.Error {
	if _, err := template.New(tmplName).Funcs(controller.FuncMap()).Parse(tmpl); err != nil {
		return field.Invalid(path, truncate(tmpl), fmt.Sprintf("must be a valid Go template: %v", err))
	}
	return nil
}

// truncate shortens a value that is echoed back in a validation error message.
func truncate(value string) string {
	runes := []rune(value)
	if len(runes) <= maxErrorValueRunes {
		return value
	}
	return string(runes[:maxErrorValueRunes]) + "..."
}
