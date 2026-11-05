/*
Copyright The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package apidefinitions

import (
	"context"
	"errors"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/util/retry"
)

// metadataCase injects one invalid ObjectMeta value into an otherwise valid object.
type metadataCase struct {
	name string
	// field is the path the rejection must point at. Causes are matched by prefix
	// so that both "metadata.finalizers" and "metadata.finalizers[0]" count.
	field  string
	mutate func(obj *unstructured.Unstructured)
	// createOnly is set for fields that cannot change on update, where a rejection
	// would come from immutability checks rather than from validating the value.
	createOnly bool
}

var invalidMetadataCases = []metadataCase{
	{
		name:       "name",
		field:      "metadata.name",
		mutate:     func(obj *unstructured.Unstructured) { obj.SetName("invalid/name") },
		createOnly: true,
	},
	{
		name:  "label-key",
		field: "metadata.labels",
		mutate: func(obj *unstructured.Unstructured) {
			obj.SetLabels(map[string]string{"invalid@key": "value"})
		},
	},
	{
		name:  "annotation-key",
		field: "metadata.annotations",
		mutate: func(obj *unstructured.Unstructured) {
			obj.SetAnnotations(map[string]string{"invalid@key": "value"})
		},
	},
	{
		// Finalizers without a domain prefix are only allowed for the standard
		// finalizers (kubernetes, orphan, foregroundDeletion).
		name:  "finalizer-not-fully-qualified",
		field: "metadata.finalizers",
		mutate: func(obj *unstructured.Unstructured) {
			obj.SetFinalizers([]string{"nodomain"})
		},
	},
}

// TestObjectMetaValidation verifies that every resource rejects invalid ObjectMeta
// on create and update, and reports each metadata error exactly once.
//
// Rationale: ObjectMeta is validated both by type-specific validation (strategy.Validate)
// and by the generic REST layer. This test guarantees metadata is validated at least
// once for every resource, so the two paths can be consolidated without silently
// dropping validation. See https://github.com/kubernetes/kubernetes/issues/138654.
func TestObjectMetaValidation(t *testing.T) {
	// DO NOT ADD NEW ENTRIES HERE.
	// Resources that accept finalizers that are neither standard nor fully qualified,
	// because their validation uses the lax apimachinery ObjectMeta validation.
	// See https://github.com/kubernetes/kubernetes/issues/138648.
	laxFinalizerOnCreateExempt := sets.New(
		"apiservices.v1.apiregistration.k8s.io",
		"customresourcedefinitions.v1.apiextensions.k8s.io",
		"events.v1",
		"leasecandidates.v1alpha2.coordination.k8s.io",
		"leasecandidates.v1beta1.coordination.k8s.io",
		"leases.v1.coordination.k8s.io",
		"mutatingadmissionpolicies.v1.admissionregistration.k8s.io",
		"mutatingadmissionpolicies.v1alpha1.admissionregistration.k8s.io",
		"mutatingadmissionpolicies.v1beta1.admissionregistration.k8s.io",
		"mutatingadmissionpolicybindings.v1.admissionregistration.k8s.io",
		"mutatingadmissionpolicybindings.v1alpha1.admissionregistration.k8s.io",
		"mutatingadmissionpolicybindings.v1beta1.admissionregistration.k8s.io",
		"mutatingwebhookconfigurations.v1.admissionregistration.k8s.io",
		"poddisruptionbudgets.v1.policy",
		"runtimeclasses.v1.node.k8s.io",
		"validatingadmissionpolicies.v1.admissionregistration.k8s.io",
		"validatingadmissionpolicybindings.v1.admissionregistration.k8s.io",
		"validatingwebhookconfigurations.v1.admissionregistration.k8s.io",
		// Custom resources.
		"foos.v1.cr.bar.com",
		"integers.v1.random.numbers.com",
		"pandas.v1.awesome.bears.com",
		"pandas.v3.awesome.bears.com",
		"pants.v2.custom.fancy.com",
	)
	// StorageVersion validates finalizers strictly on create but not on update.
	laxFinalizerOnUpdateExempt := laxFinalizerOnCreateExempt.Union(sets.New(
		"storageversions.v1alpha1.internal.apiserver.k8s.io",
	))

	TestAllDefinitions(t, "metadata-validation", func(t *testing.T, api Definition) {
		if !api.HasVerb("create") {
			t.Skip("Resource does not support create")
		}
		client := api.ResourceClient()

		for _, tc := range invalidMetadataCases {
			// Services allocate a ClusterIP before validation runs, and the allocator rejects
			// the IPAddress whose parentRef carries the invalid name, so the error never
			// points at metadata.name.
			if tc.name == "name" && ResourceString(api.Mapping.Resource) == "services.v1" {
				continue
			}
			obj := TestObj(t, api.StorageData.Stub, "{}", api.Mapping.GroupVersionKind)
			tc.mutate(obj)
			_, err := client.Create(context.TODO(), obj, metav1.CreateOptions{})
			causes := invalidCauses(t, "create "+tc.name, err, tc.field)

			exempt := sets.New[string]()
			if tc.name == "finalizer-not-fully-qualified" {
				exempt = laxFinalizerOnCreateExempt
			}
			assertDefault(t, api.Mapping.Resource, "create with invalid "+tc.field+" must be rejected", len(causes) > 0, exempt)
			if hasDuplicates(causes) {
				t.Errorf("%s: create with invalid %s reported duplicate errors: %v", ResourceString(api.Mapping.Resource), tc.field, causes)
			}
		}

		if !api.HasVerb("get") || !api.HasVerb("update") {
			return
		}
		// The lax finalizer case above may have created the object, so reuse it if present.
		name := TestObj(t, api.StorageData.Stub, "{}", api.Mapping.GroupVersionKind).GetName()
		if _, err := client.Get(context.TODO(), name, metav1.GetOptions{}); apierrors.IsNotFound(err) {
			valid := TestObj(t, api.StorageData.Stub, "{}", api.Mapping.GroupVersionKind)
			if _, err := client.Create(context.TODO(), valid, metav1.CreateOptions{}); err != nil {
				t.Fatalf("Failed to create valid object: %v", err)
			}
		} else if err != nil {
			t.Fatalf("Failed to get object: %v", err)
		}

		for _, tc := range invalidMetadataCases {
			if tc.createOnly {
				continue
			}
			// Re-read each time: a case that is wrongly accepted changes the stored object.
			// Retry on conflict, since controllers (e.g. for CRDs) may write the object concurrently.
			err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
				latest, err := client.Get(context.TODO(), name, metav1.GetOptions{})
				if err != nil {
					return err
				}
				tc.mutate(latest)
				_, err = client.Update(context.TODO(), latest, metav1.UpdateOptions{})
				return err
			})
			causes := invalidCauses(t, "update "+tc.name, err, tc.field)

			exempt := sets.New[string]()
			if tc.name == "finalizer-not-fully-qualified" {
				exempt = laxFinalizerOnUpdateExempt
			}
			assertDefault(t, api.Mapping.Resource, "update with invalid "+tc.field+" must be rejected", len(causes) > 0, exempt)
			if hasDuplicates(causes) {
				t.Errorf("%s: update with invalid %s reported duplicate errors: %v", ResourceString(api.Mapping.Resource), tc.field, causes)
			}
		}
	})
}

// invalidCauses returns the causes of an Invalid error that point at field.
// Any error other than Invalid is fatal, since it means the request failed for an
// unrelated reason and says nothing about metadata validation.
func invalidCauses(t *testing.T, op string, err error, field string) []metav1.StatusCause {
	t.Helper()
	if err == nil {
		return nil
	}
	var statusErr *apierrors.StatusError
	if !apierrors.IsInvalid(err) || !errors.As(err, &statusErr) || statusErr.ErrStatus.Details == nil {
		t.Fatalf("%s: expected an Invalid error, got: %v", op, err)
	}
	var causes []metav1.StatusCause
	for _, c := range statusErr.ErrStatus.Details.Causes {
		if strings.HasPrefix(c.Field, field) {
			causes = append(causes, c)
		}
	}
	if len(causes) == 0 {
		t.Fatalf("%s: rejected, but no cause points at %s: %v", op, field, err)
	}
	return causes
}

func hasDuplicates(causes []metav1.StatusCause) bool {
	seen := sets.New[metav1.StatusCause]()
	for _, c := range causes {
		if seen.Has(c) {
			return true
		}
		seen.Insert(c)
	}
	return false
}
