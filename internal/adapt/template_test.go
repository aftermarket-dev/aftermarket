package adapt

import (
	"strings"
	"testing"

	"github.com/aftermarket-dev/aftermarket/internal/ojson"
	"github.com/aftermarket-dev/aftermarket/internal/yamlite"
)

func compileT(t *testing.T, src string, params ...string) *Template {
	t.Helper()
	n, err := yamlite.Parse([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	ps := map[string]bool{}
	for _, p := range params {
		ps[p] = true
	}
	tpl, err := compileTemplate(n, ps)
	if err != nil {
		t.Fatal(err)
	}
	return tpl
}

func build(t *testing.T, tpl *Template, doc string, params map[string]string) (string, []string) {
	t.Helper()
	var d any
	if doc != "" {
		var err error
		if d, err = ojson.Decode([]byte(doc)); err != nil {
			t.Fatal(err)
		}
	}
	v, missing, err := tpl.Build(d, params)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	b, err := ojson.Encode(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b), missing
}

func TestTemplateObject(t *testing.T) {
	tpl := compileT(t, `
customer_id: $.customer.id
name: $.customer.full_name
first_tag: $.tags.0
id_from_url: $params.id
kind: legacy
count: 3
quoted: "3"
flag: true
none: null
dollar: $$5.00
"$$key": x
whole: $.customer
`, "id")
	got, missing := build(t, tpl, `{"customer":{"id":7,"full_name":"Ada"},"tags":["a","b"]}`, map[string]string{"id": "7"})
	want := `{"customer_id":7,"name":"Ada","first_tag":"a","id_from_url":"7","kind":"legacy","count":3,"quoted":"3","flag":true,"none":null,"dollar":"$5.00","$key":"x","whole":{"id":7,"full_name":"Ada"}}`
	if got != want || len(missing) != 0 {
		t.Fatalf("got  %s\nwant %s\nmissing %v", got, want, missing)
	}
	if !tpl.usesBody() {
		t.Fatal("usesBody")
	}
}

func TestTemplateMissingFieldsAreLeftOut(t *testing.T) {
	tpl := compileT(t, "a: $.x\nb: $.y.z\nc: [$.x, $.nope]\n")
	got, missing := build(t, tpl, `{"x":1}`, nil)
	if got != `{"a":1,"c":[1,null]}` {
		t.Fatalf("got %s", got)
	}
	if strings.Join(missing, ",") != "y.z not found,nope not found" {
		t.Fatalf("missing = %v", missing)
	}
}

func TestTemplateEach(t *testing.T) {
	tpl := compileT(t, `
items:
  $each: $.data
  $item:
    id: $.id
    tags:
      $each: $.tags
      $item: {t: $}
total: $.count
`)
	got, missing := build(t, tpl, `{"data":[{"id":1,"tags":["x"]},{"id":2,"tags":[]},{"tags":["y","z"]}],"count":3}`, nil)
	want := `{"items":[{"id":1,"tags":[{"t":"x"}]},{"id":2,"tags":[]},{"tags":[{"t":"y"},{"t":"z"}]}],"total":3}`
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
	if strings.Join(missing, ",") != "id not found" {
		t.Fatalf("missing = %v", missing)
	}
	top := compileT(t, "$each: $\n$item: $.n\n")
	if got, _ := build(t, top, `[{"n":1},{"n":2}]`, nil); got != "[1,2]" {
		t.Fatalf("top-level each = %s", got)
	}
}

func TestTemplateCannotBeBuilt(t *testing.T) {
	cases := map[string]string{
		"$each: $.data\n$item: 1\n": `{"data":{}}`,
		"$each: $.nope\n$item: 1\n": `{}`,
		"$each: $\n$item: 1\n":      `{"a":1}`,
	}
	for src, doc := range cases {
		tpl := compileT(t, src)
		d, _ := ojson.Decode([]byte(doc))
		if _, _, err := tpl.Build(d, nil); err == nil {
			t.Errorf("%q on %s should fail", src, doc)
		}
	}
	// A template that is just a reference fails when the value is missing.
	n, _ := yamlite.Parse([]byte("v: $.missing\n"))
	tpl, err := compileTemplate(n.Get("v"), nil)
	if err != nil {
		t.Fatal(err)
	}
	d, _ := ojson.Decode([]byte(`{}`))
	if _, _, err := tpl.Build(d, nil); err == nil {
		t.Error("a missing top-level value should fail")
	}
}

func TestTemplateWithoutBody(t *testing.T) {
	tpl := compileT(t, "deleted: true\nid: $params.id\n", "id")
	if tpl.usesBody() {
		t.Fatal("usesBody should be false")
	}
	if got, _ := build(t, tpl, "", map[string]string{"id": "9"}); got != `{"deleted":true,"id":"9"}` {
		t.Fatalf("got %s", got)
	}
}

func TestMissingListIsDeduplicated(t *testing.T) {
	var doc strings.Builder
	doc.WriteString(`{"d":[`)
	for i := 0; i < 1000; i++ {
		if i > 0 {
			doc.WriteString(",")
		}
		doc.WriteString(`{}`)
	}
	doc.WriteString(`]}`)
	tpl := compileT(t, "$each: $.d\n$item: {a: $.a, b: $.b}\n")
	_, missing := build(t, tpl, doc.String(), nil)
	if len(missing) != 2 {
		t.Fatalf("missing = %v", missing)
	}
}
