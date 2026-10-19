package ojson

import (
	"encoding/json"
	"testing"
)

func mustDecode(t *testing.T, s string) any {
	t.Helper()
	v, err := Decode([]byte(s))
	if err != nil {
		t.Fatalf("Decode(%s): %v", s, err)
	}
	return v
}

func enc(t *testing.T, v any) string {
	t.Helper()
	b, err := Encode(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestRoundTripKeepsOrderAndNumbers(t *testing.T) {
	in := `{"z":1,"a":{"y":1.50,"b":[1e3,true,null,"<&>"]},"m":-0.0,"big":12345678901234567890}`
	if got := enc(t, mustDecode(t, in)); got != in {
		t.Fatalf("round trip changed the document:\n got %s\nwant %s", got, in)
	}
}

func TestDecodeRejects(t *testing.T) {
	for _, s := range []string{``, `{`, `{"a":1}x`, `{"a":1} {"b":2}`, `[1,]`, `nope`} {
		if _, err := Decode([]byte(s)); err == nil {
			t.Errorf("Decode(%q) should fail", s)
		}
	}
}

func TestPathOps(t *testing.T) {
	doc := mustDecode(t, `{"customer":{"id":7,"tags":["a","b","c"]},"keep":1}`)

	p, _ := ParsePath("customer.id")
	if v, ok := Get(doc, p); !ok || v.(json.Number) != "7" {
		t.Fatalf("Get customer.id = %v %v", v, ok)
	}
	p2, _ := ParsePath("customer.tags.1")
	if v, ok := Get(doc, p2); !ok || v != "b" {
		t.Fatalf("Get tags.1 = %v", v)
	}

	changed, err := Set(doc, Path{"customer_id"}, json.Number("7"))
	if err != nil || !changed {
		t.Fatalf("Set: %v %v", changed, err)
	}
	changed, _ = Set(doc, Path{"customer_id"}, json.Number("7"))
	if changed {
		t.Fatal("setting the same value should not report a change")
	}
	if _, err := Set(doc, Path{"new", "deep", "x"}, "v"); err != nil {
		t.Fatal(err)
	}
	if _, err := Set(doc, Path{"keep", "x"}, "v"); err == nil {
		t.Fatal("setting through a number should fail")
	}
	if !Delete(doc, p2) {
		t.Fatal("delete array element")
	}
	if Delete(doc, Path{"nope"}) {
		t.Fatal("deleting a missing key should report false")
	}
	want := `{"customer":{"id":7,"tags":["a","c"]},"keep":1,"customer_id":7,"new":{"deep":{"x":"v"}}}`
	if got := enc(t, doc); got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}

func TestRename(t *testing.T) {
	doc := mustDecode(t, `{"a":1,"b":{"c":2},"d":3}`)
	if _, err := Rename(doc, Path{"a"}, Path{"z"}); err != nil {
		t.Fatal(err)
	}
	if got := enc(t, doc); got != `{"z":1,"b":{"c":2},"d":3}` {
		t.Fatalf("in-place rename: %s", got)
	}
	if _, err := Rename(doc, Path{"b", "c"}, Path{"c"}); err != nil {
		t.Fatal(err)
	}
	if got := enc(t, doc); got != `{"z":1,"b":{},"d":3,"c":2}` {
		t.Fatalf("cross-object rename: %s", got)
	}
	if _, err := Rename(doc, Path{"z"}, Path{"d"}); err != nil {
		t.Fatal(err)
	}
	if got := enc(t, doc); got != `{"b":{},"d":1,"c":2}` {
		t.Fatalf("rename onto existing key: %s", got)
	}
	if _, err := Rename(doc, Path{"missing"}, Path{"x"}); err == nil {
		t.Fatal("renaming a missing key should fail")
	}
}

func TestParsePath(t *testing.T) {
	for _, bad := range []string{"", ".", "a..b", "a."} {
		if _, err := ParsePath(bad); err == nil {
			t.Errorf("ParsePath(%q) should fail", bad)
		}
	}
}

func TestCloneIsDeep(t *testing.T) {
	doc := mustDecode(t, `{"a":{"b":[1]}}`)
	c := Clone(doc)
	Set(c, Path{"a", "b", "0"}, json.Number("2"))
	if enc(t, doc) != `{"a":{"b":[1]}}` {
		t.Fatal("clone shares state with the original")
	}
}
