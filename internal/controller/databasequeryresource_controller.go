package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"reflect"
	"sort"
	"strings"
	"text/template"
	"time"

	"github.com/Masterminds/sprig/v3"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/yaml" // For decoding template output
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	databasev1alpha1 "github.com/konnektr-io/db-query-operator/api/v1alpha1"
	"github.com/konnektr-io/db-query-operator/internal/util"
)

const (
	ManagedByLabel              = "konnektr.io/managed-by" // Label to identify managed resources
	ControllerName              = "databasequeryresource-controller"
	ConditionReconciled         = "Reconciled"
	ConditionDBConnected        = "DBConnected"
	DatabaseQueryFinalizer      = "konnektr.io/databasequeryresource-finalizer"
	LastAppliedConfigAnnotation = "konnektr.io/last-applied-configuration" // Annotation to store last applied config
)

// DatabaseQueryResourceReconciler reconciles a DatabaseQueryResource object
// Add DBClientFactory for testability
// DBClientFactory can be set in tests to inject a mock database client
// If nil, the default logic is used

type DatabaseQueryResourceReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	Log    logr.Logger // Add logger field

	DBClientFactory func(ctx context.Context, dbType string, dbConfig map[string]string) (util.DatabaseClient, error)
	OwnedGVKs       []schema.GroupVersionKind // Add this field to hold owned GVKs
}

//+kubebuilder:rbac:groups=konnektr.io,resources=databasequeryresources,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=konnektr.io,resources=databasequeryresources/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=konnektr.io,resources=databasequeryresources/finalizers,verbs=update
//+kubebuilder:rbac:groups=core,resources=secrets,verbs=get;list;watch
//+kubebuilder:rbac:groups="*",resources="*",verbs=get;list;watch;create;update;patch;delete

// Helper functions for managing last applied configuration

// managedResourceConfig represents the configuration we track for determining if updates are needed
type managedResourceConfig struct {
	Spec        interface{}       `json:"spec,omitempty"`
	Data        interface{}       `json:"data,omitempty"` // Include data field for ConfigMaps and similar resources
	Labels      map[string]string `json:"labels,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`
}

// getLastAppliedConfig retrieves the last applied configuration from the annotation
func getLastAppliedConfig(obj *unstructured.Unstructured) (*managedResourceConfig, error) {
	annotations := obj.GetAnnotations()
	if annotations == nil {
		return nil, nil
	}

	lastAppliedStr, exists := annotations[LastAppliedConfigAnnotation]
	if !exists {
		return nil, nil
	}

	var config managedResourceConfig
	if err := json.Unmarshal([]byte(lastAppliedStr), &config); err != nil {
		return nil, fmt.Errorf("failed to unmarshal last applied config: %w", err)
	}

	return &config, nil
}

// setLastAppliedConfig stores the current configuration in the annotation
func setLastAppliedConfig(obj *unstructured.Unstructured) error {
	config := managedResourceConfig{
		Spec:        obj.Object["spec"],
		Labels:      obj.GetLabels(),
		Annotations: obj.GetAnnotations(),
	}

	// Don't include the last applied config annotation itself in the stored config
	if config.Annotations != nil {
		configAnnotations := make(map[string]string)
		for k, v := range config.Annotations {
			if k != LastAppliedConfigAnnotation {
				configAnnotations[k] = v
			}
		}
		config.Annotations = configAnnotations
	}

	configBytes, err := json.Marshal(config)
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	annotations := obj.GetAnnotations()
	if annotations == nil {
		annotations = make(map[string]string)
	}
	annotations[LastAppliedConfigAnnotation] = string(configBytes)
	obj.SetAnnotations(annotations)

	return nil
}

// shouldUpdateResource determines if the resource needs to be updated by comparing with existing cluster state
func (r *DatabaseQueryResourceReconciler) shouldUpdateResource(ctx context.Context, obj *unstructured.Unstructured) (bool, error) {
	// Check if the resource exists in the cluster
	existing := &unstructured.Unstructured{}
	existing.SetGroupVersionKind(obj.GroupVersionKind())

	err := r.Get(ctx, client.ObjectKeyFromObject(obj), existing)
	if err != nil {
		if apierrors.IsNotFound(err) {
			// Resource doesn't exist, we need to create it
			return true, nil
		}
		return false, fmt.Errorf("failed to get existing resource: %w", err)
	}

	// Resource exists, check if our desired state differs from what was last applied
	lastApplied, err := getLastAppliedConfig(existing)
	if err != nil {
		return false, fmt.Errorf("failed to get last applied config from existing resource: %w", err)
	}

	// If no last applied config exists, we need to update (first time managing this resource)
	if lastApplied == nil {
		return true, nil
	}

	// Compare current desired state with last applied
	currentConfig := managedResourceConfig{
		Spec:        obj.Object["spec"],
		Data:        obj.Object["data"], // Include data field for proper change detection
		Labels:      obj.GetLabels(),
		Annotations: obj.GetAnnotations(),
	}

	// Don't include the last applied config annotation itself in the comparison
	if currentConfig.Annotations != nil {
		configAnnotations := make(map[string]string)
		for k, v := range currentConfig.Annotations {
			if k != LastAppliedConfigAnnotation {
				configAnnotations[k] = v
			}
		}
		currentConfig.Annotations = configAnnotations
	}

	// Return true if there's a difference
	return !reflect.DeepEqual(*lastApplied, currentConfig), nil
}

