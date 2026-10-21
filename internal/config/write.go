package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// ErrSameFile is returned when the output would overwrite the base.
var ErrSameFile = errors.New("the output is the vendor's file itself; AfterConfig never writes the base, so choose another --out")

// WriteOut writes data to out in one step: a temporary file in the same
// folder, then a rename. The base is never written, and if anything fails
// the old output stays as it was. The output gets the base's permissions.
func WriteOut(base, out string, data []byte) error {
	if out == "" {
		return errors.New("no output file")
	}
	if sameFile(base, out) {
		return ErrSameFile
	}
	mode := os.FileMode(0o644)
	if fi, err := os.Stat(base); err == nil {
		mode = fi.Mode().Perm()
	}
	dir := filepath.Dir(out)
	tmp, err := os.CreateTemp(dir, ".aftermarket-config-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	ok := false
	defer func() {
		if !ok {
			tmp.Close()
			os.Remove(name)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(name, mode); err != nil {
		return err
	}
	if err := os.Rename(name, out); err != nil {
		return fmt.Errorf("could not replace %s: %w", out, err)
	}
	ok = true
	return nil
}

// sameFile reports whether a and b name the same file, through links and
// different spellings of the path.
func sameFile(a, b string) bool {
	if fa, err := os.Stat(a); err == nil {
		if fb, err := os.Stat(b); err == nil && os.SameFile(fa, fb) {
			return true
		}
	}
	ra, err1 := resolve(a)
	rb, err2 := resolve(b)
	return err1 == nil && err2 == nil && ra == rb
}

// resolve makes a path absolute and resolves links in its folder, so a file
// that doesn't exist yet can still be compared.
func resolve(p string) (string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	if r, err := filepath.EvalSymlinks(abs); err == nil {
		return r, nil
	}
	dir, err := filepath.EvalSymlinks(filepath.Dir(abs))
	if err != nil {
		return abs, nil
	}
	return filepath.Join(dir, filepath.Base(abs)), nil
}
