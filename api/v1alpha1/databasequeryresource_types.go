// +kubebuilder:object:generate=true
package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// DatabaseConnectionSecretRef defines how to connect to the database via a Secret.
type DatabaseConnectionSecretRef struct {
	// Name of the Secret.
	// +kubebuilder:validation:Required
	Name string `json:"name"`
	// Namespace of the Secret. Defaults to the namespace of the DatabaseQueryResource.
	// +optional
	Namespace string `json:"namespace,omitempty"`
	// Key within the Secret for the database host. Defaults to "host".
	// +optional
	HostKey string `json:"hostKey,omitempty"`
	// Key within the Secret for the database port. Defaults to "port".
	// +optional
	PortKey string `json:"portKey,omitempty"`
	// Key within the Secret for the database username. Defaults to "username".
	// +optional
	UserKey string `json:"userKey,omitempty"`
	// Key within the Secret for the database password. Defaults to "password".
	// +optional
	PasswordKey string `json:"passwordKey,omitempty"`
	// Key within the Secret for the database name. Defaults to "dbname".
	// +optional
	DBNameKey string `json:"dbNameKey,omitempty"`
	// Key within the Secret for the SSL mode. Defaults to "sslmode". Use 'disable' if not needed.
	// +optional
	SSLModeKey string `json:"sslModeKey,omitempty"`
}

// DatabaseSpec defines the database connection details.
type DatabaseSpec struct {
	// Type of the database. Currently only "postgres" is supported.
	// +kubebuilder:validation:Enum=postgres
	// +kubebuilder:default=postgres
	Type string `json:"type"`
	// Reference to the Secret containing connection details.
	// +kubebuilder:validation:Required
	ConnectionSecretRef DatabaseConnectionSecretRef `json:"connectionSecretRef"`
}

// DatabaseQueryResourceSpec defines the desired state of DatabaseQueryResource
// +kubebuilder:deepcopy-gen=true
type DatabaseQueryResourceSpec struct {
	// INSERT ADDITIONAL SPEC FIELDS - desired state of cluster
	// Important: Run "make" to regenerate code after modifying this file

	// PollInterval defines how often to query the database and reconcile resources.
	// Format is a duration string like "5m", "1h", "30s".
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Pattern="^([0-9]+(\\.[0-9]+)?(ns|us|µs|ms|s|m|h))+$"
	PollInterval string `json:"pollInterval"`

	// Database connection details.
	// +kubebuilder:validation:Required
	Database DatabaseSpec `json:"database"`

	// SQL query to execute against the database.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Query string `json:"query"`

	// Go template string for the Kubernetes resource to be created for each row.
	// The template will receive a map[string]interface{} named `Row` representing the database row.
	// Column names are the keys in the map.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Template string `json:"template"`

	// Prune determines if resources previously created by this CR but no longer corresponding
	// to a database row should be deleted. Defaults to true.
	// +optional
	// +kubebuilder:default=true
	Prune *bool `json:"prune,omitempty"`

	// Cypher query template for updating the status of database nodes.
	// The template will receive a map[string]interface{} named `Row` representing the database row.
	// +kubebuilder:validation:Optional
	StatusUpdateQueryTemplate string `json:"statusUpdateQueryTemplate,omitempty"`
}

// DatabaseQueryResourceStatus defines the observed state of DatabaseQueryResource
// +kubebuilder:deepcopy-gen=true
type DatabaseQueryResourceStatus struct {
	// Conditions represent the latest available observations of the resource's state.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// LastPollTime records when the database was last successfully queried.
	// +optional
	LastPollTime *metav1.Time `json:"lastPollTime,omitempty"`

	// ManagedResources lists the resources currently managed by this CR.
	// +optional
	ManagedResources []string `json:"managedResources,omitempty"`

	// ObservedGeneration reflects the generation of the CR spec that was last processed.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:printcolumn:name="Interval",type="string",JSONPath=".spec.pollInterval",description="Polling interval"
//+kubebuilder:printcolumn:name="Last Poll",type="date",JSONPath=".status.lastPollTime",description="Last successful poll time"
//+kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// DatabaseQueryResource is the Schema for the databasequeryresources API
type DatabaseQueryResource struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   DatabaseQueryResourceSpec   `json:"spec,omitempty"`
	Status DatabaseQueryResourceStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// DatabaseQueryResourceList contains a list of DatabaseQueryResource
type DatabaseQueryResourceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []DatabaseQueryResource `json:"items"`
}

func init() {
	SchemeBuilder.Register(&DatabaseQueryResource{}, &DatabaseQueryResourceList{})
}

// Helper function to get default prune value
func (s *DatabaseQueryResourceSpec) GetPrune() bool {
	if s.Prune == nil {
		return true // Default to true if not set
	}
	return *s.Prune
}
