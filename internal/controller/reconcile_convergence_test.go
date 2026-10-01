// SPDX-License-Identifier: Apache-2.0
// Copyright The Kubernetes authors.

package controller

import (
	"bytes"
	"context"
	"testing"
	"time"

	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/event"

	databasev1alpha1 "github.com/konnektr-io/db-query-operator/api/v1alpha1"
)

// decodeManifest mirrors the production render path: the template output is
// decoded with yaml.NewYAMLOrJSONDecoder, which yields int64 for YAML integers.
// Building the desired state this way is what makes the int64/float64 mismatch
// in shouldUpdateResource observable (#21).
func decodeManifest(t *testing.T, manifest string) *unstructured.Unstructured {
	t.Helper()
	obj := &unstructured.Unstructured{}
	decoder := utilyaml.NewYAMLOrJSONDecoder(bytes.NewReader([]byte(manifest)), 4096)
	if err := decoder.Decode(obj); err != nil {
		t.Fatalf("failed to decode manifest: %v", err)
	}
	return obj
}

func newTestReconciler(t *testing.T, objs ...client.Object) *DatabaseQueryResourceReconciler {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add corev1 to scheme: %v", err)
	}
	return &DatabaseQueryResourceReconciler{
		Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build(),
		Scheme: scheme,
	}
}

func cmGVK() schema.GroupVersionKind {
	return schema.GroupVersionKind{Version: "v1", Kind: "ConfigMap"}
}

// TestShouldUpdateResource_ConvergesWithIntegersInSpec reproduces root cause 2
// of #21: the stored config is JSON-decoded (float64 numbers) while the desired
// config is YAML-decoded (int64 numbers). reflect.DeepEqual compares the Go
// types, so int64(2) != float64(2) and every rendered object containing an
// integer was reported as "needs update" on every single reconcile.
func TestShouldUpdateResource_ConvergesWithIntegersInSpec(t *testing.T) {
	g := NewWithT(t)

	const manifest = `
apiVersion: v1
kind: ConfigMap
metadata:
  name: digitaltwins-env-dropbox
  namespace: argocd
data:
  values.yaml: |
    cluster:
      instances: 2
`

	// First pass: the object does not exist yet.
	desired := decodeManifest(t, manifest)
	desired.SetLabels(map[string]string{ManagedByLabel: "digitaltwins-envs-dbqr"})
	r := newTestReconciler(t)

	updateNeeded, err := r.shouldUpdateResource(context.Background(), desired)
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(updateNeeded).To(BeTrue(), "a resource that does not exist yet must be applied")

	// Apply it, recording the last-applied configuration exactly as the
	// reconciler does before writing the object to the cluster.
	applied := decodeManifest(t, manifest)
	applied.SetLabels(map[string]string{ManagedByLabel: "digitaltwins-envs-dbqr"})
	g.Expect(setLastAppliedConfig(applied)).To(Succeed())

	r = newTestReconciler(t, applied)

	// Second pass: nothing in the database or the template changed, so the
	// freshly rendered object must compare equal to what we stored.
	secondPass := decodeManifest(t, manifest)
	secondPass.SetLabels(map[string]string{ManagedByLabel: "digitaltwins-envs-dbqr"})

	updateNeeded, err = r.shouldUpdateResource(context.Background(), secondPass)
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(updateNeeded).To(BeFalse(),
		"an unchanged rendered object must not be reported as needing an update (int64/float64 mismatch)")
}

