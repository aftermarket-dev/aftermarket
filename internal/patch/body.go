package patch

import (
	"net/http"

	"github.com/aftermarket-dev/aftermarket/internal/ojson"
	"github.com/aftermarket-dev/aftermarket/internal/rules"
)

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
