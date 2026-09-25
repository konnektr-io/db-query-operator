# Developer Guide

## Prerequisites

- **Go 1.26+** ([download](https://go.dev/dl/))
- **kubebuilder envtest** binaries (for integration tests)
- **make** (for build targets)
- Optional: a Kubernetes cluster for end-to-end testing

## Getting Started

```bash
# Clone (if you haven't already)
git clone https://github.com/konnektr-io/db-query-operator
cd db-query-operator

# Build everything
go build ./...

# Run all tests (unit + integration)
export KUBEBUILDER_ASSETS=$(setup-envtest use -p path 2>/dev/null)
go test ./... -count=1 -timeout 300s
```

## Test Suites

| Suite | Command | Description |
|---|---|---|
| **Unit tests** | `go test ./internal/controller/... -count=1 -run 'Test' -v` | Pure logic tests (fast, no envtest) |
| **Integration tests** | `go test ./internal/controller/... -count=1 -run 'TestControllers' -v` | Controller suite with envtest (etcd + kube-apiserver) |
| **Webhook unit tests** | `go test ./internal/webhook/... -count=1 -run 'TestValidate|TestValidator' -v` | Admission validation logic (fast, no envtest) |
| **Webhook integration tests** | `go test ./internal/webhook/... -count=1 -run 'TestWebhooks' -v` | Validating webhook served to a real API server via envtest |
| **Util tests** | `go test ./internal/util/... -count=1 -v` | Utility function tests |
| **All tests** | `go test ./... -count=1 -timeout 300s -v` | Everything |

### Envtest Setup

The integration tests use [envtest](https://pkg.go.dev/sigs.k8s.io/controller-runtime/pkg/envtest) to spin up a local etcd + kube-apiserver. Install the required binaries:

```bash
# Install setup-envtest
go install sigs.k8s.io/controller-runtime/tools/setup-envtest@latest

# Download k8s binaries (choose a version matching your controller-runtime)
setup-envtest use -p path

# Set the env var before running tests
export KUBEBUILDER_ASSETS=$(setup-envtest use -p path 2>/dev/null)
```

The first run may take ~30s while envtest downloads binaries. Subsequent runs use the cached versions.

## Project Structure

```
├── main.go                          # Entry point
├── api/
│   └── v1alpha1/                    # CRD types (DatabaseQueryResource)
│       ├── databasequeryresource_types.go
│       └── groupversion_info.go
├── internal/
│   ├── controller/                  # Reconciliation logic
│   │   ├── databasequeryresource_controller.go
│   │   ├── databasequeryresource_controller_test.go  # Integration tests (ginkgo)
│   │   ├── funcs.go                # Template functions
│   │   ├── reconcile_logic_test.go # Pure unit tests
│   │   └── suite_test.go           # Envtest bootstrap
│   ├── webhook/
│   │   └── v1alpha1/               # Admission validation
│   │       ├── databasequeryresource_webhook.go  # Validator + kubebuilder markers
│   │       ├── validation.go      # Validation rules (also used by the unit tests)
│   │       ├── validation_test.go # Unit tests
│   │       ├── webhook_integration_test.go # envtest: API server rejects invalid CRs
│   │       └── suite_test.go      # Envtest bootstrap (webhook install options)
│   └── util/
│       ├── database_client.go      # DatabaseClient interface
│       ├── postgres_client.go      # PostgreSQL implementation
│       ├── postgres_client_test.go # Postgres-specific tests
│       ├── gvk.go                  # GVK parsing
│       ├── gvk_test.go
│       └── mock_database_client.go
├── config/
│   ├── samples/                    # Sample CRs
│   ├── crd/                        # Generated CRD manifests
│   ├── rbac/                       # RBAC manifests
│   ├── webhook/                    # Generated ValidatingWebhookConfiguration + Service
│   ├── certmanager/                # Self-signed Issuer + Certificate for the webhook
│   └── manager/                    # Operator Deployment (mounts the serving certificate)
└── docs/                           # Documentation site content (MDX)
```

## Code Quality

```bash
# Run all checks
go vet ./...          # Static analysis
go fmt ./...          # Formatting
go test ./... -count=1 -timeout 300s  # Tests with envtest
```

## Making Changes

1. **Create a feature branch**: `git checkout -b feature/my-feature`
2. **Make changes** to controller logic, API types, or utilities
3. **Add tests** — unit tests for pure logic (`reconcile_logic_test.go`), integration tests for controller behavior (`*_test.go` with ginkgo)
4. **Run the full test suite** — `go test ./... -count=1 -timeout 300s`
5. **Update documentation** in `README.md` and/or `docs/`
6. **Submit a PR**

### If You Modify API Types

After changing `api/v1alpha1/` types, regenerate deepcopy and CRD manifests:

```bash
# Install controller-gen if needed
go install sigs.k8s.io/controller-tools/cmd/controller-gen@v0.18.0

# Regenerate (or simply run `make manifests`, which pins the same version)
controller-gen object paths=./api/v1alpha1
controller-gen rbac:roleName=manager-role crd webhook "paths={./api/v1alpha1,./internal/controller,./internal/webhook/...}" output:crd:artifacts:config=config/crd/bases output:rbac:artifacts:config=config/rbac output:webhook:artifacts:config=config/webhook
```

Note the `paths={a,b,c}` slice syntax — controller-gen does not accept the comma-separated
`paths=a,b,c` form.

### If You Add or Change a Webhook

1. Add or edit the validator under `internal/webhook/v1alpha1/`. The
   `+kubebuilder:webhook:` marker above the validator type drives manifest generation; make
   sure its `path` matches the path controller-runtime derives
   (`/validate-<group>-<version>-<kind>`).
2. Run `make manifests` to regenerate `config/webhook/manifests.yaml`.
3. Update `config/kustomization.yaml` if the webhook needs new resources. The namespace,
   name prefix, Service reference, certificate `dnsNames` and the cert-manager
   `inject-ca-from` annotation are all derived there, so verify the render:
   ```bash
   kustomize build config/ | grep -A 20 ValidatingWebhookConfiguration
   ```
4. Add unit tests for the validation rules (`validation_test.go`) and, when the webhook must
   be reached through the API server, an envtest spec (`webhook_integration_test.go`). The
   webhook suite installs `config/webhook/manifests.yaml` into the test API server, so a spec
   that asserts a rejection must also assert it was the webhook that rejected it.

## Build Targets

```bash
make build    # Build the operator binary
make test     # Run tests (requires KUBEBUILDER_ASSETS)
make docker-build # Build container image
```

## CI Pipeline

The project uses GitHub Actions (`.github/workflows/build-push.yaml`). The pipeline:
1. Builds the operator
2. Runs tests (unit + integration with envtest)
3. Builds and publishes container image to GHCR
4. Publishes Helm chart

## Need Help?

Open a [GitHub Issue](https://github.com/konnektr-io/db-query-operator/issues).