// main kubernetes reconciliation loop
// handles both polling interval and child resource updates
func (r *DatabaseQueryResourceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)
	r.Log = log
	log.Info("Reconciling DatabaseQueryResource", "Request.Namespace", req.Namespace, "Request.Name", req.Name)

	// Fetch the DatabaseQueryResource instance
	dbqr := &databasev1alpha1.DatabaseQueryResource{}
	allChildResources, err := r.collectAllChildResources(ctx, dbqr, r.OwnedGVKs)
	log.Info("Collected child resources for status update", "count", len(allChildResources))
	for _, obj := range allChildResources {
		log.Info("Collected child resource", "GVK", obj.GroupVersionKind(), "Namespace", obj.GetNamespace(), "Name", obj.GetName())
	}
	if err != nil {
		if apierrors.IsNotFound(err) {
			log.Info("DatabaseQueryResource not found. Ignoring since object must be deleted.")
			return ctrl.Result{}, nil
		}
		log.Error(err, "Failed to get DatabaseQueryResource")
		return ctrl.Result{}, err
	}

	// Handle finalizer logic
	if !dbqr.ObjectMeta.DeletionTimestamp.IsZero() {
		// Being deleted, handle cleanup if finalizer is present
		finalizers := dbqr.GetFinalizers()
		hasFinalizer := false
		for _, f := range finalizers {
			if f == DatabaseQueryFinalizer {
				hasFinalizer = true
				break
			}
		}
		if hasFinalizer {
			log.Info("DatabaseQueryResource is being deleted, cleaning up managed resources")
			// Collect all managed child resources
			allChildResources, err := r.collectAllChildResources(ctx, dbqr, r.OwnedGVKs)
			if err != nil {
				log.Error(err, "Failed to collect child resources for deletion cleanup")
				return ctrl.Result{}, err
			}
			// Delete all managed resources
			for _, obj := range allChildResources {
				log.Info("Deleting managed resource due to CR deletion", "GVK", obj.GroupVersionKind(), "Namespace", obj.GetNamespace(), "Name", obj.GetName())
				if err := r.Delete(ctx, obj); err != nil && !apierrors.IsNotFound(err) {
					log.Error(err, "Failed to delete managed resource during finalizer cleanup", "GVK", obj.GroupVersionKind(), "Namespace", obj.GetNamespace(), "Name", obj.GetName())
					// Optionally, return error to retry cleanup
					return ctrl.Result{}, err
				}
			}
			// Remove finalizer
			controllerutil.RemoveFinalizer(dbqr, DatabaseQueryFinalizer)
			if err := r.Update(ctx, dbqr); err != nil {
				log.Error(err, "Failed to remove finalizer after cleanup")
				return ctrl.Result{}, err
			}
			log.Info("Finalizer removed, cleanup complete")
			return ctrl.Result{}, nil
		}
		// If finalizer not present, nothing to do, allow deletion
		return ctrl.Result{}, nil
	}

	// Initialize status conditions if they are nil
	if dbqr.Status.Conditions == nil {
		dbqr.Status.Conditions = []metav1.Condition{}
	}

	// Defer status update with retry-on-conflict, only if status has changed
	defer func() {
		// Fetch the latest version to compare status
		latest := &databasev1alpha1.DatabaseQueryResource{}
		if err := r.Get(ctx, req.NamespacedName, latest); err != nil {
			log.Error(err, "Failed to fetch latest DatabaseQueryResource for status comparison")
			return
		}
		// Compare status (deep equality)
		desiredStatus := dbqr.Status
		currentStatus := latest.Status
		// Set ObservedGeneration for comparison
		desiredStatus.ObservedGeneration = dbqr.Generation
		if reflect.DeepEqual(currentStatus, desiredStatus) {
			// No change, skip update
			return
		}
		// Only update if status has changed
		maxRetries := 3
		for range maxRetries {
			latest.Status = desiredStatus
			err := r.Status().Update(ctx, latest)
			if err == nil {
				break
			}
			if apierrors.IsConflict(err) {
				// Refetch and retry
				if getErr := r.Get(ctx, req.NamespacedName, latest); getErr != nil {
					log.Error(getErr, "Failed to refetch DatabaseQueryResource after conflict during status update")
					break
				}
				continue
			}
			log.Error(err, "Failed to update DatabaseQueryResource status")
			break
		}
	}()

	// Log the pollInterval value before parsing
	log.Info("Parsing pollInterval", "pollInterval", dbqr.Spec.PollInterval)
	pollInterval, err := time.ParseDuration(dbqr.Spec.PollInterval)
	if err != nil {
		log.Error(err, "Invalid pollInterval format")
		setCondition(dbqr, ConditionReconciled, metav1.ConditionFalse, "InvalidSpec", fmt.Sprintf("Invalid pollInterval: %v", err))
		return ctrl.Result{}, nil // Don't requeue invalid spec
	}

	// Determine if we should reconcile based on change detection
	shouldReconcile, nextCheckInterval := r.shouldReconcile(ctx, dbqr, log, pollInterval)

	if !shouldReconcile {
		log.V(1).Info("No changes detected, skipping reconciliation", "nextCheck", nextCheckInterval)
		return ctrl.Result{RequeueAfter: nextCheckInterval}, nil
	}

	log.Info("Running full reconciliation")

	// Get Database Connection Details
	dbConfig, err := r.getDBConfig(ctx, dbqr)
	if err != nil {
		log.Error(err, "Failed to get database configuration")
		setCondition(dbqr, ConditionDBConnected, metav1.ConditionFalse, "SecretError", err.Error())
		setCondition(dbqr, ConditionReconciled, metav1.ConditionFalse, "DBConnectionFailed", "Failed to get DB configuration")
		// Requeue faster if secret might be missing/fixed
		return ctrl.Result{RequeueAfter: 1 * time.Minute}, nil
	}

	// Select and connect to the appropriate database client
	dbClient, err := r.getOrCreateDBClient(ctx, dbqr, dbConfig)
	if err != nil {
		log.Error(err, "Failed to get database client")
		setCondition(dbqr, ConditionDBConnected, metav1.ConditionFalse, "DBClientError", err.Error())
		setCondition(dbqr, ConditionReconciled, metav1.ConditionFalse, "DBConnectionFailed", "Failed to create/connect DB client")
		return ctrl.Result{}, nil
	}
	defer dbClient.Close(ctx)
	log.Info("Successfully connected to database", "host", dbConfig["host"], "db", dbConfig["dbname"])
	setCondition(dbqr, ConditionDBConnected, metav1.ConditionTrue, "Connected", "Successfully connected to the database")

	// Execute Query
	results, columnNames, err := dbClient.Query(ctx, dbqr.Spec.Query)
	if err != nil {
		log.Error(err, "Failed to execute database query", "query", dbqr.Spec.Query)
		setCondition(dbqr, ConditionReconciled, metav1.ConditionFalse, "QueryFailed", fmt.Sprintf("Failed to execute query: %v", err))
		return ctrl.Result{RequeueAfter: pollInterval}, nil // Requeue after interval
	}
	log.Info("Query executed successfully", "columns", columnNames, "numRows", len(results))

	// Process Rows and Manage Resources
	managedResourceKeys := make(map[string]bool) // Store keys (namespace/name) of resources created/updated in this cycle
	var rowProcessingErrors []string

	// Parse the template once
	tmpl, err := template.New("resourceTemplate").Funcs(sprig.TxtFuncMap()).Parse(dbqr.Spec.Template)
	if err != nil {
		log.Error(err, "Failed to parse resource template")
		setCondition(dbqr, ConditionReconciled, metav1.ConditionFalse, "TemplateError", fmt.Sprintf("Invalid template: %v", err))
		return ctrl.Result{}, nil // Invalid template, don't requeue based on interval
	}

	var processedRows []map[string]interface{} // Store successfully processed row data for status updates

	for _, rowData := range results {
		// Render the template
		var renderedManifest bytes.Buffer
		err = tmpl.Execute(&renderedManifest, map[string]interface{}{"Row": rowData})
		if err != nil {
			log.Error(err, "Failed to render template for row", "row", rowData)
			rowProcessingErrors = append(rowProcessingErrors, fmt.Sprintf("template render error for row data %v: %v", rowData, err))
			continue // Skip this row
		}

		// Decode the rendered template into an unstructured object
		decoder := yaml.NewYAMLOrJSONDecoder(bytes.NewReader(renderedManifest.Bytes()), 4096)
		obj := &unstructured.Unstructured{}
		if err := decoder.Decode(obj); err != nil {
			log.Error(err, "Failed to decode rendered template YAML/JSON", "templateOutput", renderedManifest.String())
			rowProcessingErrors = append(rowProcessingErrors, fmt.Sprintf("decode error for template output '%s': %v", renderedManifest.String(), err))
			continue // Skip this row
		}

		// --- Resource Management ---

		// Determine if the resource is namespaced
		restMapper := r.Client.RESTMapper()
		mapping, err := restMapper.RESTMapping(obj.GroupVersionKind().GroupKind(), obj.GroupVersionKind().Version)
		if err != nil {
			log.Error(err, "Failed to get RESTMapping for object", "GVK", obj.GroupVersionKind())
			rowProcessingErrors = append(rowProcessingErrors, fmt.Sprintf("RESTMapping error for %s: %v", obj.GroupVersionKind().String(), err))
			continue // Skip this resource
		}
		isNamespaced := mapping.Scope.Name() == "namespace"

		// Set Namespace if not specified in template, default to CR's namespace (only for namespaced resources)
		if isNamespaced && obj.GetNamespace() == "" {
			obj.SetNamespace(dbqr.Namespace)
		}
		labels := obj.GetLabels()
		if labels == nil {
			labels = make(map[string]string)
		}
		labels[ManagedByLabel] = dbqr.Name
		obj.SetLabels(labels)

		// Only set owner reference if resource is namespaced and in the same namespace as the parent
		if isNamespaced && obj.GetNamespace() == dbqr.Namespace {
			if err := controllerutil.SetControllerReference(dbqr, obj, r.Scheme); err != nil {
				log.Error(err, "Failed to set owner reference on object", "object GVK", obj.GroupVersionKind(), "object Name", obj.GetName())
				rowProcessingErrors = append(rowProcessingErrors, fmt.Sprintf("owner ref error for %s/%s: %v", obj.GetNamespace(), obj.GetName(), err))
				continue // Skip this resource
			}
		} else if isNamespaced {
			// Namespaced but not in same namespace, skip owner ref
			log.Info("Skipping owner reference: resource not in same namespace as parent", "object GVK", obj.GroupVersionKind(), "object Name", obj.GetName(), "object Namespace", obj.GetNamespace(), "parent Namespace", dbqr.Namespace)
		} else {
			// Cluster-scoped resource, skip owner ref
			log.Info("Skipping owner reference: resource is cluster-scoped", "object GVK", obj.GroupVersionKind(), "object Name", obj.GetName())
		}

		// Check if update is needed by comparing with existing resource
		updateNeeded, err := r.shouldUpdateResource(ctx, obj)
		if err != nil {
			log.Error(err, "Failed to check if update is needed", "GVK", obj.GroupVersionKind(), "Namespace", obj.GetNamespace(), "Name", obj.GetName())
			rowProcessingErrors = append(rowProcessingErrors, fmt.Sprintf("update check error for %s/%s: %v", obj.GetNamespace(), obj.GetName(), err))
			continue // Skip this resource
		}

		if updateNeeded {
			log.Info("Applying resource (update needed)", "GVK", obj.GroupVersionKind(), "Namespace", obj.GetNamespace(), "Name", obj.GetName())

			// Set the last applied configuration before applying
			if err := setLastAppliedConfig(obj); err != nil {
				log.Error(err, "Failed to set last applied config", "GVK", obj.GroupVersionKind(), "Namespace", obj.GetNamespace(), "Name", obj.GetName())
				rowProcessingErrors = append(rowProcessingErrors, fmt.Sprintf("last applied config error for %s/%s: %v", obj.GetNamespace(), obj.GetName(), err))
				continue // Skip this resource
			}

			patchMethod := client.Apply
			err = r.Patch(ctx, obj, patchMethod, client.FieldOwner(ControllerName), client.ForceOwnership)
			if err != nil {
				log.Error(err, "Failed to apply (create/update) resource", "GVK", obj.GroupVersionKind(), "Namespace", obj.GetNamespace(), "Name", obj.GetName())
				rowProcessingErrors = append(rowProcessingErrors, fmt.Sprintf("apply error for %s/%s: %v", obj.GetNamespace(), obj.GetName(), err))
				continue // Skip this resource
			}
			log.Info("Successfully applied resource", "GVK", obj.GroupVersionKind(), "Namespace", obj.GetNamespace(), "Name", obj.GetName())
		} else {
			log.V(1).Info("Resource is up-to-date, skipping apply", "GVK", obj.GroupVersionKind(), "Namespace", obj.GetNamespace(), "Name", obj.GetName())
		}

		resourceKey := getObjectKey(obj)
		managedResourceKeys[resourceKey] = true
		processedRows = append(processedRows, rowData)
	}

	// Collect all child resources, then prune if enabled
	var pruneErrors []string
	allChildResources, err = r.collectAllChildResources(ctx, dbqr, r.OwnedGVKs)
	if err != nil {
		log.Error(err, "Failed to collect child resources")
	}
	log.Info("Collected child resources for status update", "count", len(allChildResources))
	for _, obj := range allChildResources {
		log.Info("Collected child resource", "GVK", obj.GroupVersionKind(), "Namespace", obj.GetNamespace(), "Name", obj.GetName())
	}
	if dbqr.Spec.GetPrune() {
		log.Info("Pruning enabled, checking for stale resources")
		pruneErrors = r.pruneStaleResources(ctx, dbqr, managedResourceKeys, allChildResources)
		if len(pruneErrors) > 0 {
			log.Info("Errors occurred during pruning", "error", strings.Join(pruneErrors, "; "))
		} else {
			log.Info("Pruning completed")
		}
	} else {
		log.Info("Pruning disabled")
	}

	// Check for child resource state changes and update status if needed
	r.updateStatusForChildResources(ctx, dbqr, allChildResources, dbConfig)

	// Update Status
	finalErrors := append(rowProcessingErrors, pruneErrors...)
	managedResourcesList := make([]string, 0, len(managedResourceKeys))
	for k := range managedResourceKeys {
		managedResourcesList = append(managedResourcesList, k)
	}
	sort.Strings(managedResourcesList) // Sort for consistent status
	dbqr.Status.ManagedResources = managedResourcesList

	if len(finalErrors) > 0 {
		errMsg := strings.Join(finalErrors, "; ")
		setCondition(dbqr, ConditionReconciled, metav1.ConditionFalse, "ProcessingError", truncateError(errMsg, 1024))
		dbqr.Status.LastPollTime = nil // Clear last poll time on error? Or keep the last successful one? Let's keep it.
		log.Error(fmt.Errorf("%s", errMsg), "Reconciliation failed with errors")
		return ctrl.Result{RequeueAfter: pollInterval}, fmt.Errorf("reconciliation failed: %s", errMsg) // Requeue after interval even on error
	}

	// Success
	log.Info("Reconciliation successful", "managedResourceCount", len(managedResourceKeys))
	now := metav1.Now()
	dbqr.Status.LastPollTime = &now
	dbqr.Status.LastReconcileTime = &now
	setCondition(dbqr, ConditionReconciled, metav1.ConditionTrue, "Success", "Successfully queried DB and reconciled resources")

	return ctrl.Result{RequeueAfter: pollInterval}, nil
}

