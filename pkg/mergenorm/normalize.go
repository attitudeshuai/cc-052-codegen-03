// Package mergenorm 存放「采集端 × 手工台账」对账时用到的字段归一化规则。
// 同一件事在两边的写法常常不同（“张三 ”/“张 三”/“张师傅”、全角字符、
// 地块名带不带括号），配对前必须先归一到同一把尺子。
package mergenorm

import (
	"strings"
	"unicode"
)

// runWidthToASCII 把全角 ASCII 区段转成半角，全角空格转普通空格。
func runWidthToASCII(r rune) rune {
	switch {
	case r == 0x3000:
		return ' '
	case r >= 0xFF01 && r <= 0xFF5E:
		return r - 0xFEE0
	default:
		return r
	}
}

func foldSpaces(s string) string {
	return strings.Join(strings.FieldsFunc(s, func(r rune) bool {
		return unicode.IsSpace(r)
	}), " ")
}

// Operator 归一化操作人姓名：
// 全角→半角、去掉全部空白、英文转小写、去掉常见称谓后缀。
// 例：“ 张师傅 ” / “张 三” / “zhang  SAN” → “张三” / “zhangsan”。
func Operator(s string) string {
	s = strings.TrimSpace(strings.Map(runWidthToASCII, s))
	s = strings.ToLower(s)
	var b strings.Builder
	for _, r := range s {
		if unicode.IsSpace(r) {
			continue
		}
		b.WriteRune(r)
	}
	s = b.String()
	for _, suffix := range []string{"师傅", "同志", "老师", "先生", "女士"} {
		s = strings.TrimSuffix(s, suffix)
	}
	return s
}

// PlotName 归一化地块名：去全部空白、去常见装饰括号、英文小写。
// 例：“3 号地（东）” 与 “3号地(东)” 归一后相同。
func PlotName(s string) string {
	s = strings.TrimSpace(strings.Map(runWidthToASCII, s))
	var b strings.Builder
	for _, r := range s {
		if unicode.IsSpace(r) {
			continue
		}
		switch r {
		case '(', ')', '（', '）', '[', ']', '【', '】', '-', '_':
			continue
		}
		b.WriteRune(unicode.ToLower(r))
	}
	return b.String()
}

// Crop 归一化作物编码：去空白、小写。
func Crop(s string) string {
	s = strings.TrimSpace(strings.Map(runWidthToASCII, s))
	s = foldSpaces(s)
	return strings.ToLower(s)
}
