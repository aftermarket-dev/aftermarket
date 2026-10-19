package yamlite

import (
	"strings"
	"testing"
)

// dump renders a node compactly so tests can compare structure.
func dump(n *Node) string {
	if n == nil {
		return "<nil>"
	}
	switch n.Kind {
	case Null:
		return "~"
	case Scalar:
		if n.Quoted {
			return "'" + n.Value + "'"
		}
		return n.Value
	case Seq:
		var parts []string
		for _, it := range n.Items {
			parts = append(parts, dump(it))
		}
		return "[" + strings.Join(parts, ",") + "]"
	case Map:
		var parts []string
		for _, p := range n.Pairs {
			parts = append(parts, p.Key+"="+dump(p.Value))
		}
		return "{" + strings.Join(parts, ",") + "}"
	}
	return "?"
}

func TestParseValid(t *testing.T) {
	cases := []struct {
		name, src, want string
	}{
		{"empty", "", "~"},
		{"comment only", "# hi\n\n", "~"},
		{"flat map", "a: 1\nb: two\n", "{a=1,b=two}"},
		{"nested map", "a:\n  b: 1\n  c:\n    d: x\n", "{a={b=1,c={d=x}}}"},
		{"seq", "- a\n- b\n", "[a,b]"},
		{"seq under key same indent", "k:\n- a\n- b\nz: 1\n", "{k=[a,b],z=1}"},
		{"seq under key indented", "k:\n  - a\n  - b\n", "{k=[a,b]}"},
		{"seq of maps", "rules:\n  - id: x\n    when:\n      method: GET\n  - id: y\n", "{rules=[{id=x,when={method=GET}},{id=y}]}"},
		{"nested seq", "- - a\n  - b\n- c\n", "[[a,b],c]"},
		{"empty item", "-\n- a\n", "[~,a]"},
		{"item block", "-\n  a: 1\n", "[{a=1}]"},
		{"double quoted", `a: "x: y # not comment"`, "{a='x: y # not comment'}"},
		{"single quoted", `a: 'it''s'`, "{a='it's'}"},
		{"escapes", `a: "line\nnext \u00e9"`, "{a='line\nnext é'}"},
		{"comments", "a: 1 # one\n# skip\nb: x#y\n", "{a=1,b=x#y}"},
		{"null forms", "a: ~\nb: null\nc:\n", "{a=~,b=~,c=~}"},
		{"flow seq", "a: [GET, \"HEAD\", 3]\n", "{a=[GET,'HEAD',3]}"},
		{"flow map", "a: {x: 1, \"y z\": [a, b], w: {}}\n", "{a={x=1,y z=[a,b],w={}}}"},
		{"flow multiline", "a: {x: 1,\n  y: 2}\nb: 3\n", "{a={x=1,y=2},b=3}"},
		{"literal", "body: |\n  {\"a\": 1}\n  line2\nnext: 1\n", "{body='{\"a\": 1}\nline2\n',next=1}"},
		{"literal strip", "body: |-\n  x\n\nnext: 1\n", "{body='x',next=1}"},
		{"literal keep indent", "body: |\n  a\n    b\n", "{body='a\n  b\n'}"},
		{"literal blank inside", "body: |\n  a\n\n  b\n", "{body='a\n\nb\n'}"},
		{"folded", "t: >\n  a\n  b\n\n  c\n", "{t='a b\nc\n'}"},
		{"doc marker", "---\na: 1\n", "{a=1}"},
		{"quoted key", "\"a b\": 1\n'c': 2\n", "{a b=1,c=2}"},
		{"url value", "u: http://x.example/a#frag\n", "{u=http://x.example/a#frag}"},
		{"colon no space", "a: b:c\n", "{a=b:c}"},
		{"crlf", "a: 1\r\nb: 2\r\n", "{a=1,b=2}"},
		{"seq item map then key", "- a: 1\n  b: 2\n- c: 3\n", "[{a=1,b=2},{c=3}]"},
		{"empty flow", "a: []\nb: {}\n", "{a=[],b={}}"},
		{"literal with hash lines", "body: |\n  # Title\n  text # not a comment\n  #!/bin/sh\nnext: 1\n", "{body='# Title\ntext # not a comment\n#!/bin/sh\n',next=1}"},
		{"literal first line hash", "b: |-\n  #x\n", "{b='#x'}"},
		{"comment after literal", "b: |\n  x\n# done\nc: 1\n", "{b='x\n',c=1}"},
		{"folded with hash", "t: >\n  # heading\n  more\n", "{t='# heading more\n'}"},
		{"surrogate pair", "a: \"\\ud83d\\ude00\"\n", "{a='😀'}"},
		{"dash quote is plain", "v: foo-'bar # c'\n", "{v=foo-'bar}"},
		{"colon quote is plain", "v: a:'b'\n", "{v=a:'b'}"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			n, err := Parse([]byte(c.src))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if got := dump(n); got != c.want {
				t.Fatalf("got  %s\nwant %s", got, c.want)
			}
		})
	}
}

func TestParseErrors(t *testing.T) {
	cases := []struct {
		name, src, wantErr string
	}{
		{"tab indent", "a:\n\tb: 1\n", "line 2: tabs"},
		{"anchor", "a: &x 1\n", "line 1: anchors"},
		{"alias", "a: *x\n", "aliases"},
		{"tag", "a: !!str 1\n", "tags"},
		{"merge", "<<: {a: 1}\n", "merge keys"},
		{"complex key", "? a\n", "complex keys"},
		{"two docs", "a: 1\n---\nb: 2\n", "line 2: multiple YAML documents"},
		{"dup key", "a: 1\na: 2\n", "line 2: duplicate key"},
		{"bad indent", "a: 1\n  b: 2\n", "line 2: unexpected indentation"},
		{"not a key", "a: 1\njust text\n", "line 2: expected key: value"},
		{"unclosed quote", "a: \"x\n", "unclosed double quote"},
		{"unclosed flow", "a: [1, 2\n", "unclosed ["},
		{"inline nested map", "a: b: c\n", "nested mapping"},
		{"start indented", "  a: 1\n", "column 1"},
		{"list in map", "a: 1\n- b\n", "line 2: a list item cannot appear here"},
		{"bad escape", `a: "\q"`, "bad escape"},
		{"flow trailing", "a: [1] x\n", "unexpected text"},
		{"empty flow item", "a: [a, ,b]\n", "empty item"},
		{"leading empty item", "a: [,b]\n", "empty item"},
		{"lone surrogate", "a: \"\\ud800\"\n", "unpaired surrogate"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Parse([]byte(c.src))
			if err == nil {
				t.Fatalf("expected error containing %q", c.wantErr)
			}
			if !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("error %q does not contain %q", err, c.wantErr)
			}
		})
	}
}

func TestLineNumbers(t *testing.T) {
	n, err := Parse([]byte("# c\nrules:\n  - id: a\n    when:\n      path: /x\n"))
	if err != nil {
		t.Fatal(err)
	}
	r := n.Get("rules").Items[0]
	if r.Line != 3 {
		t.Fatalf("rule line = %d, want 3", r.Line)
	}
	if got := r.Get("when").Get("path").Line; got != 5 {
		t.Fatalf("path line = %d, want 5", got)
	}
}