// parsePostgreSQLURI parses a PostgreSQL connection URI and returns connection parameters.
// Format: postgresql://username:password@host:port/dbname?sslmode=...
func (r *DatabaseQueryResourceReconciler) parsePostgreSQLURI(uri string) (map[string]string, error) {
	// Parse the URI using url.Parse
	parsedURL, err := url.Parse(uri)
	if err != nil {
		return nil, fmt.Errorf("invalid URI format: %w", err)
	}

	// Validate scheme
	if parsedURL.Scheme != "postgresql" && parsedURL.Scheme != "postgres" {
		return nil, fmt.Errorf("unsupported scheme '%s', expected 'postgresql' or 'postgres'", parsedURL.Scheme)
	}

	config := make(map[string]string)

	// Extract username and password
	if parsedURL.User != nil {
		config["username"] = parsedURL.User.Username()
		if password, ok := parsedURL.User.Password(); ok {
			config["password"] = password
		}
	}

	// Extract host and port
	host := parsedURL.Hostname()
	if host == "" {
		return nil, fmt.Errorf("host not found in URI")
	}
	config["host"] = host

	port := parsedURL.Port()
	if port == "" {
		port = "5432" // Default PostgreSQL port
	}
	config["port"] = port

	// Extract database name from path
	dbname := strings.TrimPrefix(parsedURL.Path, "/")
	if dbname == "" {
		return nil, fmt.Errorf("database name not found in URI")
	}
	config["dbname"] = dbname

	// Extract query parameters (e.g., sslmode)
	queryParams := parsedURL.Query()
	if sslmode := queryParams.Get("sslmode"); sslmode != "" {
		config["sslmode"] = sslmode
	} else {
		config["sslmode"] = "prefer" // Default
	}

	return config, nil
}

