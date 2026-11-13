package config

import (
	"reflect"
	"sort"
	"strings"
	"sync"
)

const redacted = "[REDACTED]"

var (
	secretType   = reflect.TypeOf(Secret{})
	redactorType = reflect.TypeOf(&Redactor{})
)

// Redactor replaces known secret values in text. It is the only place that
// turns Secrets back into raw strings for matching.
type Redactor struct {
	mu     sync.RWMutex
	values []string
}

// NewRedactor collects every Secret reachable through exported fields of the
// sources (structs, pointers, slices, maps, Secret values), so a Secret field
// added to a template is covered without touching any caller. A *Redactor
// source contributes its values.
func NewRedactor(sources ...any) *Redactor {
	r := &Redactor{}
	seen := map[string]bool{}
	var walk func(reflect.Value)
	walk = func(v reflect.Value) {
		switch v.Kind() {
		case reflect.Invalid:
		case reflect.Pointer, reflect.Interface:
			if v.Type() == redactorType {
				if o, _ := v.Interface().(*Redactor); o != nil {
					o.mu.RLock()
					defer o.mu.RUnlock()
					for _, raw := range o.values {
						if !seen[raw] {
							seen[raw] = true
							r.values = append(r.values, raw)
						}
					}
				}
				return
			}
			if !v.IsNil() {
				walk(v.Elem())
			}
		case reflect.Struct:
			if v.Type() == secretType {
				if raw := v.Interface().(Secret).value; raw != "" && !seen[raw] {
					seen[raw] = true
					r.values = append(r.values, raw)
				}
				return
			}
			for i := 0; i < v.NumField(); i++ {
				if v.Type().Field(i).IsExported() {
					walk(v.Field(i))
				}
			}
		case reflect.Slice, reflect.Array:
			for i := 0; i < v.Len(); i++ {
				walk(v.Index(i))
			}
		case reflect.Map:
			for it := v.MapRange(); it.Next(); {
				walk(it.Value())
			}
		}
	}
	for _, s := range sources {
		walk(reflect.ValueOf(s))
	}
	// Longest first, so a secret that contains another is replaced whole.
	sort.SliceStable(r.values, func(i, j int) bool { return len(r.values[i]) > len(r.values[j]) })
	return r
}

// Add makes the redactor cover one more secret, such as a refreshed token.
func (r *Redactor) Add(s Secret) {
	if r == nil || s.value == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, v := range r.values {
		if v == s.value {
			return
		}
	}
	r.values = append(r.values, s.value)
	sort.SliceStable(r.values, func(i, j int) bool { return len(r.values[i]) > len(r.values[j]) })
}

// String returns text with every known secret replaced. A nil Redactor
// returns text unchanged.
func (r *Redactor) String(text string) string {
	if r == nil {
		return text
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, raw := range r.values {
		text = strings.ReplaceAll(text, raw, redacted)
	}
	return text
}

// Contains reports whether text holds any known secret.
func (r *Redactor) Contains(text string) bool {
	if r == nil {
		return false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, raw := range r.values {
		if strings.Contains(text, raw) {
			return true
		}
	}
	return false
}
