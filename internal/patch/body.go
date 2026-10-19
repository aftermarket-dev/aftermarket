package patch

import (
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"

	"github.com/aftermarket-dev/aftermarket/internal/ojson"
	"github.com/aftermarket-dev/aftermarket/internal/rules"
)

// isJSONType reports whether a Content-Type names JSON.
func isJSONType(ct string) bool {
	mt, _, err := mime.ParseMediaType(ct)
	if err != nil {
		mt = strings.ToLower(strings.TrimSpace(strings.SplitN(ct, ";", 2)[0]))
	}
	return mt == "application/json" || strings.HasSuffix(mt, "+json")
}

// bodyRead is the outcome of buffering a body that a rule wants to change.
type bodyRead struct {
	raw     []byte // bytes exactly as received (possibly compressed)
	plain   []byte // decoded bytes
	gzipped bool
	// rest is set when the body was larger than the limit: raw holds the
	// first part and rest the remainder, still unread.
	rest io.ReadCloser
}

// restore returns a body that yields exactly the original bytes.
func (b *bodyRead) restore() io.ReadCloser {
	if b.rest != nil {
		return &multiCloser{Reader: io.MultiReader(bytes.NewReader(b.raw), b.rest), c: b.rest}
	}
	return io.NopCloser(bytes.NewReader(b.raw))
}

type multiCloser struct {
	io.Reader
	c io.Closer
}

func (m *multiCloser) Close() error { return m.c.Close() }

var errTooLarge = errors.New("body is larger than max_body")

// readBody buffers up to max bytes of body. On errTooLarge the returned
// bodyRead can still restore the full original stream.
func readBody(body io.ReadCloser, max int64) (*bodyRead, error) {
	br := &bodyRead{}
	if body == nil || body == http.NoBody {
		return br, nil
	}
	buf, err := io.ReadAll(io.LimitReader(body, max+1))
	br.raw = buf
	if err != nil {
		br.rest = body
		return br, err
	}
	if int64(len(buf)) > max {
		br.rest = body
		return br, errTooLarge
	}
	_ = body.Close()
	return br, nil
}

// decode undoes Content-Encoding. Only identity and gzip are supported.
func (b *bodyRead) decode(encoding string, max int64) (string, bool) {
	enc := strings.ToLower(strings.TrimSpace(encoding))
	switch enc {
	case "", "identity":
		b.plain = b.raw
		return "", true
	case "gzip", "x-gzip":
		zr, err := gzip.NewReader(bytes.NewReader(b.raw))
		if err != nil {
			return "gzip body could not be read", false
		}
		plain, err := io.ReadAll(io.LimitReader(zr, max+1))
		if err != nil {
			return "gzip body could not be read", false
		}
		if int64(len(plain)) > max {
			return "decompressed body is larger than max_body", false
		}
		b.plain = plain
		b.gzipped = true
		return "", true
	}
	return "content encoding " + enc + " is not supported for JSON changes", false
}

// encodeBody re-applies the content encoding to new plain bytes.
func encodeBody(plain []byte, gzipped bool) []byte {
	if !gzipped {
		return plain
	}
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	_, _ = zw.Write(plain)
	_ = zw.Close()
	return buf.Bytes()
}

// opResult describes what happened to one rule's JSON operations.
type opResult struct {
	applied    []string
	notApplied []string
}

// applyJSON runs ops against doc in order.
func applyJSON(doc any, ops []rules.JSONOp) opResult {
	var res opResult
	for _, op := range ops {
		desc := op.Describe()
		switch op.Kind {
		case "rename", "copy":
			v, ok := ojson.Get(doc, op.From)
			if !ok {
				res.notApplied = append(res.notApplied, desc+" ("+op.From.String()+" not found)")
				continue
			}
			var changed bool
			var err error
			if op.Kind == "rename" {
				changed, err = ojson.Rename(doc, op.From, op.To)
			} else {
				changed, err = ojson.Set(doc, op.To, ojson.Clone(v))
			}
			if err != nil {
				res.notApplied = append(res.notApplied, desc+" ("+err.Error()+")")
				continue
			}
			if changed {
				res.applied = append(res.applied, desc)
			} else {
				res.notApplied = append(res.notApplied, desc+" (already in place)")
			}
		case "set":
			changed, err := ojson.Set(doc, op.To, ojson.Clone(op.Value))
			switch {
			case err != nil:
				res.notApplied = append(res.notApplied, desc+" ("+err.Error()+")")
			case changed:
				res.applied = append(res.applied, desc)
			default:
				res.notApplied = append(res.notApplied, desc+" (already in place)")
			}
		case "delete":
			if ojson.Delete(doc, op.From) {
				res.applied = append(res.applied, desc)
			} else {
				res.notApplied = append(res.notApplied, desc+" (not found)")
			}
		}
	}
	return res
}

// applyHeaders runs header actions and returns what changed.
func applyHeaders(h http.Header, a *rules.Actions) (applied, notApplied []string) {
	for _, name := range a.RemoveHeaders {
		if _, ok := h[name]; ok {
			h.Del(name)
			applied = append(applied, "remove header "+name)
		} else {
			notApplied = append(notApplied, "remove header "+name+" (not present)")
		}
	}
	for _, kv := range a.SetHeaders {
		if vals := h.Values(kv.Name); len(vals) == 1 && vals[0] == kv.Glob {
			notApplied = append(notApplied, "set header "+kv.Name+" (already in place)")
			continue
		}
		h.Set(kv.Name, kv.Glob)
		applied = append(applied, "set header "+kv.Name)
	}
	return applied, notApplied
}