// getDBConfig retrieves database connection details from the referenced Secret.
func (r *DatabaseQueryResourceReconciler) getDBConfig(ctx context.Context, dbqr *databasev1alpha1.DatabaseQueryResource) (map[string]string, error) {
	secretRef := dbqr.Spec.Database.ConnectionSecretRef
	secretNamespace := secretRef.Namespace
	if secretNamespace == "" {
		secretNamespace = dbqr.Namespace // Default to CR's namespace
	}
	secretName := secretRef.Name

	secret := &corev1.Secret{}
	if err := r.Get(ctx, types.NamespacedName{Name: secretName, Namespace: secretNamespace}, secret); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("secret '%s/%s' not found", secretNamespace, secretName)
		}
		return nil, fmt.Errorf("failed to get secret '%s/%s': %w", secretNamespace, secretName, err)
	}

	// Check if URIKey is specified and use it if available
	if secretRef.URIKey != "" {
		uriBytes, ok := secret.Data[secretRef.URIKey]
		if !ok {
			return nil, fmt.Errorf("URIKey '%s' not found in secret '%s/%s'", secretRef.URIKey, secretNamespace, secretName)
		}

		uri := string(uriBytes)
		config, err := r.parsePostgreSQLURI(uri)
		if err != nil {
			return nil, fmt.Errorf("failed to parse PostgreSQL URI from key '%s': %w", secretRef.URIKey, err)
		}

		r.Log.Info("Using connection URI from secret", "secret", secretName, "uriKey", secretRef.URIKey)
		return config, nil
	}

	// Fallback to individual field parsing
	r.Log.Info("Using individual connection fields from secret", "secret", secretName)

	// Get values using defaults
	getValue := func(key, defaultValue string) (string, error) {
		if key == "" {
			key = defaultValue // Use default key name if not specified in CR
		}
		valueBytes, ok := secret.Data[key]
		if !ok {
			// Allow missing optional keys like sslmode if the default key itself wasn't found
			if key == "sslmode" && secretRef.SSLModeKey == "" { // If user didn't specify a key and default isn't there
				r.Log.Info("SSLModeKey or default 'sslmode' not found in secret, using 'prefer'", "secret", secretName)
				return "prefer", nil // Default SSL mode for pgx
			}
			return "", fmt.Errorf("key '%s' not found in secret '%s/%s'", key, secretNamespace, secretName)
		}
		return string(valueBytes), nil
	}

	config := make(map[string]string)
	var err error

	config["host"], err = getValue(secretRef.HostKey, "host")
	if err != nil {
		return nil, err
	}
	config["port"], err = getValue(secretRef.PortKey, "port")
	if err != nil {
		return nil, err
	}
	config["username"], err = getValue(secretRef.UserKey, "username")
	if err != nil {
		return nil, err
	}
	config["password"], err = getValue(secretRef.PasswordKey, "password")
	if err != nil {
		return nil, err
	}
	config["dbname"], err = getValue(secretRef.DBNameKey, "dbname")
	if err != nil {
		return nil, err
	}
	config["sslmode"], err = getValue(secretRef.SSLModeKey, "sslmode")
	if err != nil {
		return nil, err
	}

	return config, nil
}

