package util

import (
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/runtime/schema"
)

// ParseGVKs parses a semicolon-separated list of GVK patterns into a slice of GroupVersionKind.
// Invalid entries are skipped. Returns an error if no valid GVKs are found.
func ParseGVKs(pattern string) ([]schema.GroupVersionKind, error) {
	var gvks []schema.GroupVersionKind
	for entry := range strings.SplitSeq(pattern, ";") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		parts := strings.Split(entry, "/")
		var group string = ""
		var version string = ""
		var kind string = ""
		if len(parts) == 2 {
			version = parts[0]
			kind = parts[1]
		} else if len(parts) == 3 {
			group = parts[0]
			version = parts[1]
			kind = parts[2]
		}
		// Invalid patterns are skipped
		if version == "" || kind == "" {
			continue
		}
		gvk := schema.GroupVersionKind{Group: group, Version: version, Kind: kind}
		gvks = append(gvks, gvk)
	}
	if len(gvks) == 0 {
		return nil, fmt.Errorf("no valid watched GVKs specified")
	}
	return gvks, nil
}
