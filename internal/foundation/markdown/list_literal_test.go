package markdown

import (
	"slices"
	"strings"
	"testing"
)

func TestListLiteralBlocks(t *testing.T) {
	for _, tt := range []struct {
		name    string
		text    string
		literal []int
	}{
		{
			name: "nested checklists",
			text: "- Parent\n  - [ ] two spaces\n    - [ ] four spaces\n\t- [ ] tab\n      - [ ] deeper",
		},
		{
			name:    "code relative to list content",
			text:    "- Parent\n\n      - [ ] code\n    - [ ] child\n\n          - [ ] nested code",
			literal: []int{2, 5},
		},
		{
			name:    "ordered marker width and padding",
			text:    "10.  Parent\n     - [ ] child\n\n           - [ ] code\n1.\tParent\n\t- [ ] child",
			literal: []int{3},
		},
		{
			name:    "fenced examples in a list",
			text:    "- Parent\n    - Nested\n      ~~~markdown\n      - [ ] example\n      ~~~\n    - [ ] child",
			literal: []int{2, 3, 4},
		},
		{
			name:    "fence starts after marker",
			text:    "- ```markdown\n  - [ ] example\n  ```\n- [ ] child",
			literal: []int{0, 1, 2},
		},
		{
			name:    "leaving list ends unclosed fence",
			text:    "- ~~~\n  - [ ] example\n- [ ] child",
			literal: []int{0, 1},
		},
		{
			name:    "independent code after list",
			text:    "- Parent\n\nExample:\n\n    - [ ] code\n- [ ] child",
			literal: []int{4},
		},
		{
			name:    "code starts after marker",
			text:    "-     - [ ] code\n\n      - [ ] more code\n  - [ ] child",
			literal: []int{0, 2},
		},
		{
			name:    "thematic break is not a list",
			text:    "- - -\n\n    - [ ] code",
			literal: []int{2},
		},
		{
			name: "CRLF blank line retains container",
			text: "- Parent\r\n\r\n    - [ ] child\r",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var syntax ListLiteralBlocks
			for i, line := range strings.Split(tt.text, "\n") {
				if got, want := syntax.Literal(line), slices.Contains(tt.literal, i); got != want {
					t.Errorf("line %d %q: literal=%v, want %v", i, line, got, want)
				}
			}
		})
	}
}
