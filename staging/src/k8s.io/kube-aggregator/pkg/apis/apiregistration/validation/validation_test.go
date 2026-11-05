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

package validation

import (
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/kube-aggregator/pkg/apis/apiregistration"
)

func TestValidateAPIServiceUpdate(t *testing.T) {
	oldObj := &apiregistration.APIService{
		ObjectMeta: metav1.ObjectMeta{Name: "v1.example.com", ResourceVersion: "1"},
		Spec: apiregistration.APIServiceSpec{
			Group:                "example.com",
			Version:              "v1",
			GroupPriorityMinimum: 1000,
			VersionPriority:      15,
		},
	}
	type errSummary struct {
		Type  field.ErrorType
		Field string
	}
	tests := []struct {
		name     string
		mutate   func(*apiregistration.APIService)
		expected []errSummary
	}{
		{
			name:   "valid update",
			mutate: func(a *apiregistration.APIService) { a.Labels = map[string]string{"a": "b"} },
		},
		{
			name:     "invalid label is reported once",
			mutate:   func(a *apiregistration.APIService) { a.Labels = map[string]string{"invalid@key": "v"} },
			expected: []errSummary{{field.ErrorTypeInvalid, "metadata.labels"}},
		},
		{
			name:     "invalid annotation is reported once",
			mutate:   func(a *apiregistration.APIService) { a.Annotations = map[string]string{"invalid@key": "v"} },
			expected: []errSummary{{field.ErrorTypeInvalid, "metadata.annotations"}},
		},
		{
			name:     "invalid finalizer",
			mutate:   func(a *apiregistration.APIService) { a.Finalizers = []string{"invalid@finalizer"} },
			expected: []errSummary{{field.ErrorTypeInvalid, "metadata.finalizers"}},
		},
		{
			name:     "name must keep matching the updated version and group",
			mutate:   func(a *apiregistration.APIService) { a.Spec.Version = "v2" },
			expected: []errSummary{{field.ErrorTypeInvalid, "metadata.name"}},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			obj := oldObj.DeepCopy()
			tc.mutate(obj)
			errs := ValidateAPIServiceUpdate(context.Background(), obj, oldObj)
			var got []errSummary
			for _, err := range errs {
				got = append(got, errSummary{err.Type, err.Field})
			}
			if diff := cmp.Diff(tc.expected, got); diff != "" {
				t.Errorf("unexpected errors (-want +got):\n%s\nall errors: %v", diff, errs)
			}
		})
	}
}