// pruneStaleResources deletes resources in allChildren that are not in currentKeys. Returns errors for any failed deletions.
func (r *DatabaseQueryResourceReconciler) pruneStaleResources(ctx context.Context, dbqr *databasev1alpha1.DatabaseQueryResource, currentKeys map[string]bool, allChildren []*unstructured.Unstructured) []string {
	log := r.Log.WithValues("DatabaseQueryResource", types.NamespacedName{Name: dbqr.Name, Namespace: dbqr.Namespace})
	var errors []string

	// Debug logging: show what we're comparing
	currentKeysList := make([]string, 0, len(currentKeys))
	for k := range currentKeys {
		currentKeysList = append(currentKeysList, k)
	}

	for _, item := range allChildren {
		objKey := getObjectKey(item)

		if _, exists := currentKeys[objKey]; !exists {
			log.Info("Pruning stale resource", "GVK", item.GroupVersionKind(), "Namespace", item.GetNamespace(), "Name", item.GetName(), "objectKey", objKey)
			if err := r.Delete(ctx, item); err != nil {
				if !apierrors.IsNotFound(err) {
					log.Error(err, "Failed to prune resource", "GVK", item.GroupVersionKind(), "Namespace", item.GetNamespace(), "Name", item.GetName())
					errors = append(errors, fmt.Sprintf("delete %s: %v", objKey, err))
				} else {
					log.Info("Resource already deleted (NotFound)", "GVK", item.GroupVersionKind(), "Namespace", item.GetNamespace(), "Name", item.GetName())
				}
			} else {
				log.Info("Successfully pruned resource", "GVK", item.GroupVersionKind(), "Namespace", item.GetNamespace(), "Name", item.GetName())
			}
		}
	}
	return errors
}