// TestShouldUpdateResource_ConvergesForConfigMapData reproduces the second,
// independent non-converging path: setLastAppliedConfig stored only Spec,
// Labels and Annotations, while shouldUpdateResource also compares Data.
// lastApplied.Data was therefore always nil and any template rendering a
// data: block was re-applied forever (#21).
func TestShouldUpdateResource_ConvergesForConfigMapData(t *testing.T) {
	g := NewWithT(t)

	const manifest = `
apiVersion: v1
kind: ConfigMap
metadata:
  name: env-config
  namespace: default
data:
  mode: production
  replicas: "3"
`

	applied := decodeManifest(t, manifest)
	applied.SetLabels(map[string]string{ManagedByLabel: "test-dbqr"})
	g.Expect(setLastAppliedConfig(applied)).To(Succeed())

	r := newTestReconciler(t, applied)

	secondPass := decodeManifest(t, manifest)
	secondPass.SetLabels(map[string]string{ManagedByLabel: "test-dbqr"})

	updateNeeded, err := r.shouldUpdateResource(context.Background(), secondPass)
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(updateNeeded).To(BeFalse(),
		"data must be persisted in the last-applied config so ConfigMap/Secret templates converge")
}

// TestShouldUpdateResource_DetectsRealChanges guards against over-correcting
// the comparison into a no-op: a genuine difference must still be reported.
func TestShouldUpdateResource_DetectsRealChanges(t *testing.T) {
	g := NewWithT(t)

	const appliedManifest = `
apiVersion: v1
kind: ConfigMap
metadata:
  name: env-config
  namespace: default
data:
  replicas: "3"
`
	const changedManifest = `
apiVersion: v1
kind: ConfigMap
metadata:
  name: env-config
  namespace: default
data:
  replicas: "5"
`

	applied := decodeManifest(t, appliedManifest)
	applied.SetLabels(map[string]string{ManagedByLabel: "test-dbqr"})
	g.Expect(setLastAppliedConfig(applied)).To(Succeed())

	r := newTestReconciler(t, applied)

	changed := decodeManifest(t, changedManifest)
	changed.SetLabels(map[string]string{ManagedByLabel: "test-dbqr"})

	updateNeeded, err := r.shouldUpdateResource(context.Background(), changed)
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(updateNeeded).To(BeTrue(), "a real content change must still trigger an update")
}

// TestNextRequeueInterval_ChangeDetectionNotStarved guards the regression that
// removing the self-triggering watch exposed: after a full reconciliation the
// controller used to requeue at pollInterval, so a CR configured with
// pollInterval 5m and changePollInterval 2s never ran its detection query again
// until the full interval elapsed — the accidental ~17s wake-ups had been the
// only thing keeping it alive (#21).
func TestNextRequeueInterval_ChangeDetectionNotStarved(t *testing.T) {
	tests := []struct {
		name         string
		spec         databasev1alpha1.DatabaseQueryResourceSpec
		pollInterval time.Duration
		want         time.Duration
	}{
		{
			name:         "change detection disabled uses pollInterval",
			spec:         databasev1alpha1.DatabaseQueryResourceSpec{},
			pollInterval: 15 * time.Minute,
			want:         15 * time.Minute,
		},
		{
			name: "change detection shorter than pollInterval wins",
			spec: databasev1alpha1.DatabaseQueryResourceSpec{
				ChangeDetection: &databasev1alpha1.ChangeDetectionConfig{
					Enabled:            true,
					ChangePollInterval: "2s",
				},
			},
			pollInterval: 5 * time.Minute,
			want:         2 * time.Second,
		},
		{
			name: "change detection longer than pollInterval does not delay the poll",
			spec: databasev1alpha1.DatabaseQueryResourceSpec{
				ChangeDetection: &databasev1alpha1.ChangeDetectionConfig{
					Enabled:            true,
					ChangePollInterval: "1h",
				},
			},
			pollInterval: 5 * time.Minute,
			want:         5 * time.Minute,
		},
		{
			name: "invalid changePollInterval falls back to the default",
			spec: databasev1alpha1.DatabaseQueryResourceSpec{
				ChangeDetection: &databasev1alpha1.ChangeDetectionConfig{
					Enabled:            true,
					ChangePollInterval: "not-a-duration",
				},
			},
			pollInterval: time.Minute,
			want:         10 * time.Second,
		},
		{
			name: "enabled change detection without an explicit interval uses the default",
			spec: databasev1alpha1.DatabaseQueryResourceSpec{
				ChangeDetection: &databasev1alpha1.ChangeDetectionConfig{Enabled: true},
			},
			pollInterval: 5 * time.Minute,
			want:         10 * time.Second,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := NewWithT(t)
			dbqr := &databasev1alpha1.DatabaseQueryResource{Spec: tt.spec}
			g.Expect(nextRequeueInterval(dbqr, tt.pollInterval)).To(Equal(tt.want))
		})
	}
}

