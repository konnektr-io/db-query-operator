# Database Query Operator

[![License: Apache 2.0](https://img.shields.io/badge/License-Apache%202.0-blue.svg)](https://opensource.org/licenses/Apache-2.0)
[![Build Status](https://github.com/konnektr-io/db-query-operator/actions/workflows/build-push.yaml/badge.svg)](https://github.com/konnektr-io/db-query-operator/actions/workflows/build-push.yaml)

## Overview

The Database Query Operator is a Kubernetes operator designed to manage Kubernetes resources based on the results of a database query. It periodically polls a specified database (currently PostgreSQL), executes a user-defined SQL query, and uses a Go template to render Kubernetes manifests for each row returned by the query.

The operator handles the reconciliation loop, ensuring that the resources in the cluster match the desired state defined by the database query results and the template. This allows for dynamic configuration and resource management driven directly by your application's database state.

## Features

* **CRD Driven:** Configuration is managed via a `DatabaseQueryResource` Custom Resource Definition.
* **Database Polling:** Periodically queries a database at a configurable interval.
* **PostgreSQL Support:** Currently supports PostgreSQL databases.
* **Custom Queries:** Execute any read-only SQL query to generate Kubernetes resources.
* **Go Templating:** Define Kubernetes resource manifests using Go templates, allowing data from query results to be injected.
* **Row-to-Resource Mapping:** Each row in the query result typically generates one Kubernetes resource.
* **Status Updates:** Optionally update the database with the status of the created resources after reconciliation. 
* **Secret Management:** Securely fetches database credentials from Kubernetes Secrets.
* **Reconciliation:** Creates, updates, and (optionally) deletes Kubernetes resources to match the query results.
* **Pruning:** Automatically cleans up resources previously created by the operator if they no longer correspond to a row in the database query result (configurable).
* **Ownership:** Sets Owner References on created resources (optional, but recommended) for automatic garbage collection by Kubernetes when the `DatabaseQueryResource` is deleted.
* **Labeling:** Labels created resources for easy identification and potential pruning.

## Prerequisites

* **kubectl:** For interacting with the Kubernetes cluster.
* **Helm:** For installing the operator.
* **Kubernetes Cluster:** Access to a Kubernetes cluster (e.g., kind, Minikube, EKS, GKE, AKS).
* **PostgreSQL Database:** A running PostgreSQL instance accessible from the Kubernetes cluster.

## Getting Started

### 1. Install the Operator using Helm

You can deploy the operator using Helm from the official chart repository:

```bash
helm repo add konnektr https://charts.konnektr.io
helm repo update
helm install db-query-operator konnektr/db-query-operator \
  --namespace <namespace> \
  --create-namespace \
  --set image.tag=<version> \
  --set gvkPattern="v1/ConfigMap;apps/v1/Deployment" \
  --set installCRDs=true
```

* By default, the image tag will match the Helm chart's `appVersion`.
* You can override any value in `values.yaml` using `--set` or a custom `values.yaml`.
* The `gvkPattern` parameter allows you to specify which Kubernetes resources the operator should manage.
* The CRDs are not installed by default; install with the installCRDs parameter or manually as described below.

#### Install the CRDs (required)

```bash
kubectl apply -f https://github.com/konnektr-io/db-query-operator/releases/latest/download/crds.yaml
```

#### Uninstall

```bash
helm uninstall db-query-operator -n <namespace>
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

### 3. Verify the Operator Pod

Check that the operator pod is running:

```bash
kubectl get pods -n <namespace>
# Look for a pod named like controller-manager-...

# View logs
kubectl logs -n <namespace> -l control-plane=controller-manager -f
```

### 4. Create Database Credentials Secret

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

## Usage

Create a `DatabaseQueryResource` custom resource to tell the operator which database to query and how to generate resources.

**Example `config/samples/database_v1alpha1_databasequeryresource.yaml`:**

```yaml
apiVersion: konnektr.io/v1alpha1
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
        # konnektr.io/managed-by: user-configmaps-example
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
kubectl get configmaps -n default -l konnektr.io/managed-by=user-configmaps-example
kubectl get configmap user-alice-config -n default -o yaml # Example for user 'alice'
```

### Example with `query` and `statusUpdateQueryTemplate`

Here is an example `DatabaseQueryResource` Custom Resource that uses both `query` and `statusUpdateQueryTemplate` fields. This example creates a Kubernetes `Deployment` for each row in the database and updates the database with the deployment's status:

```yaml
apiVersion: konnektr.io/v1alpha1
kind: DatabaseQueryResource
metadata:
  name: deployment-example
  namespace: default
spec:
  pollInterval: "1m"
  prune: true
  database:
    type: postgres
    connectionSecretRef:
      name: db-credentials
  query: |
    SELECT resource_id, name, replicas, image, status FROM deployments;
  template: |
    apiVersion: apps/v1
    kind: Deployment
    metadata:
      name: {{ .Row.name }}
      namespace: {{ .Metadata.Namespace }}
      labels:
        resource_id: "{{ .Row.resource_id }}"
    spec:
      replicas: {{ .Row.replicas }}
      selector:
        matchLabels:
          app: {{ .Row.name }}
      template:
        metadata:
          labels:
            app: {{ .Row.name }}
        spec:
          containers:
          - name: {{ .Row.name }}
            image: {{ .Row.image }}
  statusUpdateQueryTemplate: |
    UPDATE deployments
    SET status = '{{ .Resource.status.availableReplicas | default 0 }}'
    WHERE resource_id = '{{ .Resource.metadata.labels.resource_id }}';
```

In this example:

* The `query` fetches all rows from the `deployments` table.
* The `template` generates a Kubernetes `Deployment` for each row.
* The `statusUpdateQueryTemplate` updates the `status` field in the database based on the reconciliation outcome.

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
  * **Template Context:** The template receives a map with the following structure for the query:

  ```go
  {
      "Row": {
          "column1_name": value1,
          "column2_name": value2,
          // ... other columns from the query result
      }
  }
  ```
* `statusUpdateQueryTemplate` (string, optional): A Go template string for an SQL query that updates the status of the resource in the database after reconciliation.
  * **Template Context:** The template receives a map with the following structure for the status update query:

  ```go
  {
      "Resource": { // The individual child resource being updated
          "apiVersion": "v1/ConfigMap",
          "kind": "ConfigMap",
          "metadata": {
              "name": "user-alice-config",
              "namespace": "default",
              // ... other metadata fields
          },
          // ... other resource fields
      },
  }
  ```

  * You can use standard Go template functions. Access row data via `.Row.column_name`. Access parent CR metadata via `.Metadata.Namespace`, etc.

## Cascading Deletion and Finalizer Logic

By default, deleting a `DatabaseQueryResource` will **not** delete the resources it manages (such as ConfigMaps, Deployments, etc).

If you want the operator to delete all managed resources when the `DatabaseQueryResource` is deleted, you must explicitly add the following finalizer to the resource:

```yaml
metadata:
  finalizers:
    - konnektr.io/databasequeryresource-finalizer
```

When this finalizer is present, the operator will:

1. On deletion (when you run `kubectl delete databasequeryresource ...`), the operator will first delete all managed resources (those labeled with `konnektr.io/managed-by: <name>`).
2. Once all managed resources are deleted, the operator will remove the finalizer, allowing the `DatabaseQueryResource` to be deleted.

**How to use:**
- To enable cascading deletion, patch your resource before deleting:

  ```bash
  kubectl patch databasequeryresource <name> -n <namespace> --type='json' -p='[{"op": "add", "path": "/metadata/finalizers/-", "value": "konnektr.io/databasequeryresource-finalizer"}]'
  ```
- Then delete as usual:

  ```bash
  kubectl delete databasequeryresource <name> -n <namespace>
  ```

If the finalizer is not present, deleting the `DatabaseQueryResource` will **not** delete any managed resources.

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

## License

This project is licensed under the Apache License 2.0. See the [LICENSE](LICENSE) file for details.
