package privacy

import "sinuo/internal/codexidentity"

// Responses 回放的 arguments 是内嵌 JSON；先解码字符串叶子，再按两层各自的规则重编码。
func expandRestoreSegments(segments []textSegment, path string) ([]textSegment, func(map[int]string) map[int]string, *PolicyError) {
	identity := func(m map[int]string) map[int]string { return m }
	if path != "/v1/responses" && path != "/v1/responses/compact" {
		return segments, identity, nil
	}
	type nested struct {
		outer, first int
		children     []textSegment
	}
	var groups []nested
	var flat []textSegment
	var owners []int
	for i, seg := range segments {
		if seg.sealed || len(seg.Path) != 3 || seg.Path[0].Key != "input" || seg.Path[2].Key != "arguments" {
			flat = append(flat, seg)
			owners = append(owners, i)
			continue
		}
		data := []byte(seg.Value)
		if codexidentity.ValidateJSON(data) != nil {
			return nil, nil, restoreError("回放的工具参数不是完整 JSON")
		}
		children, err := collectJSONTextSegments(data, func([]pathElem) bool { return true })
		if err != nil {
			return nil, nil, restoreError("工具参数扫描失败")
		}
		groups = append(groups, nested{i, len(flat), children})
		flat = append(flat, children...)
		for range children {
			owners = append(owners, -1)
		}
	}
	if len(groups) == 0 {
		return segments, identity, nil
	}
	return flat, func(repl map[int]string) map[int]string {
		out := map[int]string{}
		for i, v := range repl {
			if owners[i] >= 0 {
				out[owners[i]] = v
			}
		}
		for _, group := range groups {
			changes := map[int]string{}
			for j := range group.children {
				if v, ok := repl[group.first+j]; ok {
					changes[j] = v
				}
			}
			if len(changes) > 0 {
				out[group.outer] = string(replaceSegments([]byte(segments[group.outer].Value), group.children, changes))
			}
		}
		return out
	}, nil
}
