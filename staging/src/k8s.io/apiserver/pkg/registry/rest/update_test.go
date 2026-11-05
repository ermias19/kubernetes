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

package rest

import (
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
)

// fakeUpdateStrategy returns fixed errors from ValidateUpdate. It does not implement
// DeclarativeValidationStrategy, so ValidateUpdate's output is not post-processed.
type fakeUpdateStrategy struct {
	RESTUpdateStrategy
	errs field.ErrorList
}

func (s fakeUpdateStrategy) ValidateUpdate(ctx context.Context, obj, old runtime.Object) field.ErrorList {
	return s.errs
}

func (s fakeUpdateStrategy) NamespaceScoped() bool { return true }

func TestValidateUpdate(t *testing.T) {
	oldObj := &v1.ConfigMap{ObjectMeta: metav1.ObjectMeta{
		Name: "cm", Namespace: "default", UID: "uid", ResourceVersion: "1",
	}}
	withMeta := func(mutate func(*metav1.ObjectMeta)) *v1.ConfigMap {
		obj := oldObj.DeepCopy()
		mutate(&obj.ObjectMeta)
		return obj
	}
	labelErr := field.Invalid(field.NewPath("metadata", "labels"), "invalid@key", "strategy error")

	type errSummary struct {
		Type  field.ErrorType
		Field string
	}
	tests := []struct {
		name          string
		obj           *v1.ConfigMap
		strategyErrs  field.ErrorList
		expectedTypes []errSummary
	}{
		{
			name: "valid update",
			obj:  withMeta(func(m *metav1.ObjectMeta) { m.Labels = map[string]string{"a": "b"} }),
		},
		{
			// The generic checks used to run alongside the strategy and repeat its errors.
			name:          "strategy errors are returned without generic metadata errors",
			obj:           withMeta(func(m *metav1.ObjectMeta) { m.Labels = map[string]string{"invalid@key": "v"} }),
			strategyErrs:  field.ErrorList{labelErr},
			expectedTypes: []errSummary{{field.ErrorTypeInvalid, "metadata.labels"}},
		},
		{
			// ValidateObjectMetaAccessor and ValidateObjectMetaAccessorUpdate both check labels.
			name:          "generic checks report an invalid label once",
			obj:           withMeta(func(m *metav1.ObjectMeta) { m.Labels = map[string]string{"invalid@key": "v"} }),
			expectedTypes: []errSummary{{field.ErrorTypeInvalid, "metadata.labels"}},
		},
		{
			name:          "generic checks report an invalid annotation once",
			obj:           withMeta(func(m *metav1.ObjectMeta) { m.Annotations = map[string]string{"invalid@key": "v"} }),
			expectedTypes: []errSummary{{field.ErrorTypeInvalid, "metadata.annotations"}},
		},
		{
			name:          "generic checks still validate finalizers",
			obj:           withMeta(func(m *metav1.ObjectMeta) { m.Finalizers = []string{"invalid@finalizer"} }),
			expectedTypes: []errSummary{{field.ErrorTypeInvalid, "metadata.finalizers"}},
		},
		{
			name:          "generic checks still validate generateName",
			obj:           withMeta(func(m *metav1.ObjectMeta) { m.GenerateName = "invalid/" }),
			expectedTypes: []errSummary{{field.ErrorTypeInvalid, "metadata.generateName"}},
		},
		{
			name:          "generic checks still enforce immutable fields",
			obj:           withMeta(func(m *metav1.ObjectMeta) { m.UID = "other" }),
			expectedTypes: []errSummary{{field.ErrorTypeInvalid, "metadata.uid"}},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			errs := ValidateUpdate(context.Background(), tc.obj, oldObj, fakeUpdateStrategy{errs: tc.strategyErrs})
			var got []errSummary
			for _, err := range errs {
				got = append(got, errSummary{err.Type, err.Field})
			}
			if diff := cmp.Diff(tc.expectedTypes, got); diff != "" {
				t.Errorf("unexpected errors (-want +got):\n%s\nall errors: %v", diff, errs)
			}
		})
	}
}
