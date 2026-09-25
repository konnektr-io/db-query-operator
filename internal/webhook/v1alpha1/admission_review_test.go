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

package v1alpha1

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	admissionv1 "k8s.io/api/admission/v1"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	databasev1alpha1 "github.com/konnektr-io/db-query-operator/api/v1alpha1"
)

// validatePath is the path controller-runtime derives for this webhook:
// /validate-<group with dots replaced by dashes>-<version>-<lowercased kind>.
const validatePath = "/validate-konnektr-io-v1alpha1-databasequeryresource"

// warningRecorder captures the Warning headers client-go would hand to kubectl.
type warningRecorder struct {
	mu       sync.Mutex
	messages []string
}

// HandleWarningHeader implements rest.WarningHandler.
func (w *warningRecorder) HandleWarningHeader(_ int, _ string, text string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.messages = append(w.messages, text)
}

func (w *warningRecorder) Messages() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.messages...)
}

// admissionReview builds the v1 AdmissionReview the API server would POST for
// op. mutate may be nil; otherwise it receives a valid resource to break.
func admissionReview(op admissionv1.Operation, uid string, mutate func(*databasev1alpha1.DatabaseQueryResource)) *admissionv1.AdmissionReview {
	dbqr := validResource()
	if mutate != nil {
		mutate(dbqr)
	}
	// The API server always encodes the type metadata of the stored object and
	// the webhook's decoder resolves the target type from it.
	dbqr.TypeMeta = metav1.TypeMeta{
		APIVersion: databasev1alpha1.GroupVersion.String(),
		Kind:       "DatabaseQueryResource",
	}
	raw, err := json.Marshal(dbqr)
	Expect(err).NotTo(HaveOccurred())

	return &admissionv1.AdmissionReview{
		TypeMeta: metav1.TypeMeta{APIVersion: admissionv1.SchemeGroupVersion.String(), Kind: "AdmissionReview"},
		Request: &admissionv1.AdmissionRequest{
			UID:       types.UID(uid),
			Kind:      metav1.GroupVersionKind{Group: "konnektr.io", Version: "v1alpha1", Kind: "DatabaseQueryResource"},
			Resource:  metav1.GroupVersionResource{Group: "konnektr.io", Version: "v1alpha1", Resource: "databasequeryresources"},
			Operation: op,
			Object:    runtime.RawExtension{Raw: raw},
		},
	}
}

// postAdmissionReview posts rawBody to the webhook endpoint over the local
// serving address and returns the HTTP status and decoded AdmissionReview.
func postAdmissionReview(rawBody []byte) (int, *admissionv1.AdmissionReview) {
	httpClient := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			// The envtest serving certificate is generated for the local
			// serving host; the admission protocol is under test, not TLS.
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // test server
		},
	}
	url := fmt.Sprintf("https://%s:%d%s",
		testEnv.WebhookInstallOptions.LocalServingHost,
		testEnv.WebhookInstallOptions.LocalServingPort,
		validatePath,
	)

	resp, err := httpClient.Post(url, "application/json", bytes.NewReader(rawBody))
	Expect(err).NotTo(HaveOccurred())
	defer func() { Expect(resp.Body.Close()).To(Succeed()) }()

	body, err := io.ReadAll(resp.Body)
	Expect(err).NotTo(HaveOccurred())
	Expect(body).NotTo(BeEmpty(), "webhook returned an empty body with status %d", resp.StatusCode)

	review := &admissionv1.AdmissionReview{}
	Expect(json.Unmarshal(body, review)).To(Succeed())
	return resp.StatusCode, review
}

func encodeReview(review *admissionv1.AdmissionReview) []byte {
	raw, err := json.Marshal(review)
	Expect(err).NotTo(HaveOccurred())
	return raw
}