// collectAllChildResources lists all resources managed by the CR and returns them, but does not delete anything.
func (r *DatabaseQueryResourceReconciler) collectAllChildResources(ctx context.Context, dbqr *databasev1alpha1.DatabaseQueryResource, ownedGVKs []schema.GroupVersionKind) ([]*unstructured.Unstructured, error) {
	log := r.Log.WithValues("DatabaseQueryResource", types.NamespacedName{Name: dbqr.Name, Namespace: dbqr.Namespace})
	var allChildren []*unstructured.Unstructured
	selector := labels.SelectorFromSet(labels.Set{
		ManagedByLabel: dbqr.Name,
	})
	for _, gvk := range ownedGVKs {
		list := &unstructured.UnstructuredList{}
		list.SetGroupVersionKind(gvk)

		// Check if this resource type is cluster-scoped
		mapping, err := r.RESTMapper().RESTMapping(gvk.GroupKind(), gvk.Version)
		if err != nil {
			log.Error(err, "Failed to get REST mapping for collection", "GVK", gvk)
			continue
		}

		// Use appropriate list options based on scope
		var listOptions []client.ListOption
		listOptions = append(listOptions, client.MatchingLabelsSelector{Selector: selector})

		if mapping.Scope.Name() == meta.RESTScopeNameNamespace {
			// Namespaced resource - limit to DBQR namespace
			listOptions = append(listOptions, client.InNamespace(dbqr.Namespace))
		}
		// For cluster-scoped resources, don't add namespace limitation

		err = r.List(ctx, list, listOptions...)
		if err != nil {
			if meta.IsNoMatchError(err) || runtime.IsNotRegisteredError(err) {
				log.V(1).Info("Skipping GVK for collection, not registered in scheme", "GVK", gvk)
				continue
			}
			log.Error(err, "Failed to list resources for collection", "GVK", gvk)
			return nil, err
		}
		log.Info("Found candidates for collection", "GVK", gvk, "Count", len(list.Items))
		for i := range list.Items {
			item := &list.Items[i]
			allChildren = append(allChildren, item)
		}
	}
	return allChildren, nil
}

