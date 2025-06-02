package controller

import (
	"bytes"
	"context"
	"fmt"
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
	ManagedByLabel       = "konnektr.io/managed-by" // Label to identify managed resources
	ControllerName       = "databasequeryresource-controller"
	ConditionReconciled  = "Reconciled"
	ConditionDBConnected = "DBConnected"
	DatabaseQueryFinalizer = "konnektr.io/databasequeryresource-finalizer"
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

// Key for context value to indicate child resource event
var childResourceEventKey = struct{}{}

// Info about the triggering child resource
// Used to pass to context for status update
// Only minimal info needed for status update
// (GVK, namespace, name)
type childResourceInfo struct {
	GVK       schema.GroupVersionKind
	Namespace string
	Name      string
}

//+kubebuilder:rbac:groups=konnektr.io,resources=databasequeryresources,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=konnektr.io,resources=databasequeryresources/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=konnektr.io,resources=databasequeryresources/finalizers,verbs=update
//+kubebuilder:rbac:groups=core,resources=secrets,verbs=get;list;watch
//+kubebuilder:rbac:groups="*",resources="*",verbs=get;list;watch;create;update;patch;delete // WARNING: Broad permissions. Scope down if possible.

// main kubernetes reconciliation loop
// handles both polling interval and child resource updates
func (r *DatabaseQueryResourceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)
	r.Log = log
	log.Info("Reconciling DatabaseQueryResource", "Request.Namespace", req.Namespace, "Request.Name", req.Name)

	// 1. Fetch the DatabaseQueryResource instance
	dbqr := &databasev1alpha1.DatabaseQueryResource{}
	if err := r.Get(ctx, req.NamespacedName, dbqr); err != nil {
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

	// Defer status update
	defer func() {
		dbqr.Status.ObservedGeneration = dbqr.Generation
		if err := r.Status().Update(ctx, dbqr); err != nil {
			log.Error(err, "Failed to update DatabaseQueryResource status")
		}
	}()

	// 2. Parse Poll Interval
	pollInterval, err := time.ParseDuration(dbqr.Spec.PollInterval)
	if err != nil {
		log.Error(err, "Invalid pollInterval format")
		setCondition(dbqr, ConditionReconciled, metav1.ConditionFalse, "InvalidSpec", fmt.Sprintf("Invalid pollInterval: %v", err))
		return ctrl.Result{}, nil // Don't requeue invalid spec
	}

	// 3. Get Database Connection Details
	dbConfig, err := r.getDBConfig(ctx, dbqr)
	if err != nil {
		log.Error(err, "Failed to get database configuration")
		setCondition(dbqr, ConditionDBConnected, metav1.ConditionFalse, "SecretError", err.Error())
		setCondition(dbqr, ConditionReconciled, metav1.ConditionFalse, "DBConnectionFailed", "Failed to get DB configuration")
		// Requeue faster if secret might be missing/fixed
		return ctrl.Result{RequeueAfter: 1 * time.Minute}, nil
	}

	// 4. Select and connect to the appropriate database client
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

	// 5. Execute Query
	results, columnNames, err := dbClient.Query(ctx, dbqr.Spec.Query)
	if err != nil {
		log.Error(err, "Failed to execute database query", "query", dbqr.Spec.Query)
		setCondition(dbqr, ConditionReconciled, metav1.ConditionFalse, "QueryFailed", fmt.Sprintf("Failed to execute query: %v", err))
		return ctrl.Result{RequeueAfter: pollInterval}, nil // Requeue after interval
	}
	log.Info("Query executed successfully", "columns", columnNames)

	// 6. Process Rows and Manage Resources
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

		// Set Namespace if not specified in template, default to CR's namespace
		if obj.GetNamespace() == "" {
			obj.SetNamespace(dbqr.Namespace)
		}
		labels := obj.GetLabels()
		if labels == nil {
			labels = make(map[string]string)
		}
		labels[ManagedByLabel] = dbqr.Name
		obj.SetLabels(labels)
		if err := controllerutil.SetControllerReference(dbqr, obj, r.Scheme); err != nil {
			log.Error(err, "Failed to set owner reference on object", "object GVK", obj.GroupVersionKind(), "object Name", obj.GetName())
			rowProcessingErrors = append(rowProcessingErrors, fmt.Sprintf("owner ref error for %s/%s: %v", obj.GetNamespace(), obj.GetName(), err))
			continue // Skip this resource
		}
		log.Info("Applying resource", "GVK", obj.GroupVersionKind(), "Namespace", obj.GetNamespace(), "Name", obj.GetName())
		patchMethod := client.Apply
		err = r.Patch(ctx, obj, patchMethod, client.FieldOwner(ControllerName), client.ForceOwnership)
		if err != nil {
			log.Error(err, "Failed to apply (create/update) resource", "GVK", obj.GroupVersionKind(), "Namespace", obj.GetNamespace(), "Name", obj.GetName())
			rowProcessingErrors = append(rowProcessingErrors, fmt.Sprintf("apply error for %s/%s: %v", obj.GetNamespace(), obj.GetName(), err))
			continue // Skip this resource
		}
		resourceKey := getObjectKey(obj)
		managedResourceKeys[resourceKey] = true
		log.Info("Successfully applied resource", "key", resourceKey)
		processedRows = append(processedRows, rowData)
	}

	// 7. Collect all child resources, then prune if enabled
	var pruneErrors []string
	allChildResources, err := r.collectAllChildResources(ctx, dbqr, r.OwnedGVKs)
	if err != nil {
		log.Error(err, "Failed to collect child resources")
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

	// 8. Check for child resource state changes and update status if needed
	r.updateStatusForChildResources(ctx, dbqr, allChildResources, dbConfig)

	// 9. Update Status
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
	setCondition(dbqr, ConditionReconciled, metav1.ConditionTrue, "Success", "Successfully queried DB and reconciled resources")

	return ctrl.Result{RequeueAfter: pollInterval}, nil
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
	for _, item := range allChildren {
		objKey := getObjectKey(item)
		if _, exists := currentKeys[objKey]; !exists {
			log.Info("Pruning stale resource", "GVK", item.GroupVersionKind(), "Namespace", item.GetNamespace(), "Name", item.GetName())
			if err := r.Delete(ctx, item); err != nil {
				if !apierrors.IsNotFound(err) {
					log.Error(err, "Failed to prune resource", "GVK", item.GroupVersionKind(), "Namespace", item.GetNamespace(), "Name", item.GetName())
					errors = append(errors, fmt.Sprintf("delete %s: %v", objKey, err))
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
		err := r.List(ctx, list, client.InNamespace(dbqr.Namespace), client.MatchingLabelsSelector{Selector: selector})
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
		// Only process Deployments for this example, but could generalize
		if obj.GetKind() == "Deployment" && obj.GroupVersionKind().Group == "apps" {
			dbClient, err := r.getOrCreateDBClient(ctx, dbqr, dbConfig)
			if err != nil {
				log.Error(err, "Failed to get database client for status update")
				continue
			}
			defer dbClient.Close(ctx)
			tmpl, err := template.New("statusUpdateQuery").Funcs(sprig.TxtFuncMap()).Parse(dbqr.Spec.StatusUpdateQueryTemplate)
			if err != nil {
				log.Error(err, "Failed to parse status update query template (child event)")
				continue
			}
			var queryBuffer bytes.Buffer
			err = tmpl.Execute(&queryBuffer, map[string]interface{}{
				"Resource": obj.Object,
			})
			if err != nil {
				log.Error(err, "Failed to render status update query (child event)")
				continue
			}
			err = dbClient.Exec(ctx, queryBuffer.String())
			if err != nil {
				log.Error(err, "Failed to execute status update query (child event)", "query", queryBuffer.String())
			} else {
				log.Info("Successfully updated status in database (child event)", "query", queryBuffer.String())
				setCondition(dbqr, ConditionReconciled, metav1.ConditionTrue, "ChildResourceChanged", "Status updated due to child resource event")
			}
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

// createOrUpdateResource implements the CreateOrUpdate logic.
// Deprecated in favor of Server-Side Apply (Patch), but kept as a fallback example.
func (r *DatabaseQueryResourceReconciler) createOrUpdateResource(ctx context.Context, obj *unstructured.Unstructured) error {
	log := r.Log.WithValues("object", getObjectKey(obj))
	existing := &unstructured.Unstructured{}
	existing.SetGroupVersionKind(obj.GroupVersionKind())

	err := r.Get(ctx, client.ObjectKeyFromObject(obj), existing)
	if err != nil {
		if apierrors.IsNotFound(err) {
			log.Info("Creating new resource")
			if createErr := r.Create(ctx, obj); createErr != nil {
				log.Error(createErr, "Failed to create resource")
				return createErr
			}
			log.Info("Resource created successfully")
			return nil
		}
		log.Error(err, "Failed to get existing resource")
		return err
	}

	// Resource exists, check if update is needed
	// Simple comparison: If resource versions differ, or some key fields differ.
	// A more robust diff would be needed for perfect updates, Server-Side Apply handles this better.
	// We need to preserve fields set by other controllers or users.
	// Copying required fields and metadata.
	// Warning: This simple update might overwrite changes made by others. SSA is preferred.

	// Preserve resource version for update
	obj.SetResourceVersion(existing.GetResourceVersion())
	// Preserve ClusterIP if it's a Service and already set
	if existing.GetKind() == "Service" {
		if clusterIP, found, _ := unstructured.NestedString(existing.Object, "spec", "clusterIP"); found && clusterIP != "" && clusterIP != "None" {
			if _, objHasIP, _ := unstructured.NestedString(obj.Object, "spec", "clusterIP"); !objHasIP || unstructured.SetNestedField(obj.Object, clusterIP, "spec", "clusterIP") != nil {
				log.Info("Preserving existing ClusterIP", "ClusterIP", clusterIP)
				unstructured.SetNestedField(obj.Object, clusterIP, "spec", "clusterIP")
			}
		}
	}

	// Check if an update is actually needed (very basic check)
	// This is where Server-Side Apply shines as it handles this comparison server-side.
	// For CreateOrUpdate, a deep comparison library or manual checks are often needed.
	if reflect.DeepEqual(obj.Object["spec"], existing.Object["spec"]) &&
		reflect.DeepEqual(obj.GetLabels(), existing.GetLabels()) &&
		reflect.DeepEqual(obj.GetAnnotations(), existing.GetAnnotations()) {
		log.Info("Resource is already up-to-date")
		return nil
	}

	log.Info("Updating existing resource")
	if updateErr := r.Update(ctx, obj); updateErr != nil {
		log.Error(updateErr, "Failed to update resource")
		return updateErr
	}
	log.Info("Resource updated successfully")
	return nil
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
