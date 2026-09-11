package graph

import "testing"

func TestPythonGenericReceiverAnnotations(t *testing.T) {
	for ann, want := range map[string]string{
		"types.ParamType[t.Any]":      "ParamType",
		"Store[list[other.Value]]":    "Store",
		"Store[int] | None":           "Store",
		"type[Store[int]]":            "class:Store",
		"typing.Optional[Store[int]]": "Store",
		"typing.Union[A, B]":          "",
		"A | B":                       "",
		"list[A]":                     "",
		"tuple[A, B]":                 "",
	} {
		if got := pyBareType(ann); got != want {
			t.Errorf("%s: got %q want %q", ann, got, want)
		}
	}
}