// updateStatusForChildResources checks all child resources and updates the parent status if any child has changed state.
func (r *DatabaseQueryResourceReconciler) updateStatusForChildResources(ctx context.Context, dbqr *databasev1alpha1.DatabaseQueryResource, children []*unstructured.Unstructured, dbConfig map[string]string) {
	log := r.Log.WithValues("DatabaseQueryResource", types.NamespacedName{Name: dbqr.Name, Namespace: dbqr.Namespace})
	if dbqr.Spec.StatusUpdateQueryTemplate == "" {
		return
	}
	for _, obj := range children {
		log.Info("Attempting status update for child resource", "GVK", obj.GroupVersionKind(), "Namespace", obj.GetNamespace(), "Name", obj.GetName())
		// Process all resource types, not just Deployments
		dbClient, err := r.getOrCreateDBClient(ctx, dbqr, dbConfig)
		if err != nil {
			log.Error(err, "Failed to get database client for status update", "GVK", obj.GroupVersionKind(), "Name", obj.GetName())
			continue
		}
		defer dbClient.Close(ctx)
		tmpl, err := template.New("statusUpdateQuery").Funcs(sprig.TxtFuncMap()).Parse(dbqr.Spec.StatusUpdateQueryTemplate)
		if err != nil {
			log.Error(err, "Failed to parse status update query template (child event)", "GVK", obj.GroupVersionKind(), "Name", obj.GetName())
			continue
		}
		var queryBuffer bytes.Buffer
		// Provide the entire resource object as context, including metadata, spec, and status
		err = tmpl.Execute(&queryBuffer, map[string]interface{}{
			"Resource": obj.Object,
		})
		if err != nil {
			log.Error(err, "Failed to render status update query (child event)", "GVK", obj.GroupVersionKind(), "Name", obj.GetName())
			continue
		}
		log.Info("Rendered status update query", "GVK", obj.GroupVersionKind(), "Name", obj.GetName(), "query", queryBuffer.String())
		err = dbClient.Exec(ctx, queryBuffer.String())
		if err != nil {
			log.Error(err, "Failed to execute status update query (child event)", "GVK", obj.GroupVersionKind(), "Name", obj.GetName(), "query", queryBuffer.String())
		} else {
			log.Info("Successfully updated status in database (child event)", "GVK", obj.GroupVersionKind(), "Name", obj.GetName(), "query", queryBuffer.String())
			setCondition(dbqr, ConditionReconciled, metav1.ConditionTrue, "ChildResourceChanged", "Status updated due to child resource event")
		}
	}
}

// getObjectKey creates a unique string identifier for a Kubernetes object.
func getObjectKey(obj client.Object) string {
	gvk := obj.GetObjectKind().GroupVersionKind()
	return fmt.Sprintf("%s/%s/%s/%s", gvk.Group, gvk.Version, obj.GetNamespace(), obj.GetName())
}

// setCondition updates the status condition for the CR.
func setCondition(dbqr *databasev1alpha1.DatabaseQueryResource, typeString string, status metav1.ConditionStatus, reason, message string) {
	condition := metav1.Condition{
		Type:               typeString,
		Status:             status,
		ObservedGeneration: dbqr.Generation,
		LastTransitionTime: metav1.Now(),
		Reason:             reason,
		Message:            message,
	}
	meta.SetStatusCondition(&dbqr.Status.Conditions, condition)
}

// truncateError ensures error messages fit within Kubernetes status field limits.
func truncateError(msg string, maxLen int) string {
	if len(msg) > maxLen {
		return msg[:maxLen-3] + "..."
	}
	return msg
}

// SetupWithManagerAndGVKs sets up the controller with the Manager and watches the specified GVKs as owned resources.
func (r *DatabaseQueryResourceReconciler) SetupWithManagerAndGVKs(mgr ctrl.Manager, ownedGVKs []schema.GroupVersionKind) error {
	controllerBuilder := ctrl.NewControllerManagedBy(mgr).
		For(&databasev1alpha1.DatabaseQueryResource{})

	// Custom event handler for owned resources
	for _, gvk := range ownedGVKs {
		u := &unstructured.Unstructured{}
		u.SetGroupVersionKind(gvk)
		controllerBuilder = controllerBuilder.Owns(u, builder.WithPredicates(
			statusChangePredicate(),
			predicate.ResourceVersionChangedPredicate{},
			predicate.GenerationChangedPredicate{},
			predicate.AnnotationChangedPredicate{},
			predicate.LabelChangedPredicate{},
		))
	}

	return controllerBuilder.Complete(r)
}

func statusChangePredicate() predicate.Predicate {
	return predicate.Funcs{
		UpdateFunc: func(e event.UpdateEvent) bool {
			// Always trigger on update (including status changes)
			return true
		},
		CreateFunc:  func(e event.CreateEvent) bool { return true },
		DeleteFunc:  func(e event.DeleteEvent) bool { return false },
		GenericFunc: func(e event.GenericEvent) bool { return false },
	}
}

// getOrCreateDBClient returns a connected DatabaseClient using the factory or default logic
func (r *DatabaseQueryResourceReconciler) getOrCreateDBClient(ctx context.Context, dbqr *databasev1alpha1.DatabaseQueryResource, dbConfig map[string]string) (util.DatabaseClient, error) {
	if r.DBClientFactory != nil {
		return r.DBClientFactory(ctx, dbqr.Spec.Database.Type, dbConfig)
	}
	switch strings.ToLower(dbqr.Spec.Database.Type) {
	case "postgres", "postgresql", "pgx", "":
		dbClient := &util.PostgresDatabaseClient{}
		if err := dbClient.Connect(ctx, dbConfig); err != nil {
			return nil, err
		}
		return dbClient, nil
	// case "mysql":
	// 	dbClient := &util.MySQLDatabaseClient{}
	// 	if err := dbClient.Connect(ctx, dbConfig); err != nil {
	// 		return nil, err
	// 	}
	// 	return dbClient, nil
	default:
		return nil, fmt.Errorf("unsupported database type: %s", dbqr.Spec.Database.Type)
	}
}

