package server

import (
	"reflect"
	"testing"

	"gantry/internal/kube"
)

func TestProblemsOf(t *testing.T) {
	cases := []struct {
		msg    string
		causes []kube.Cause
		want   []yamlProblem
	}{
		{"failed to create typed patch object (default/x; /v1, Kind=ConfigMap): .foo: field not declared in schema", nil,
			[]yamlProblem{{Doc: 1, Field: ".foo", Message: "field not declared in schema"}}},
		{"failed to create typed patch object (default/y; apps/v1, Kind=Deployment): .spec.replicas: expected numeric (int or float), got string", nil,
			[]yamlProblem{{Doc: 1, Field: ".spec.replicas", Message: "expected numeric (int or float), got string"}}},
		{`Deployment.apps "y" is invalid: spec.template.spec.containers[0].image: Required value`,
			[]kube.Cause{{Reason: "FieldValueRequired", Message: "Required value", Field: "spec.template.spec.containers[0].image"}},
			[]yamlProblem{{Doc: 1, Field: "spec.template.spec.containers[0].image", Message: "Required value"}}},
		{`strict decoding error: unknown field "spec.replicaz", unknown field "spec.foo"`, nil,
			[]yamlProblem{{Doc: 1, Field: "spec.replicaz", Message: `unknown field "spec.replicaz"`}, {Doc: 1, Field: "spec.foo", Message: `unknown field "spec.foo"`}}},
		{"admission webhook denied", nil, []yamlProblem{{Doc: 1, Message: "admission webhook denied"}}},
	}
	for _, c := range cases {
		if got := problemsOf(1, c.msg, c.causes); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%q\n got %+v\nwant %+v", c.msg, got, c.want)
		}
	}
}
