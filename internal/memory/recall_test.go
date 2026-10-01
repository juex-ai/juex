package memory

import (
	"strings"
	"testing"
	"time"

	mc "github.com/juex-ai/juex/internal/foundation/memoryclient"
)

func TestRecallSentenceMatchesUnsegmentedChineseAndPunctuation(t *testing.T) {
	s := NewState()
	s.Entries["docs"] = mc.Entry{ID: "docs", Name: "文档格式", Summary: "个人示例项目使用 Markdown 编写文档"}
	for _, query := range []string{"我个人示例项目的默认文档格式是什么？", "Which format? Markdown?"} {
		page, err := s.Search(mc.Query{Text: recallTerms(query), Limit: 8}, time.Now())
		if err != nil || len(page.Entries) != 1 {
			t.Fatal("sentence failed lexical recall", query, page, err)
		}
	}
	if recallTerms("？a！我") != "" || len(strings.Fields(recallTerms(strings.Repeat("文档格式", 3000)))) > 128 {
		t.Fatal("empty or repetitive query expansion")
	}
}
