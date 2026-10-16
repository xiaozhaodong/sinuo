package modelrouting

import (
	"strings"
	"testing"
)

func TestSearchKey_ValidationAndKind(t *testing.T) {
	groups := []Group{{ID: 1}}
	a := Assignment{ID: 1, ModelName: SearchKey, GroupID: 1}
	if err := ValidateAssignment(a, MatchExact, nil, groups); err != nil {
		t.Fatal(err)
	}
	if !IsSearchKey(" SERVER_TOOL:SEARCH ") || BindingKind(SearchKey) != BindingKindSearch {
		t.Fatal("联网搜索保留键应归一化为 search 类型")
	}
	a.ID = 2
	if err := ValidateAssignment(a, MatchExact, []Assignment{{ID: 1, ModelName: SearchKey}}, groups); err == nil || !strings.Contains(err.Error(), "联网搜索映射已经存在，只能配置一条") {
		t.Fatalf("重复搜索映射应明确报错: %v", err)
	}
	for _, model := range []string{"server_tool:other", "SERVER_TOOL:SEARCH_EXTRA"} {
		if err := ValidateAssignment(Assignment{ModelName: model, GroupID: 1}, MatchExact, nil, groups); err == nil {
			t.Fatalf("未知保留键不能作为模型名: %s", model)
		}
	}
}
