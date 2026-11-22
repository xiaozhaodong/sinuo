package inputcomposition

import (
	"regexp"
	"strings"

	"github.com/tidwall/gjson"
)

// 只识别原样返回静态命令结果的 exec 包装。脚本可以改写或合并输出，
// 在这种情况下保留工具名，不把脚本里的文件名误当成整个结果的来源。
const (
	jsLiteral = `(?:"(?:[^"\\\r\n]|\\["'\\/])*"|'(?:[^'\\\r\n]|\\["'\\/])*')`
	option    = `\s*(?:"?(?:workdir|shell)"?\s*:\s*` + jsLiteral +
		`|"?(?:max_output_tokens|yield_time_ms)"?\s*:\s*\d+|"?(?:tty|login)"?\s*:\s*(?:true|false))`
	call = `tools\.exec_command\(\s*\{\s*(?:` + option + `\s*,\s*)*(?:cmd|"cmd"|'cmd')\s*:\s*(` + jsLiteral +
		`)(?:\s*,` + option + `)*\s*,?\s*\}\s*\)`
)

var (
	directExec     = regexp.MustCompile(`^text\(\s*await\s+` + call + `\s*\)\s*;?$`)
	directOutput   = regexp.MustCompile(`^text\(\s*\(\s*await\s+` + call + `\s*\)\.output\s*\)\s*;?$`)
	assignedOutput = regexp.MustCompile(`^(?:const|let)\s+([a-zA-Z_]\w*)\s*=\s*await\s+` + call + `\s*;\s*text\(\s*([a-zA-Z_]\w*)(?:\.output)?\s*\)\s*;?$`)
	batchExec      = regexp.MustCompile(`^text\(\s*await\s+Promise\.all(?:Settled)?\(\s*\[\s*` +
		call + `(?:\s*,\s*` + call + `)+\s*,?\s*\]\s*\)\s*\)\s*;?$`)
	jsEscapes = strings.NewReplacer(`\\`, `\`, `\"`, `"`, `\'`, "'", `\/`, "/")
)

func shellCommand(args gjson.Result) string {
	if cmd := args.Get("cmd"); cmd.Type == gjson.String {
		return cmd.Str
	}
	return args.Get("command").Str
}

func execOutputLabel(name string, args gjson.Result) label {
	script := strings.TrimSpace(args.Str)
	if args.Type == gjson.String && strings.HasPrefix(script, "{") && gjson.Valid(script) {
		args = gjson.Parse(script)
	}
	if args.IsObject() {
		script = strings.TrimSpace(args.Get("code").Str)
		if script == "" {
			return commandLabel(name, shellCommand(args))
		}
	}
	for _, form := range []*regexp.Regexp{directExec, directOutput} {
		if m := form.FindStringSubmatch(script); m != nil {
			literal := m[1]
			return commandLabel(name, jsEscapes.Replace(literal[1:len(literal)-1]))
		}
	}
	if m := assignedOutput.FindStringSubmatch(script); m != nil && m[1] == m[3] && m[1] != "tools" && m[1] != "text" {
		literal := m[2]
		return commandLabel(name, jsEscapes.Replace(literal[1:len(literal)-1]))
	}
	if batchExec.MatchString(script) {
		return label{"results", "批量命令结果"}
	}
	return label{"results", name}
}

func commandLabel(tool, cmd string) label {
	if l, ok := readCommand(cmd); ok {
		return l
	}
	fallback := label{"results", tool}
	if len(cmd) > 1024 || strings.ContainsAny(cmd, ";&|`$<>*?\n") {
		return fallback
	}
	args := strings.Fields(cmd)
	if len(args) == 0 {
		return fallback
	}
	sub := ""
	if len(args) > 1 {
		sub = args[1]
	}
	var name string
	switch args[0] {
	case "ls":
		name = "文件列表"
	case "rg":
		if sub == "--files" {
			name = "文件列表"
		} else {
			name = "文本搜索结果"
		}
	case "grep":
		name = "文本搜索结果"
	case "git":
		switch sub {
		case "diff", "show":
			name = "Git 差异与提交内容"
		case "status":
			name = "Git 工作区状态"
		case "log":
			name = "Git 提交历史"
		}
	case "go":
		switch sub {
		case "test":
			name = "测试输出"
		case "build":
			name = "构建输出"
		}
	case "pytest":
		name = "测试输出"
	case "npm", "pnpm", "yarn":
		switch sub {
		case "test":
			name = "测试输出"
		case "run":
			if len(args) > 2 && args[2] == "build" {
				name = "构建输出"
			}
		}
	}
	if name == "" {
		return fallback
	}
	return label{"results", name}
}

func shellWrapper(shell, flag string) bool {
	switch shell {
	case "bash", "sh", "zsh", "/bin/bash", "/bin/sh", "/bin/zsh":
		return flag == "-c" || flag == "-lc"
	}
	return false
}

// 只接受静态读取和一个可选 cd 前缀。多个文件合并输出时保留一个条目。
func readCommand(cmd string) (label, bool) {
	var none label
	if len(cmd) > 1024 || strings.ContainsAny(cmd, ";`$<>*?\n|") {
		return none, false
	}
	cmd = strings.TrimSpace(cmd)
	if shell, rest, ok := strings.Cut(cmd, " "); ok {
		flag, literal, _ := strings.Cut(strings.TrimSpace(rest), " ")
		if shellWrapper(shell, flag) {
			literal = strings.TrimSpace(literal)
			if len(literal) < 2 || literal[0] != literal[len(literal)-1] ||
				(literal[0] != '\'' && literal[0] != '"') || strings.IndexByte(literal[1:len(literal)-1], literal[0]) >= 0 {
				return none, false
			}
			cmd = strings.TrimSpace(literal[1 : len(literal)-1])
		}
	}
	if dir, rest, ok := strings.Cut(cmd, "&&"); ok {
		words, valid := readWords(dir)
		if !valid || len(words) != 2 || words[0] != "cd" || strings.HasPrefix(words[1], "-") {
			return none, false
		}
		cmd = rest
	}
	args, valid := readWords(cmd)
	if !valid || len(args) < 2 {
		return none, false
	}
	files := args[1:]
	switch args[0] {
	case "cat", "bat", "nl":
		if args[0] == "cat" && files[0] == "-n" || args[0] == "bat" && files[0] == "--plain" ||
			args[0] == "nl" && files[0] == "-ba" {
			files = files[1:]
		}
	case "head", "tail":
		if len(files) >= 3 && (files[0] == "-n" || files[0] == "-c") && digits(files[1]) {
			files = files[2:]
		}
	case "sed":
		if len(files) < 3 || files[0] != "-n" {
			return none, false
		}
		rangeExpr, ok := strings.CutSuffix(files[1], "p")
		lo, hi, pair := strings.Cut(rangeExpr, ",")
		if !ok || !digits(lo) || pair && !digits(hi) {
			return none, false
		}
		files = files[2:]
	default:
		return none, false
	}
	if len(files) > 0 && files[0] == "--" {
		files = files[1:]
	}
	for _, f := range files {
		if f == "" || strings.HasPrefix(f, "-") {
			return none, false
		}
	}
	switch len(files) {
	case 0:
		return none, false
	case 1:
		return fileLabel(files[0]), true
	default:
		return label{"files", "多文件读取"}, true
	}
}

// 支持整词引号；遇到展开、转义或引号拼接就放弃推断。
func readWords(cmd string) ([]string, bool) {
	if strings.ContainsAny(cmd, "&\\(){}[]#!~") {
		return nil, false
	}
	var words []string
	for cmd = strings.TrimSpace(cmd); cmd != ""; cmd = strings.TrimSpace(cmd) {
		var word string
		if q := cmd[0]; q == '\'' || q == '"' {
			n := strings.IndexByte(cmd[1:], q)
			if n < 0 {
				return nil, false
			}
			word, cmd = cmd[1:n+1], cmd[n+2:]
			if cmd != "" && cmd[0] != ' ' && cmd[0] != '\t' {
				return nil, false
			}
		} else {
			n := strings.IndexAny(cmd, " \t")
			if n < 0 {
				n = len(cmd)
			}
			word, cmd = cmd[:n], cmd[n:]
			if strings.ContainsAny(word, "\042'") {
				return nil, false
			}
		}
		words = append(words, word)
	}
	return words, true
}