// TestSpecOrLifecycleChangedPredicate covers root cause 1 of #21: the primary
// watch had no predicates, so every status write this controller made
// (status.lastPollTime always changes) re-enqueued the object and the
// RequeueAfter returned by Reconcile never won — collapsing a 15m poll
// interval to the duration of a single pass.
func TestSpecOrLifecycleChangedPredicate(t *testing.T) {
	base := func() *unstructured.Unstructured {
		obj := &unstructured.Unstructured{}
		obj.SetGroupVersionKind(cmGVK())
		obj.SetName("test-dbqr")
		obj.SetNamespace("default")
		return obj
	}
	mutation := func(mods ...func(*unstructured.Unstructured)) *unstructured.Unstructured {
		obj := base()
		for _, m := range mods {
			m(obj)
		}
		return obj
	}

	tests := []struct {
		name      string
		old, new  *unstructured.Unstructured
		wantAllow bool
		reason    string
	}{
		{
			name:      "status-only write is ignored",
			old:       base(),
			new:       mutation(func(o *unstructured.Unstructured) { o.SetResourceVersion("2") }),
			wantAllow: false,
			reason:    "the controller's own status timestamps must not re-enqueue the CR",
		},
		{
			name: "spec change is allowed",
			old:  base(),
			new: mutation(func(o *unstructured.Unstructured) {
				o.SetGeneration(2)
			}),
			wantAllow: true,
			reason:    "a generation bump means spec or metadata changed",
		},
		{
			name: "finalizer added is allowed",
			old:  base(),
			new: mutation(func(o *unstructured.Unstructured) {
				o.SetFinalizers([]string{DatabaseQueryFinalizer})
			}),
			wantAllow: true,
			reason:    "finalizer transitions must run the cleanup path",
		},
		{
			name:      "finalizer removed is allowed",
			old:       mutation(func(o *unstructured.Unstructured) { o.SetFinalizers([]string{DatabaseQueryFinalizer}) }),
			new:       base(),
			wantAllow: true,
			reason:    "finalizer removal completes deletion",
		},
		{
			name: "deletionTimestamp set is allowed",
			old:  base(),
			new: mutation(func(o *unstructured.Unstructured) {
				now := metav1.Now()
				o.SetDeletionTimestamp(&now)
			}),
			wantAllow: true,
			reason:    "deletion must trigger the finalizer cleanup",
		},
	}

	p := specOrLifecycleChangedPredicate()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := NewWithT(t)
			got := p.Update(event.UpdateEvent{ObjectOld: tt.old, ObjectNew: tt.new})
			g.Expect(got).To(Equal(tt.wantAllow), tt.reason)
		})
	}
}

// TestSpecOrLifecycleChangedPredicate_CreationAndDeletion ensures the filter
// does not swallow the events that start and end a reconcile.
func TestSpecOrLifecycleChangedPredicate_CreationAndDeletion(t *testing.T) {
	g := NewWithT(t)

	p := specOrLifecycleChangedPredicate()
	g.Expect(p.Create(event.CreateEvent{Object: &unstructured.Unstructured{}})).To(BeTrue(),
		"create must reconcile at least once")
	g.Expect(p.Delete(event.DeleteEvent{Object: &unstructured.Unstructured{}})).To(BeTrue(),
		"delete must be observed so finalizer cleanup runs")
	g.Expect(p.Generic(event.GenericEvent{Object: &unstructured.Unstructured{}})).To(BeFalse(),
		"generic events carry no actionable change")
}
