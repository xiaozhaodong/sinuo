package configimport

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"gopkg.in/yaml.v3"
)

var errYAMLStructure = errors.New("YAML 包含不支持的结构、映射键或 merge；该文件未提取")

func (p *parser) parseYAML(content string) error {
	d := yaml.NewDecoder(strings.NewReader(content))
	for doc := 0; ; doc++ {
		var node yaml.Node
		if err := d.Decode(&node); err == io.EOF {
			return nil
		} else if err != nil {
			// yaml.v3 的错误可能带原始内容，不能透传。
			return errors.New("YAML 语法错误；该文件未提取，请检查缩进、字段格式和别名引用")
		}
		// 先检查全部节点，覆盖被 merge 覆盖的分支；不把别名展开为新的对象树。
		// 验证和提取共用预算，每次引用访问都计数，防止少量文本指数放大。
		anchors := make(map[*yaml.Node]bool)
		if err := p.yamlAnchors(&node, 0, anchors); err != nil {
			return err
		}
		if err := p.validateYAML(&node, 0, make(map[*yaml.Node]bool), anchors); err != nil {
			return err
		}
		if err := p.yamlValue(&node, fmt.Sprintf("文档%d", doc+1), "", 0); err != nil {
			return err
		}
	}
}

// yaml.v3 的流解码器会保留前一文档的锚点；按本文件节点归属限制引用作用域。
// 这里只沿原始 Content 收集，不能沿 Alias 把前一文档节点带入集合。
func (p *parser) yamlAnchors(n *yaml.Node, depth int, anchors map[*yaml.Node]bool) error {
	if err := p.step(depth); err != nil {
		return err
	}
	if n.Anchor != "" {
		anchors[n] = true
	}
	for _, c := range n.Content {
		if err := p.yamlAnchors(c, depth+1, anchors); err != nil {
			return err
		}
	}
	return nil
}

func (p *parser) validateYAML(n *yaml.Node, depth int, active, anchors map[*yaml.Node]bool) error {
	if err := p.step(depth); err != nil {
		return err
	}
	if n == nil {
		return errYAMLStructure
	}
	if active[n] {
		return errors.New("YAML 包含循环别名或 merge 引用；该文件未提取")
	}
	active[n] = true
	defer delete(active, n)
	if !yamlStandardTag(n) {
		return errors.New("本版不处理 YAML 自定义标签或特殊集合标签；该文件未提取")
	}
	switch n.Kind {
	case yaml.AliasNode:
		if !anchors[n.Alias] {
			return errors.New("YAML 别名必须引用当前文档中的锚点；该文件未提取")
		}
		return p.validateYAML(n.Alias, depth+1, active, anchors)
	case yaml.MappingNode:
		if len(n.Content)%2 != 0 {
			return errYAMLStructure
		}
		mergeSeen := false
		for i := 0; i < len(n.Content); i += 2 {
			if err := p.validateYAML(n.Content[i], depth+1, active, anchors); err != nil {
				return err
			}
			_, merge, err := p.yamlKey(n.Content[i], depth+1)
			if err != nil {
				return err
			}
			if merge {
				if mergeSeen {
					return errors.New("YAML 含重复 merge 键；请使用单个 merge 列表，该文件未提取")
				}
				mergeSeen = true
				if err := p.yamlMergeShape(n.Content[i+1], depth+1, true); err != nil {
					return err
				}
			}
			if err := p.validateYAML(n.Content[i+1], depth+1, active, anchors); err != nil {
				return err
			}
		}
	case yaml.DocumentNode, yaml.SequenceNode:
		for _, c := range n.Content {
			if err := p.validateYAML(c, depth+1, active, anchors); err != nil {
				return err
			}
		}
	case yaml.ScalarNode:
	default:
		return errYAMLStructure
	}
	return nil
}

func yamlStandardTag(n *yaml.Node) bool {
	switch n.Kind {
	case yaml.DocumentNode, yaml.AliasNode:
		return n.Tag == ""
	case yaml.MappingNode:
		return n.Tag == "!!map"
	case yaml.SequenceNode:
		return n.Tag == "!!seq"
	case yaml.ScalarNode:
		switch n.Tag {
		case "!!str", "!!bool", "!!int", "!!float", "!!null", "!!timestamp", "!!binary", "!!merge":
			return true
		}
	}
	return false
}