// The specs below exercise the admission wire contract directly: the
// envtest specs prove the API server rejects bad resources, but they cannot
// show what the webhook puts in its response — the UID echo, the warning list
// and the behaviour on a body the decoder cannot read.
var _ = Describe("AdmissionReview wire protocol", func() {
	It("allows a valid resource and echoes the request UID", func() {
		status, review := postAdmissionReview(encodeReview(
			admissionReview(admissionv1.Create, "uid-valid", nil),
		))

		Expect(status).To(Equal(http.StatusOK))
		Expect(review.Response).NotTo(BeNil())
		Expect(review.Response.UID).To(Equal(types.UID("uid-valid")))
		Expect(review.Response.Allowed).To(BeTrue())
		Expect(review.Response.Warnings).To(BeEmpty())
	})

	It("denies an invalid resource, naming the offending field", func() {
		status, review := postAdmissionReview(encodeReview(
			admissionReview(admissionv1.Create, "uid-denied", func(r *databasev1alpha1.DatabaseQueryResource) {
				r.Spec.PollInterval = "0s"
			}),
		))

		Expect(status).To(Equal(http.StatusOK))
		Expect(review.Response).NotTo(BeNil())
		// A response without the UID echoed is rejected by the API server
		// before the message is ever shown, so it must survive a denial.
		Expect(review.Response.UID).To(Equal(types.UID("uid-denied")))
		Expect(review.Response.Allowed).To(BeFalse())
		Expect(review.Response.Result.Message).To(ContainSubstring("spec.pollInterval"))
		// The "admission webhook ... rejected the request" prefix is added by
		// the API server when it relays the denial (and is what the envtest
		// specs assert); on the wire the message is the validator's own text.
		Expect(review.Response.Result.Message).To(ContainSubstring("must be greater than zero"))
	})

	It("warns — without denying — when changePollInterval is not shorter than pollInterval", func() {
		_, review := postAdmissionReview(encodeReview(
			admissionReview(admissionv1.Create, "uid-warning", func(r *databasev1alpha1.DatabaseQueryResource) {
				r.Spec.PollInterval = "1m"
				r.Spec.ChangeDetection = &databasev1alpha1.ChangeDetectionConfig{
					Enabled:            true,
					TableName:          "users",
					TimestampColumn:    "updated_at",
					ChangePollInterval: "2m",
				}
			}),
		))

		Expect(review.Response).NotTo(BeNil())
		Expect(review.Response.Allowed).To(BeTrue())
		Expect(review.Response.Warnings).NotTo(BeEmpty(),
			"the warning must survive serialization to reach the API server")
		Expect(strings.Join(review.Response.Warnings, "\n")).
			To(ContainSubstring("spec.changeDetection.changePollInterval"))
	})

	It("answers a decode error for a malformed body and keeps serving", func() {
		status, review := postAdmissionReview(
			[]byte(`{"apiVersion":"admission.k8s.io/v1","kind":"AdmissionReview","request":`),
		)

		// controller-runtime always answers 200 and carries the failure in
		// the response: allowed=false with the status code as result.code.
		Expect(status).To(Equal(http.StatusOK))
		Expect(review.Response).NotTo(BeNil())
		Expect(review.Response.Allowed).To(BeFalse())
		Expect(review.Response.Result).NotTo(BeNil())
		Expect(review.Response.Result.Code).To(Equal(int32(http.StatusBadRequest)))

		By("and the endpoint keeps answering well-formed reviews afterwards")
		_, ok := postAdmissionReview(encodeReview(
			admissionReview(admissionv1.Create, "uid-after-garbage", nil),
		))
		Expect(ok.Response).NotTo(BeNil())
		Expect(ok.Response.Allowed).To(BeTrue())
	})
})

var _ = Describe("admission warnings delivered through the API server", func() {
	It("forwards changePollInterval warnings to the requesting client", func() {
		recorder := &warningRecorder{}
		warnCfg := rest.CopyConfig(cfg)
		warnCfg.WarningHandler = recorder
		warnClient, err := client.New(warnCfg, client.Options{Scheme: scheme.Scheme})
		Expect(err).NotTo(HaveOccurred())

		dbqr := validResource()
		dbqr.Name = "webhook-warning"
		dbqr.Spec.PollInterval = "1m"
		dbqr.Spec.ChangeDetection = &databasev1alpha1.ChangeDetectionConfig{
			Enabled:            true,
			TableName:          "users",
			TimestampColumn:    "updated_at",
			ChangePollInterval: "2m",
		}
		Expect(warnClient.Create(ctx, dbqr)).To(Succeed())
		DeferCleanup(func() {
			Expect(warnClient.Delete(ctx, dbqr)).To(Succeed())
		})

		Expect(recorder.Messages()).NotTo(BeEmpty(),
			"the API server must surface the webhook warning to the client")
		Expect(strings.Join(recorder.Messages(), "\n")).
			To(ContainSubstring("spec.changeDetection.changePollInterval"))
	})
})

var _ = Describe("the generated webhook configuration", func() {
	// With failurePolicy: Fail an unreachable webhook rejects every routed
	// write. Deletion must stay outside that blast radius, and the failure
	// policy is a documented default — both are cheap to lock in here.
	It("routes only CREATE and UPDATE and keeps failurePolicy at Fail", func() {
		raw, err := os.ReadFile(filepath.Join("..", "..", "..", "config", "webhook", "manifests.yaml"))
		Expect(err).NotTo(HaveOccurred())

		config := &admissionregistrationv1.ValidatingWebhookConfiguration{}
		Expect(yaml.Unmarshal(raw, config)).To(Succeed())
		Expect(config.Webhooks).To(HaveLen(1))

		webhook := config.Webhooks[0]
		Expect(webhook.FailurePolicy).NotTo(BeNil())
		Expect(*webhook.FailurePolicy).To(Equal(admissionregistrationv1.Fail))
		Expect(webhook.Rules).To(HaveLen(1))
		Expect(webhook.Rules[0].Operations).To(ConsistOf(
			admissionregistrationv1.Create,
			admissionregistrationv1.Update,
		))
	})
})
