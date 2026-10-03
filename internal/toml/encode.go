package toml

import (
	"sort"
	"strings"
)

// Marshal renders a value as a TOML document. Keys are emitted in a stable
// order (scalars first, then sub-tables) so that writing a config file twice
// produces byte-identical output.
func Marshal(root *Value) []byte {
	var b strings.Builder
	if root == nil || root.Kind != KindTable {
		return nil
	}
	writeTable(&b, nil, root)
	return []byte(b.String())
}

func writeTable(b *strings.Builder, path []string, t *Value) {
	if t == nil || t.Kind != KindTable {
		return
	}
	keys := t.Keys()
	var scalars, tables, arrays []string
	for _, k := range keys {
		v := t.Table[k]
		switch {
		case v.Kind == KindTable:
			tables = append(tables, k)
		case v.Kind == KindArray && len(v.Array) > 0 && v.Array[0] != nil && v.Array[0].Kind == KindTable:
			arrays = append(arrays, k)
		default:
			scalars = append(scalars, k)
		}
	}

	if len(path) > 0 {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString("[" + encodePath(path) + "]\n")
	}
	sort.Strings(scalars)
	for _, k := range scalars {
		b.WriteString(encodeKey(k) + " = " + scalarString(t.Table[k]) + "\n")
	}
	for _, k := range arrays {
		arr := t.Table[k]
		sub := append(append([]string{}, path...), k)
		for _, elem := range arr.Array {
			if b.Len() > 0 {
				b.WriteString("\n")
			}
			b.WriteString("[[" + encodePath(sub) + "]]\n")
			writeTable(b, nil, elem)
		}
	}
	sort.Strings(tables)
	for _, k := range tables {
		writeTable(b, append(append([]string{}, path...), k), t.Table[k])
	}
}

// scalarString renders a value that must appear on a single line.
func scalarString(v *Value) string {
	if v == nil {
		return `""`
	}
	switch v.Kind {
	case KindArray:
		parts := make([]string, 0, len(v.Array))
		for _, it := range v.Array {
			parts = append(parts, scalarString(it))
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case KindTable:
		return v.String()
	default:
		return v.String()
	}
}

func encodePath(parts []string) string {
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		out = append(out, encodeKey(p))
	}
	return strings.Join(out, ".")
}

func encodeKey(k string) string {
	if k == "" {
		return `""`
	}
	for i := 0; i < len(k); i++ {
		if !isBareKeyChar(k[i]) {
			return quote(k)
		}
	}
	return k
}