// 字符串键可以带锚点或经别名引用；引号内的 "<<" 是普通键。
func (p *parser) yamlKey(n *yaml.Node, depth int) (string, bool, error) {
	for {
		if err := p.step(depth); err != nil {
			return "", false, err
		}
		if n == nil {
			return "", false, errYAMLStructure
		}
		if n.Kind != yaml.AliasNode {
			break
		}
		n, depth = n.Alias, depth+1
	}
	if n.Kind == yaml.ScalarNode {
		if n.Tag == "!!str" {
			return n.Value, false, nil
		}
		if n.Tag == "!!merge" && n.Value == "<<" {
			return "", true, nil
		}
	}
	return "", false, errYAMLStructure
}

func (p *parser) yamlMergeShape(n *yaml.Node, depth int, allowSequence bool) error {
	if err := p.step(depth); err != nil {
		return err
	}
	if n == nil {
		return errYAMLStructure
	}
	switch n.Kind {
	case yaml.AliasNode:
		return p.yamlMergeShape(n.Alias, depth+1, allowSequence)
	case yaml.MappingNode:
		return nil
	case yaml.SequenceNode:
		if allowSequence {
			for _, c := range n.Content {
				if err := p.yamlMergeShape(c, depth+1, false); err != nil {
					return err
				}
			}
			return nil
		}
	}
	return errYAMLStructure
}

func (p *parser) yamlValue(n *yaml.Node, location, key string, depth int) error {
	if err := p.step(depth); err != nil {
		return err
	}
	switch n.Kind {
	case yaml.AliasNode:
		return p.yamlValue(n.Alias, location, key, depth+1)
	case yaml.DocumentNode, yaml.SequenceNode:
		for i, c := range n.Content {
			childPath := location
			if n.Kind == yaml.SequenceNode {
				childPath = boundedFieldPath(fmt.Sprintf("%s[%d]", location, i))
			}
			if err := p.yamlValue(c, childPath, key, depth+1); err != nil {
				return err
			}
		}
	case yaml.MappingNode:
		return p.yamlFields(n, depth, func(k string, value *yaml.Node, childDepth int) error {
			return p.yamlValue(value, fieldPath(location, k), k, childDepth)
		})
	case yaml.ScalarNode:
		return p.value(location, key, n.Value, n.Tag == "!!str", depth)
	}
	return nil
}

// 只枚举有效字段及源节点，不复制值或生成展开后的 map。
// 显式键覆盖 merge；merge 列表中靠前的映射优先：https://yaml.org/type/merge.html
func (p *parser) yamlFields(n *yaml.Node, depth int, visit func(string, *yaml.Node, int) error) error {
	if err := p.step(depth); err != nil {
		return err
	}
	if n.Kind == yaml.AliasNode {
		return p.yamlFields(n.Alias, depth+1, visit)
	}
	if n.Kind == yaml.SequenceNode {
		for _, c := range n.Content {
			if err := p.yamlFields(c, depth+1, visit); err != nil {
				return err
			}
		}
		return nil
	}
	seen := make(map[string]bool)
	var merge *yaml.Node
	for i := 0; i < len(n.Content); i += 2 {
		k, isMerge, err := p.yamlKey(n.Content[i], depth+1)
		if err != nil {
			return err
		}
		if isMerge {
			merge = n.Content[i+1]
			continue
		}
		seen[k] = true
		// 保留同一映射中重复显式字段的全部实际值，与 JSON 导入一致。
		if err := visit(k, n.Content[i+1], depth+1); err != nil {
			return err
		}
	}
	if merge != nil {
		return p.yamlFields(merge, depth+1, func(k string, value *yaml.Node, childDepth int) error {
			if seen[k] {
				return nil
			}
			seen[k] = true
			return visit(k, value, childDepth)
		})
	}
	return nil
}
