package ojson

import (
	"encoding/json"
	"testing"
)

func TestPreservesOrderAndIndent(t *testing.T) {
	in := []byte("{\n    \"zeta\": 1,\n    \"alpha\": {\"b\": 2, \"a\": 1}\n}\n")
	o, err := Parse(in)
	if err != nil {
		t.Fatal(err)
	}
	o.Set("middle", json.RawMessage(`"m"`))
	o.Set("zeta", json.RawMessage(`9`))
	out, err := o.Format(DetectIndent(in))
	if err != nil {
		t.Fatal(err)
	}
	want := "{\n    \"zeta\": 9,\n    \"alpha\": {\n        \"b\": 2,\n        \"a\": 1\n    },\n    \"middle\": \"m\"\n}\n"
	if string(out) != want {
		t.Fatalf("got:\n%s\nwant:\n%s", out, want)
	}
	o.Delete("alpha")
	if len(o.Keys()) != 2 {
		t.Fatalf("keys = %v", o.Keys())
	}
}
