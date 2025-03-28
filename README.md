# KtrlDBQuery Database Query Operator

[![License: Apache 2.0](https://img.shields.io/badge/License-Apache%202.0-blue.svg)](https://opensource.org/licenses/Apache-2.0)
<!-- Add other badges if applicable: build status, code coverage, etc. -->

## Overview

KtrlDBQuery is a Kubernetes operator designed to manage Kubernetes resources based on the results of a database query. It periodically polls a specified database (currently PostgreSQL), executes a user-defined SQL query, and uses a Go template to render Kubernetes manifests for each row returned by the query.

The operator handles the reconciliation loop, ensuring that the resources in the cluster match the desired state defined by the database query results and the template. This allows for dynamic configuration and resource management driven directly by your application's database state.

## Features

* **CRD Driven:** Configuration is managed via a `DatabaseQueryResource` Custom Resource Definition.
* **Database Polling:** Periodically queries a database at a configurable interval.
* **PostgreSQL Support:** Currently supports PostgreSQL databases.
* **Custom Queries:** Execute any read-only SQL query.
* **Go Templating:** Define Kubernetes resource manifests using Go templates, allowing data from query results to be injected.
* **Row-to-Resource Mapping:** Each row in the query result typically generates one Kubernetes resource.
* **Secret Management:** Securely fetches database credentials from Kubernetes Secrets.
* **Reconciliation:** Creates, updates, and (optionally) deletes Kubernetes resources to match the query results.
* **Pruning:** Automatically cleans up resources previously created by the operator if they no longer correspond to a row in the database query result (configurable).
* **Ownership:** Sets Owner References on created resources (optional, but recommended) for automatic garbage collection by Kubernetes when the `DatabaseQueryResource` is deleted.
* **Labeling:** Labels created resources for easy identification and potential pruning.

## Prerequisites

* **Go:** Version 1.19+ (for building/development)
* **Docker:** For building the container image.
* **kubectl:** For interacting with the Kubernetes cluster.
* **Kubernetes Cluster:** Access to a Kubernetes cluster (e.g., kind, Minikube, EKS, GKE, AKS).
* **PostgreSQL Database:** A running PostgreSQL instance accessible from the Kubernetes cluster.
* **controller-gen:** Kubernetes code generator tool (`go install sigs.k8s.io/controller-tools/cmd/controller-gen@latest`). Required for development.
* **kustomize:** (Usually included with `kubectl` v1.14+) Used for building and applying manifests.

## Getting Started

### 1. Clone the Repository

```bash
git clone <your-repository-url>
cd <repository-directory> # e.g., cd db-query-operator
```

### 2. Prepare the Database

Ensure your PostgreSQL database is running and accessible from your cluster. Create the necessary table(s) and data that your query will target.

Example table schema used in the sample:

```sql
CREATE TABLE users (
    user_id SERIAL PRIMARY KEY,
    username VARCHAR(50) UNIQUE NOT NULL,
    email VARCHAR(100),
    status VARCHAR(20) DEFAULT 'inactive',
    created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
);

-- Insert some sample data
INSERT INTO users (username, email, status) VALUES
  ('alice', 'alice@example.com', 'active'),
  ('bob', 'bob@example.com', 'active');
```

### 3. Create Database Credentials Secret

Create a Kubernetes Secret containing the connection details for your PostgreSQL database. The operator will read credentials from this Secret.

**Example `db-credentials.yaml`:**

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: db-credentials
  # IMPORTANT: Deploy this secret in the same namespace as your DatabaseQueryResource CR,
  # or specify the secret's namespace in the CR spec.
  namespace: default
type: Opaque
stringData:
  # Default keys (can be overridden in the CR spec)
  host: "your-postgres-host-or-service-name" # e.g., postgresql.database.svc.cluster.local
  port: "5432"
  username: "your_db_user"
  password: "your_db_password"
  dbname: "your_db_name"
  sslmode: "disable" # Or "require", "verify-full", etc.
```

Apply the secret:

```bash
kubectl apply -f db-credentials.yaml
```

### 4. Build and Push the Docker Image

Build the operator's container image and push it to a container registry accessible by your Kubernetes cluster.

```bash
# Replace 'your-dockerhub-username' with your actual registry/username
export IMG=your-dockerhub-username/ktrl-db-query:latest
docker build -t ${IMG} .
docker push ${IMG}
```

### 5. Update Deployment Manifest

Ensure the operator Deployment manifest (`config/manager/manager.yaml`) points to the image you just pushed. If using `kustomize`, update `config/manager/kustomization.yaml`.

**Example (editing `config/manager/manager.yaml` directly):**
Find the `image:` line under `spec.template.spec.containers` and update it:

```yaml
containers:
- name: manager
  image: your-dockerhub-username/ktrl-db-query:latest # <-- UPDATE THIS
  # ... rest of the container spec
```

### 6. Deploy the Operator

Deploy the CRD, RBAC rules (ClusterRole, ServiceAccount, ClusterRoleBinding), and the Deployment itself. It's recommended to deploy the operator into its own namespace (e.g., `ktrl-db-query-system`).

```bash
# Create the namespace (optional, but recommended)
kubectl create namespace ktrl-db-query-system

# Apply the CRD
kubectl apply -f config/crd/bases/database.example.com_databasequeryresources.yaml

# Apply RBAC components (adjust namespace if you chose a different one)
kubectl apply -f config/rbac/service_account.yaml -n ktrl-db-query-system
kubectl apply -f config/rbac/role.yaml
kubectl apply -f config/rbac/role_binding.yaml

# Apply the Deployment (adjust namespace if you chose a different one)
kubectl apply -f config/manager/manager.yaml -n ktrl-db-query-system

# --- OR using Kustomize (if config/default/kustomization.yaml is set up) ---
# Note: You might need to edit config/default/kustomization.yaml to set the namespace
# kustomize build config/default | kubectl apply -f -
```

**Note:** The default RBAC rules grant broad permissions (`*/*` for create/update/patch/delete) to allow managing any resource type defined in the template. For production, **it is highly recommended to scope these permissions down** in `config/rbac/role.yaml` to only the specific resource types (Group, Version, Kind) that your templates will generate. Remember to regenerate and reapply the RBAC manifests if you change the `//+kubebuilder:rbac` markers in the controller.

### 7. Verify the Operator Pod

Check that the operator pod is running:

```bash
kubectl get pods -n ktrl-db-query-system
# Look for a pod named like controller-manager-...

# View logs
kubectl logs -n ktrl-db-query-system -l control-plane=controller-manager -f
```

## Usage

Create a `DatabaseQueryResource` custom resource to tell the operator which database to query and how to generate resources.

**Example `config/samples/database_v1alpha1_databasequeryresource.yaml`:**

```yaml
apiVersion: database.example.com/v1alpha1
kind: DatabaseQueryResource
metadata:
  name: user-configmaps-example
  namespace: default # Namespace where this CR is deployed and where resources will be created by default
spec:
  # How often to query the database and reconcile
  pollInterval: "1m"
  # Whether to delete resources if their corresponding DB row disappears (default: true)
  prune: true
  database:
    type: postgres
    connectionSecretRef:
      # Name of the Secret created earlier
      name: db-credentials
      # Optional: Namespace of the Secret (defaults to this CR's namespace)
      # namespace: database-secrets
      # Optional: Override default keys in the Secret
      # hostKey: DB_HOST
      # portKey: DB_PORT
      # userKey: DB_USER
      # passwordKey: DB_PASSWORD
      # dbNameKey: DB_NAME
      # sslModeKey: DB_SSLMODE
  # The SQL query to execute
  query: "SELECT username, user_id, email, status FROM users WHERE status = 'active';"
  # Go template for the Kubernetes resource(s)
  template: |
    apiVersion: v1
    kind: ConfigMap
    metadata:
      # Name must be unique per row. Use data from the row.
      # Ensure the resulting name is DNS-compatible!
      name: user-{{ .Row.username | lower }}-config
      # Optional: Specify namespace, otherwise defaults to CR's namespace.
      # Use {{ .Metadata.Namespace }} to explicitly use the CR's namespace.
      namespace: {{ .Metadata.Namespace }}
      labels:
        # Use DB data in labels/annotations
        user_id: "{{ .Row.user_id }}"
        # This label is automatically added by the controller:
        # database.example.com/managed-by: user-configmaps-example
    data:
      email: "{{ .Row.email }}"
      status: "{{ .Row.status }}"
      username: "{{ .Row.username }}"
      # Example using Go template functions (time)
      managedTimestamp: "{{ now | date "2006-01-02T15:04:05Z07:00" }}"
```

**Apply the sample CR:**

```bash
kubectl apply -f config/samples/database_v1alpha1_databasequeryresource.yaml -n default
```

**Check the results:**
After the `pollInterval` duration, the operator should query the database and create resources based on the template.

```bash
# Check the status of the DatabaseQueryResource
kubectl get databasequeryresource user-configmaps-example -n default -o yaml

# Check for created resources (ConfigMaps in this example)
kubectl get configmaps -n default -l database.example.com/managed-by=user-configmaps-example
kubectl get configmap user-alice-config -n default -o yaml # Example for user 'alice'
```

## CRD Specification (`DatabaseQueryResourceSpec`)

* `pollInterval` (string, required): Duration string specifying how often to poll the database (e.g., `"30s"`, `"5m"`, `"1h"`).
* `prune` (boolean, optional, default: `true`): If `true`, resources previously managed by this CR that no longer correspond to a row in the latest query result will be deleted.
* `database` (object, required):
  * `type` (string, required, enum: `"postgres"`): The type of database. Currently only `postgres`.
  * `connectionSecretRef` (object, required): Reference to the Secret containing connection details.
    * `name` (string, required): Name of the Secret.
    * `namespace` (string, optional): Namespace of the Secret. Defaults to the `DatabaseQueryResource`'s namespace.
    * `hostKey` (string, optional): Key in the Secret for the hostname. Defaults to `"host"`.
    * `portKey` (string, optional): Key in the Secret for the port. Defaults to `"port"`.
    * `userKey` (string, optional): Key in the Secret for the username. Defaults to `"username"`.
    * `passwordKey` (string, optional): Key in the Secret for the password. Defaults to `"password"`.
    * `dbNameKey` (string, optional): Key in the Secret for the database name. Defaults to `"dbname"`.
    * `sslModeKey` (string, optional): Key in the Secret for the SSL mode. Defaults to `"sslmode"`. If the key is not found and `sslModeKey` is not specified, `prefer` is used.
* `query` (string, required): The SQL query to execute. It should be a read-only query.
* `template` (string, required): A Go template string that renders a valid Kubernetes resource manifest (YAML or JSON).
  * **Template Context:** The template receives a map with the following structure:

```
{
    "Row": {
    "column1_name": value1,
    "column2_name": value2,
    // ... other columns from the query result
    },
    "Metadata": { // Metadata of the parent DatabaseQueryResource CR
        "Name": "cr-name",
        "Namespace": "cr-namespace"
        // ... other metav1.ObjectMeta fields
    }
}
```

* You can use standard Go template functions (sprig library is *not* included by default, but could be added). Access row data via `.Row.column_name`. Access parent CR metadata via `.Metadata.Namespace`, etc.

## Development

1. **Prerequisites:** Ensure Go, Docker, `kubectl`, `controller-gen`, and access to a Kubernetes cluster are set up.
2. **Clone:** `git clone <repository-url>`
3. **Modify Code:** Make changes to the API (`api/v1alpha1/`) or controller (`internal/controller/`).
4. **Regenerate Code:** After modifying API types or RBAC/CRD markers, run:

    ```bash
    # Regenerate deepcopy methods for API types
    controller-gen object paths=./api/v1alpha1

    # Regenerate CRD and RBAC manifests
    # Adjust paths if needed, especially on Windows: paths=./api/v1alpha1,./internal/controller
    controller-gen rbac:roleName=manager-role crd webhook paths=./api/v1alpha1,./internal/controller output:crd:artifacts:config=config/crd/bases output:rbac:artifacts:config=config/rbac
    ```

5. **Build:**

    ```bash
    go build ./...
    # Or build the container image (see step 4 in Getting Started)
    ```

6. **Deploy:** Re-deploy the operator using the steps in "Getting Started".

## Contributing

Contributions are welcome! Please follow standard GitHub practices: fork the repository, create a feature branch, make your changes, and submit a pull request. Ensure your code builds, passes any tests, and includes updates to documentation if necessary.

<!-- Add more specific contribution guidelines if desired -->

## License

This project is licensed under the Apache License 2.0. See the [LICENSE](LICENSE) file for details.
