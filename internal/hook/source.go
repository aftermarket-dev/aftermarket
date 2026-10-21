package hook

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/aftermarket-dev/aftermarket/internal/config"
	"github.com/aftermarket-dev/aftermarket/internal/ojson"
)

// Snapshot is what a source looked like at one check. It is kept in the
// state file.
type Snapshot struct {
	Exists  bool                `json:"exists"`
	Hash    string              `json:"hash,omitempty"`
	Size    int64               `json:"size,omitempty"`
	Status  int                 `json:"status,omitempty"`  // http status or command exit code
	Content string              `json:"content,omitempty"` // text kept for field and line changes
	Files   map[string]FileInfo `json:"files,omitempty"`   // folder
}

// FileInfo is one file in a folder snapshot.
type FileInfo struct {
	Size    int64  `json:"size"`
	ModTime int64  `json:"mtime"`
	Hash    string `json:"sha256"`
}

// Change is one difference in an event.
type Change struct {
	Op   string `json:"op"`             // added, removed or changed
	Path string `json:"path,omitempty"` // a field, a file name, "status" or "line"
	Old  any    `json:"old,omitempty"`
	New  any    `json:"new,omitempty"`
}

// errNotReady means the source is in the middle of changing; it is checked
// again next time, and nothing is reported now.
var errNotReady = errors.New("not ready")

func hashOf(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// look reads a source's current snapshot.
func (h *Hook) look(ctx context.Context, s *Source) (Snapshot, error) {
	switch s.Kind {
	case "file":
		return h.lookFile(s)
	case "folder":
		return h.lookFolder(s)
	case "http":
		return h.lookHTTP(ctx, s)
	case "command":
		return h.lookCommand(ctx, s)
	}
	return Snapshot{}, fmt.Errorf("unknown source kind %q", s.Kind)
}

// readSettled reads a file twice, settle apart, and only accepts it when
// both reads agree, so a file caught mid-write is never reported.
func (h *Hook) readSettled(path string) ([]byte, bool, error) {
	a, err := readCapped(path, h.cfg.MaxBody)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	h.sleep(h.cfg.Settle)
	b, err := readCapped(path, h.cfg.MaxBody)
	if err != nil || !bytes.Equal(a, b) {
		return nil, false, errNotReady
	}
	return a, true, nil
}

var errTooBig = errors.New("larger than max_body")

func readCapped(path string, max int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > max {
		return nil, errTooBig
	}
	return b, nil
}

func (h *Hook) lookFile(s *Source) (Snapshot, error) {
	data, exists, err := h.readSettled(s.Path)
	if err != nil || !exists {
		return Snapshot{}, err
	}
	snap := Snapshot{Exists: true, Hash: hashOf(data), Size: int64(len(data)), Content: string(data)}
	if s.Format != "" {
		// Structured content that doesn't read is half written, or broken;
		// either way it is not reported until it reads.
		if _, err := config.ParseTree(s.Format, data); err != nil {
			return Snapshot{}, errNotReady
		}
	}
	return snap, nil
}

func (h *Hook) lookFolder(s *Source) (Snapshot, error) {
	info, err := os.Stat(s.Path)
	if errors.Is(err, fs.ErrNotExist) {
		return Snapshot{}, nil
	}
	if err != nil {
		return Snapshot{}, err
	}
	if !info.IsDir() {
		return Snapshot{}, fmt.Errorf("%s is not a folder", s.Path)
	}
	var prev map[string]FileInfo
	if st := h.state.Sources[s.ID]; st != nil {
		prev = st.Snap.Files
	}
	snap := Snapshot{Exists: true, Files: map[string]FileInfo{}}
	stat := map[string]fs.FileInfo{}
	err = filepath.WalkDir(s.Path, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p != s.Path && !s.Recursive {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		if s.Pattern != "" {
			if ok, _ := filepath.Match(s.Pattern, d.Name()); !ok {
				return nil
			}
		}
		fi, err := d.Info()
		if err != nil {
			return nil // removed while walking: seen next time
		}
		rel, _ := filepath.Rel(s.Path, p)
		stat[filepath.ToSlash(rel)] = fi
		return nil
	})
	if err != nil {
		return Snapshot{}, err
	}
	changed := false
	for name, fi := range stat {
		old, had := prev[name]
		if had && old.Size == fi.Size() && old.ModTime == fi.ModTime().UnixNano() {
			snap.Files[name] = old
			continue
		}
		changed = true
		snap.Files[name] = FileInfo{Size: fi.Size(), ModTime: fi.ModTime().UnixNano()}
	}
	for name := range prev {
		if _, ok := snap.Files[name]; !ok {
			changed = true
		}
	}
	if !changed {
		snap.Hash = folderHash(snap.Files)
		return snap, nil
	}
	// Something is new or different: wait, and check that nothing is still
	// being written before hashing.
	h.sleep(h.cfg.Settle)
	for name, fi := range snap.Files {
		if fi.Hash != "" {
			continue
		}
		p := filepath.Join(s.Path, filepath.FromSlash(name))
		again, err := os.Stat(p)
		if err != nil || again.Size() != fi.Size || again.ModTime().UnixNano() != fi.ModTime {
			return Snapshot{}, errNotReady
		}
		f, err := os.Open(p)
		if err != nil {
			return Snapshot{}, errNotReady
		}
		sum := sha256.New()
		_, err = io.Copy(sum, f)
		f.Close()
		if err != nil {
			return Snapshot{}, errNotReady
		}
		fi.Hash = hex.EncodeToString(sum.Sum(nil))
		snap.Files[name] = fi
	}
	snap.Hash = folderHash(snap.Files)
	return snap, nil
}

func folderHash(files map[string]FileInfo) string {
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	sum := sha256.New()
	for _, n := range names {
		fmt.Fprintf(sum, "%s\x00%s\x00", n, files[n].Hash)
	}
	return hex.EncodeToString(sum.Sum(nil))
}

func (h *Hook) lookHTTP(ctx context.Context, s *Source) (Snapshot, error) {
	ctx, cancel := context.WithTimeout(ctx, s.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.URL.String(), nil)
	if err != nil {
		return Snapshot{}, err
	}
	for _, kv := range s.Headers {
		req.Header.Set(kv[0], kv[1])
	}
	resp, err := h.client.Do(req)
	if err != nil {
		return Snapshot{}, errors.New("could not get an answer")
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, h.cfg.MaxBody+1))
	if err != nil {
		return Snapshot{}, errors.New("the answer was cut off")
	}
	if int64(len(b)) > h.cfg.MaxBody {
		return Snapshot{}, errTooBig
	}
	return Snapshot{Exists: true, Status: resp.StatusCode, Hash: hashOf(b), Size: int64(len(b)), Content: string(b)}, nil
}