// shouldReconcile determines if a full reconciliation should run
// Returns: (shouldReconcile bool, nextCheckInterval time.Duration)
func (r *DatabaseQueryResourceReconciler) shouldReconcile(
	ctx context.Context,
	dbqr *databasev1alpha1.DatabaseQueryResource,
	log logr.Logger,
	pollInterval time.Duration,
) (bool, time.Duration) {

	// If change detection is not enabled, always reconcile at pollInterval
	if dbqr.Spec.ChangeDetection == nil || !dbqr.Spec.ChangeDetection.Enabled {
		return true, pollInterval
	}

	// Parse change poll interval
	changePollInterval := 10 * time.Second // default
	if dbqr.Spec.ChangeDetection.ChangePollInterval != "" {
		var err error
		changePollInterval, err = time.ParseDuration(dbqr.Spec.ChangeDetection.ChangePollInterval)
		if err != nil {
			log.Error(err, "Invalid changePollInterval, using default 10s")
			changePollInterval = 10 * time.Second
		}
	}

	now := time.Now()

	// Force full reconciliation if we haven't reconciled within pollInterval
	if dbqr.Status.LastReconcileTime != nil {
		timeSinceLastReconcile := now.Sub(dbqr.Status.LastReconcileTime.Time)
		if timeSinceLastReconcile >= pollInterval {
			log.V(1).Info("Full reconciliation interval reached",
				"timeSinceLastReconcile", timeSinceLastReconcile,
				"pollInterval", pollInterval)
			return true, pollInterval
		}
	} else {
		// First run - always reconcile
		log.Info("First reconciliation run")
		return true, pollInterval
	}

	// Check if enough time has passed since last change check
	if dbqr.Status.LastChangeCheckTime != nil {
		timeSinceLastCheck := now.Sub(dbqr.Status.LastChangeCheckTime.Time)
		if timeSinceLastCheck < changePollInterval {
			// Not time to check yet
			return false, changePollInterval - timeSinceLastCheck
		}
	}

	// Perform change detection query
	hasChanges, err := r.detectChanges(ctx, dbqr, log)
	if err != nil {
		log.Error(err, "Error detecting changes, forcing reconciliation")
		return true, pollInterval
	}

	// Update last change check time
	nowMeta := metav1.Now()
	dbqr.Status.LastChangeCheckTime = &nowMeta
	if err := r.Status().Update(ctx, dbqr); err != nil {
		log.Error(err, "Failed to update LastChangeCheckTime")
	}

	if hasChanges {
		log.Info("Changes detected in database")
		return true, pollInterval
	}

	// No changes, check again after changePollInterval
	return false, changePollInterval
}

// detectChanges runs the change detection query
func (r *DatabaseQueryResourceReconciler) detectChanges(
	ctx context.Context,
	dbqr *databasev1alpha1.DatabaseQueryResource,
	log logr.Logger,
) (bool, error) {

	// Get database configuration
	dbConfig, err := r.getDBConfig(ctx, dbqr)
	if err != nil {
		return false, fmt.Errorf("failed to get database configuration: %w", err)
	}

	// Get database connection
	db, err := r.getOrCreateDBClient(ctx, dbqr, dbConfig)
	if err != nil {
		return false, fmt.Errorf("failed to connect to database: %w", err)
	}
	defer db.Close(ctx)

	// Use last check time, or use a time far in the past for first check
	var lastCheckTime time.Time
	if dbqr.Status.LastChangeCheckTime != nil {
		lastCheckTime = dbqr.Status.LastChangeCheckTime.Time
	} else {
		// First check - use a time that will catch all existing records
		lastCheckTime = time.Unix(0, 0)
	}

	// Execute query - we need to use raw query with parameters
	// Since the DatabaseClient interface uses Query(ctx, query string), we need to format it
	// For PostgreSQL, we'll format the timestamp parameter directly
	formattedQuery := fmt.Sprintf(
		"SELECT 1 FROM %s WHERE %s > '%s' LIMIT 1",
		dbqr.Spec.ChangeDetection.TableName,
		dbqr.Spec.ChangeDetection.TimestampColumn,
		lastCheckTime.Format(time.RFC3339Nano),
	)

	log.V(1).Info("Running change detection query",
		"query", formattedQuery,
		"lastCheckTime", lastCheckTime)

	rows, _, err := db.Query(ctx, formattedQuery)
	if err != nil {
		return false, fmt.Errorf("change detection query failed: %w", err)
	}

	// If we get any row, changes were detected
	hasChanges := len(rows) > 0

	return hasChanges, nil
}
