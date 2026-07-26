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
│   └── rbac/                       # RBAC manifests
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
go install sigs.k8s.io/controller-tools/cmd/controller-gen@latest

# Regenerate
controller-gen object paths=./api/v1alpha1
controller-gen rbac:roleName=manager-role crd webhook paths=./api/v1alpha1,./internal/controller output:crd:artifacts:config=config/crd/bases output:rbac:artifacts:config=config/rbac
```

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