func (h *Hook) lookCommand(ctx context.Context, s *Source) (Snapshot, error) {
	ctx, cancel := context.WithTimeout(ctx, s.Timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, s.Run[0], s.Run[1:]...)
	cmd.Dir = s.Dir
	var out bytes.Buffer
	cmd.Stdout = &limitWriter{w: &out, n: h.cfg.MaxBody}
	err := cmd.Run()
	if ctx.Err() != nil {
		return Snapshot{}, errors.New("the command did not finish within its timeout")
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		// A failing command tells us nothing about the data; try again later.
		return Snapshot{}, fmt.Errorf("the command exited with status %d", exit.ExitCode())
	}
	if err != nil {
		return Snapshot{}, errors.New("the command could not be started")
	}
	if int64(out.Len()) > h.cfg.MaxBody {
		return Snapshot{}, errTooBig
	}
	b := out.Bytes()
	return Snapshot{Exists: true, Hash: hashOf(b), Size: int64(len(b)), Content: string(b)}, nil
}

type limitWriter struct {
	w io.Writer
	n int64
}

func (l *limitWriter) Write(p []byte) (int, error) {
	if l.n < 0 {
		return len(p), nil
	}
	if int64(len(p)) > l.n {
		// One byte past the limit, so the caller sees it was exceeded.
		l.w.Write(p[:l.n+1])
		l.n = -1
		return len(p), nil
	}
	l.n -= int64(len(p))
	return l.w.Write(p)
}

// diff lists the changes between two snapshots of s. No changes means the
// source is the same, by meaning where its format allows.
func diff(s *Source, a, b Snapshot) []Change {
	var out []Change
	switch {
	case !a.Exists && !b.Exists:
		return nil
	case !a.Exists:
		return []Change{{Op: "added", Path: "", New: summary(b)}}
	case !b.Exists:
		return []Change{{Op: "removed", Path: "", Old: summary(a)}}
	}
	if s.Kind == "folder" {
		names := map[string]bool{}
		for n := range a.Files {
			names[n] = true
		}
		for n := range b.Files {
			names[n] = true
		}
		sorted := make([]string, 0, len(names))
		for n := range names {
			sorted = append(sorted, n)
		}
		sort.Strings(sorted)
		for _, n := range sorted {
			fa, ina := a.Files[n]
			fb, inb := b.Files[n]
			switch {
			case !inb:
				out = append(out, Change{Op: "removed", Path: n, Old: fileSummary(fa)})
			case !ina:
				out = append(out, Change{Op: "added", Path: n, New: fileSummary(fb)})
			case fa.Hash != fb.Hash:
				out = append(out, Change{Op: "changed", Path: n, Old: fileSummary(fa), New: fileSummary(fb)})
			}
		}
		return out
	}
	if a.Status != b.Status {
		out = append(out, Change{Op: "changed", Path: "status", Old: a.Status, New: b.Status})
	}
	if a.Hash == b.Hash {
		return out
	}
	format := s.Format
	if s.Kind == "http" || s.Kind == "command" {
		format = config.JSON
	}
	if format != "" {
		ta, errA := config.ParseTree(format, []byte(a.Content))
		tb, errB := config.ParseTree(format, []byte(b.Content))
		if errA == nil && errB == nil {
			var fields []Change
			fieldDiff(nil, ta, tb, s.Ignore, &fields)
			return append(out, fields...)
		}
	}
	return append(out, lineDiff(a.Content, b.Content)...)
}

