package patch

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
)

// Server-authored rows appended to master-data tables. The shipped default is
// embedded so a plain run needs no extra files; pass a path to override it.
//
//go:embed custom_content.json
var defaultCustomContent []byte

type CustomContent struct {
	AppendRows map[string][][]any `json:"appendRows"`
}

// DefaultCustomContent returns the embedded content definition.
func DefaultCustomContent() (CustomContent, error) { return ParseCustomContent(defaultCustomContent) }

// ParseCustomContent decodes a content definition, normalising JSON numbers to
// int64 so appended rows encode as msgpack integers rather than floats.
func ParseCustomContent(raw []byte) (CustomContent, error) {
	var cc CustomContent
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&cc); err != nil {
		return CustomContent{}, fmt.Errorf("parse custom content: %w", err)
	}
	for table, rows := range cc.AppendRows {
		for i, row := range rows {
			for j, cell := range row {
				v, err := normaliseJSONNumber(cell)
				if err != nil {
					return CustomContent{}, fmt.Errorf("%s row %d col %d: %w", table, i, j, err)
				}
				rows[i][j] = v
			}
		}
	}
	return cc, nil
}

// rowsFor returns the rows to append to a table, or nil when none are defined.
func (c CustomContent) rowsFor(table string) []any {
	rows, ok := c.AppendRows[table]
	if !ok {
		return nil
	}
	out := make([]any, 0, len(rows))
	for _, r := range rows {
		cells := make([]any, len(r))
		copy(cells, r)
		out = append(out, cells)
	}
	return out
}

func normaliseJSONNumber(v any) (any, error) {
	n, ok := v.(json.Number)
	if !ok {
		return v, nil // string, bool, null — pass through
	}
	if i, err := n.Int64(); err == nil {
		return i, nil
	}
	f, err := n.Float64()
	if err != nil {
		return nil, fmt.Errorf("bad number %q", n.String())
	}
	return f, nil
}
