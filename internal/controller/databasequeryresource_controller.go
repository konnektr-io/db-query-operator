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
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	databasev1alpha1 "github.com/konnektr-io/db-query-operator/api/v1alpha1"
	"github.com/konnektr-io/db-query-operator/internal/util"
)

const (
	ManagedByLabel       = "konnektr.io/managed-by" // Label to identify managed resources
	ControllerName       = "databasequeryresource-controller"
	ConditionReconciled  = "Reconciled"
	ConditionDBConnected = "DBConnected"
)

// DatabaseQueryResourceReconciler reconciles a DatabaseQueryResource object
type DatabaseQueryResourceReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	Log    logr.Logger // Add logger field
}

//+kubebuilder:rbac:groups=konnektr.io,resources=databasequeryresources,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=konnektr.io,resources=databasequeryresources/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=konnektr.io,resources=databasequeryresources/finalizers,verbs=update
//+kubebuilder:rbac:groups=core,resources=secrets,verbs=get;list;watch
//+kubebuilder:rbac:groups="*",resources="*",verbs=get;list;watch;create;update;patch;delete // WARNING: Broad permissions. Scope down if possible.

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
func (r *DatabaseQueryResourceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx) // Use context-aware logger
	r.Log = log                 // Store logger for helper methods

	log.Info("Reconciling DatabaseQueryResource", "Request.Namespace", req.Namespace, "Request.Name", req.Name)

	// 1. Fetch the DatabaseQueryResource instance
	dbqr := &databasev1alpha1.DatabaseQueryResource{}
	if err := r.Get(ctx, req.NamespacedName, dbqr); err != nil {
		if apierrors.IsNotFound(err) {
			log.Info("DatabaseQueryResource not found. Ignoring since object must be deleted.")
			return ctrl.Result{}, nil
		}
		log.Error(err, "Failed to get DatabaseQueryResource")
		// Don't update status if we couldn't fetch the object
		return ctrl.Result{}, err
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
	var dbClient util.DatabaseClient
	switch strings.ToLower(dbqr.Spec.Database.Type) {
	case "postgres", "postgresql", "pgx", "":
		dbClient = &util.PostgresDatabaseClient{}
	// case "mysql":
	// 	dbClient = &util.MySQLDatabaseClient{}
	default:
		log.Error(fmt.Errorf("unsupported database type: %s", dbqr.Spec.Database.Type), "Unsupported database type")
		setCondition(dbqr, ConditionDBConnected, metav1.ConditionFalse, "UnsupportedDB", "Unsupported database type")
		setCondition(dbqr, ConditionReconciled, metav1.ConditionFalse, "DBConnectionFailed", "Unsupported database type")
		return ctrl.Result{}, nil
	}
	if err := dbClient.Connect(ctx, dbConfig); err != nil {
		log.Error(err, "Failed to connect to database", "host", dbConfig["host"], "db", dbConfig["dbname"])
		setCondition(dbqr, ConditionDBConnected, metav1.ConditionFalse, "ConnectionError", err.Error())
		setCondition(dbqr, ConditionReconciled, metav1.ConditionFalse, "DBConnectionFailed", "Failed to connect to DB")
		return ctrl.Result{RequeueAfter: pollInterval}, nil
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
	tmpl, err := template.New("resourceTemplate").Parse(dbqr.Spec.Template)
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

	// 7. Prune Old Resources (Garbage Collection)
	var pruneErrors []string
	if dbqr.Spec.GetPrune() {
		log.Info("Pruning enabled, checking for stale resources")
		pruneErrors = r.pruneStaleResources(ctx, dbqr, managedResourceKeys)
		if len(pruneErrors) > 0 {
			log.Info("Errors occurred during pruning", "error", strings.Join(pruneErrors, "; "))
		} else {
			log.Info("Pruning completed")
		}
	} else {
		log.Info("Pruning disabled")
	}

	// 8. Update Status
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

	// After processing rows and applying resources, handle status updates
	if dbqr.Spec.StatusUpdateQueryTemplate != "" {
		tmpl, err := template.New("statusUpdateQuery").Parse(dbqr.Spec.StatusUpdateQueryTemplate)
		if err != nil {
			log.Error(err, "Failed to parse status update query template")
		} else {
			for _, rowData := range processedRows { // Assume processedRows contains data for successfully applied resources
				var queryBuffer bytes.Buffer
				err = tmpl.Execute(&queryBuffer, map[string]interface{}{
					"Row": rowData,
					"Status": map[string]interface{}{
						"State":   "Success", // or "Error" based on the outcome
						"Message": "",        // or the error message
					},
				})
				if err != nil {
					log.Error(err, "Failed to render status update query")
					continue
				}

				err = dbClient.Exec(ctx, queryBuffer.String())
				if err != nil {
					log.Error(err, "Failed to execute status update query", "query", queryBuffer.String())
				} else {
					log.Info("Successfully updated status in database", "query", queryBuffer.String())
				}
			}
		}
	}

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

// pruneStaleResources lists all resources managed by the CR and deletes those not found in the current desired state.
func (r *DatabaseQueryResourceReconciler) pruneStaleResources(ctx context.Context, dbqr *databasev1alpha1.DatabaseQueryResource, currentKeys map[string]bool) []string {
	log := r.Log.WithValues("DatabaseQueryResource", types.NamespacedName{Name: dbqr.Name, Namespace: dbqr.Namespace})
	var errors []string

	// We need to list resources across all GVKs. This is tricky and potentially inefficient.
	// A better approach might be to store the GVKs of previously created resources in the status,
	// but let's try a label-based approach first across common types.
	// WARNING: This might miss resources if they are of unusual kinds.
	// Consider adding configuration to the CRD to specify which GVKs to scan for pruning.

	log.Info("Listing potentially managed resources for pruning")

	// Define a set of common GVKs to check. This list might need adjustment.
	// Ideally, the template itself could hint at the GVK being created.
	gvksToCheck := []schema.GroupVersionKind{
		{Group: "", Version: "v1", Kind: "ConfigMap"},
		{Group: "", Version: "v1", Kind: "Secret"},
		{Group: "", Version: "v1", Kind: "Service"},
		{Group: "apps", Version: "v1", Kind: "Deployment"},
		{Group: "apps", Version: "v1", Kind: "StatefulSet"},
		{Group: "batch", Version: "v1", Kind: "Job"},
		{Group: "batch", Version: "v1", Kind: "CronJob"},
		// Add more common types or types you expect to create
	}

	// Label selector to find resources managed by this specific CR instance
	selector := labels.SelectorFromSet(labels.Set{
		ManagedByLabel: dbqr.Name,
	})

	for _, gvk := range gvksToCheck {
		list := &unstructured.UnstructuredList{}
		list.SetGroupVersionKind(gvk) // Set GVK for List operation

		err := r.List(ctx, list, client.InNamespace(dbqr.Namespace), client.MatchingLabelsSelector{Selector: selector})
		// Use client.MatchingLabels{ManagedByLabel: dbqr.Name} if selector gives issues

		if err != nil {
			// Ignore "no kind is registered" errors, just means we don't know about this GVK yet
			if meta.IsNoMatchError(err) || runtime.IsNotRegisteredError(err) {
				log.V(1).Info("Skipping GVK for pruning, not registered in scheme", "GVK", gvk)
				continue
			}
			log.Error(err, "Failed to list resources for pruning", "GVK", gvk)
			errors = append(errors, fmt.Sprintf("list %s: %v", gvk.Kind, err))
			continue
		}

		log.Info("Found candidates for pruning", "GVK", gvk, "Count", len(list.Items))

		for _, item := range list.Items {
			objKey := getObjectKey(&item)
			if _, exists := currentKeys[objKey]; !exists {
				// This resource was managed by us previously but is not in the current DB result set. Delete it.
				log.Info("Pruning stale resource", "GVK", item.GroupVersionKind(), "Namespace", item.GetNamespace(), "Name", item.GetName())
				if err := r.Delete(ctx, &item); err != nil {
					// Ignore not found errors, might have been deleted already
					if !apierrors.IsNotFound(err) {
						log.Error(err, "Failed to prune resource", "GVK", item.GroupVersionKind(), "Namespace", item.GetNamespace(), "Name", item.GetName())
						errors = append(errors, fmt.Sprintf("delete %s: %v", objKey, err))
					}
				} else {
					log.Info("Successfully pruned resource", "GVK", item.GroupVersionKind(), "Namespace", item.GetNamespace(), "Name", item.GetName())
				}
			}
		}
	}

	return errors
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

	for _, gvk := range ownedGVKs {
		u := &unstructured.Unstructured{}
		u.SetGroupVersionKind(gvk)
		controllerBuilder = controllerBuilder.Owns(u)
	}

	return controllerBuilder.Complete(r)
}