func summary(s Snapshot) any {
	if s.Files != nil {
		return map[string]any{"files": len(s.Files)}
	}
	return map[string]any{"size": s.Size, "sha256": s.Hash}
}

func fileSummary(f FileInfo) any {
	return map[string]any{"size": f.Size, "sha256": f.Hash}
}

const maxChanges = 100

func ignored(p ojson.Path, ignore []ojson.Path) bool {
	for _, ig := range ignore {
		if len(ig) != len(p) {
			continue
		}
		match := true
		for i := range ig {
			if ig[i] != "*" && ig[i] != p[i] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

// fieldDiff lists field changes between two trees, secrets redacted.
func fieldDiff(p ojson.Path, a, b any, ignore []ojson.Path, out *[]Change) {
	if len(*out) >= maxChanges || ignored(p, ignore) {
		return
	}
	ao, aok := a.(*ojson.Object)
	bo, bok := b.(*ojson.Object)
	if aok && bok {
		for _, k := range ao.Keys() {
			cp := append(append(ojson.Path(nil), p...), k)
			av, _ := ao.Get(k)
			if bv, ok := bo.Get(k); ok {
				fieldDiff(cp, av, bv, ignore, out)
			} else if !ignored(cp, ignore) {
				*out = append(*out, Change{Op: "removed", Path: cp.String(), Old: value(cp, av)})
			}
		}
		for _, k := range bo.Keys() {
			if _, ok := ao.Get(k); !ok {
				cp := append(append(ojson.Path(nil), p...), k)
				if !ignored(cp, ignore) {
					bv, _ := bo.Get(k)
					*out = append(*out, Change{Op: "added", Path: cp.String(), New: value(cp, bv)})
				}
			}
		}
		return
	}
	al, alok := a.([]any)
	bl, blok := b.([]any)
	if alok && blok {
		// Element by element, then what was added or removed at the end, so
		// a query result that gains a row says just that.
		for i := 0; i < len(al) || i < len(bl); i++ {
			cp := append(append(ojson.Path(nil), p...), fmt.Sprint(i))
			switch {
			case i >= len(bl):
				*out = append(*out, Change{Op: "removed", Path: cp.String(), Old: value(cp, al[i])})
			case i >= len(al):
				*out = append(*out, Change{Op: "added", Path: cp.String(), New: value(cp, bl[i])})
			default:
				fieldDiff(cp, al[i], bl[i], ignore, out)
			}
			if len(*out) >= maxChanges {
				return
			}
		}
		return
	}
	if config.Equal(a, b) {
		return
	}
	name := p.String()
	*out = append(*out, Change{Op: "changed", Path: name, Old: value(p, a), New: value(p, b)})
}

// value turns a tree value into what a webhook carries: plain JSON, with
// secrets and passwords in URLs replaced.
func value(p ojson.Path, v any) any {
	if config.SecretPath(p) {
		return "REDACTED"
	}
	enc, err := ojson.Encode(redactTree(p, v))
	if err != nil {
		return nil
	}
	var out any
	dec := json.NewDecoder(strings.NewReader(config.MaskURLs(string(enc))))
	dec.UseNumber()
	if dec.Decode(&out) != nil {
		return nil
	}
	return out
}

func redactTree(p ojson.Path, v any) any {
	switch t := v.(type) {
	case *ojson.Object:
		o := ojson.NewObject()
		for _, k := range t.Keys() {
			c, _ := t.Get(k)
			cp := append(append(ojson.Path(nil), p...), k)
			if config.SecretPath(cp) {
				o.Set(k, "REDACTED")
			} else {
				o.Set(k, redactTree(cp, c))
			}
		}
		return o
	case []any:
		out := make([]any, len(t))
		for i, it := range t {
			out[i] = redactTree(append(append(ojson.Path(nil), p...), fmt.Sprint(i)), it)
		}
		return out
	}
	return v
}

// lineDiff lists lines removed and added between two texts, as multisets,
// in the order they appear.
func lineDiff(a, b string) []Change {
	count := map[string]int{}
	al := splitLines(a)
	bl := splitLines(b)
	for _, l := range al {
		count[l]++
	}
	var added []string
	for _, l := range bl {
		if count[l] > 0 {
			count[l]--
		} else {
			added = append(added, l)
		}
	}
	var out []Change
	for _, l := range al {
		if count[l] > 0 {
			count[l]--
			out = append(out, Change{Op: "removed", Path: "line", Old: config.MaskURLs(l)})
		}
	}
	for _, l := range added {
		out = append(out, Change{Op: "added", Path: "line", New: config.MaskURLs(l)})
	}
	if len(out) > maxChanges {
		out = append(out[:maxChanges], Change{Op: "changed", Path: "line", New: fmt.Sprintf("and %d more lines", len(out)-maxChanges)})
	}
	if len(out) == 0 { // same lines, different order or line endings
		out = append(out, Change{Op: "changed", Path: "", Old: map[string]any{"size": len(a)}, New: map[string]any{"size": len(b)}})
	}
	return out
}

func splitLines(s string) []string {
	s = strings.TrimSuffix(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}
