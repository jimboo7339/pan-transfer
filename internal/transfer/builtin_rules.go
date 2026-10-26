package transfer

import "strings"

// 内置命名正则规则的展示元信息与辅助函数；
// 规则本体（pattern/replace）来自 magic_rename.go 的 defaultMagicRegex，
// 数据库中的同名 $key 行可覆盖内置值。

// BuiltinRuleMeta 是内置规则的展示信息。
type BuiltinRuleMeta struct {
	Key   string
	Label string
}

// BuiltinRules 内置命名规则清单（顺序即前端展示顺序）。
var BuiltinRules = []BuiltinRuleMeta{
	{Key: "$TV_MAGIC", Label: "剧集魔法匹配"},
	{Key: "$TV_REGEX", Label: "剧集集数（SxxExx）"},
	{Key: "$SHOW_MAGIC", Label: "综艺魔法匹配"},
	{Key: "$SHOW_PRO", Label: "综艺序号优先"},
	{Key: "$BLACK_WORD", Label: "黑名单关键词过滤"},
}

// builtinLabels 便于按 key 查展示名。
var builtinLabels = func() map[string]string {
	m := map[string]string{}
	for _, b := range BuiltinRules {
		m[b.Key] = b.Label
	}
	return m
}()

// BuiltinLabel 返回内置规则展示名；非内置返回空。
func BuiltinLabel(key string) string { return builtinLabels[key] }

// BuiltinRuleDef 返回内置规则的默认 pattern/replace；非内置返回空串。
func BuiltinRuleDef(key string) (string, string) {
	if e, ok := defaultMagicRegex()[key]; ok {
		return e.Pattern, e.Replace
	}
	return "", ""
}

// MatchName 用规则的 pattern（支持 $ 内置规则名）判断样例文件名是否命中。
func MatchName(pattern, name string) bool {
	mr := NewMagicRename()
	actual, _ := mr.MagicRegexConv(pattern, "")
	if strings.TrimSpace(actual) == "" {
		return true
	}
	c := mr.compile(actual)
	return c != nil && c.MatchString(name)
}

// ExpandRule 把 $ 规则名展开为实际 pattern/replace。
func ExpandRule(pattern, replace string) (string, string) {
	mr := NewMagicRename()
	return mr.MagicRegexConv(pattern, replace)
}

// TestRename 按规则对样例文件名计算重命名结果（不落库，供正则维护页试算）。
func TestRename(name, pattern, replace string) (matched bool, result, actualPattern, actualReplace string) {
	mr := NewMagicRename()
	actualPattern, actualReplace = mr.MagicRegexConv(pattern, replace)
	matched = true
	if strings.TrimSpace(actualPattern) != "" {
		c := mr.compile(actualPattern)
		matched = c != nil && c.MatchString(name)
	}
	if matched {
		result = mr.Sub(actualPattern, actualReplace, name)
	}
	return
}
