package main

// 源文件规模约束：单文件不超过 800 行，单个函数（含方法、Vue/JS 里的
// 函数与箭头函数）不超过 250 行。
//
// 行数只是代理指标，真正约束的是职责：文件或函数超长通常意味着多个关注点
// 挤在一起，改动时互相牵连、review 也看不出边界。超限时按领域拆文件、按
// 步骤拆函数，而不是靠压缩格式把行数藏起来。

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	maxFileLines = 800
	maxFuncLines = 250
)

// sourceExts 是需要纳入规模约束的源文件类型；构建产物与依赖目录不参与。
var sourceExts = map[string]bool{".go": true, ".vue": true, ".js": true, ".ts": true}

func skipSourceDir(name string) bool {
	switch name {
	case "node_modules", "dist", ".git", "testdata", "vendor":
		return true
	}
	return false
}

// walkSourceFiles 遍历仓库内手工维护的源文件。
func walkSourceFiles(t *testing.T, fn func(path string) error) {
	t.Helper()
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if skipSourceDir(d.Name()) {
				return fs.SkipDir
			}
			return nil
		}
		if !sourceExts[filepath.Ext(path)] || strings.HasSuffix(path, ".min.js") {
			return nil
		}
		return fn(path)
	})
	if err != nil {
		t.Fatalf("扫描源文件失败: %v", err)
	}
}

// TestSourceFilesStaySmall 固化「单个源文件不超过 800 行」的约定。
func TestSourceFilesStaySmall(t *testing.T) {
	walkSourceFiles(t, func(path string) error {
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		lines, err := countLines(f)
		if err != nil {
			return err
		}
		if lines > maxFileLines {
			t.Errorf("%s 有 %d 行，超过单文件上限 %d 行；请按职责拆分", path, lines, maxFileLines)
		}
		return nil
	})
}

// TestFunctionsStaySmall 固化「单个函数不超过 250 行」的约定。
func TestFunctionsStaySmall(t *testing.T) {
	walkSourceFiles(t, func(path string) error {
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var spans []funcSpan
		if filepath.Ext(path) == ".go" {
			spans = goFuncSpans(t, path, src)
		} else {
			spans = jsFuncSpans(path, string(src))
		}
		for _, s := range spans {
			if s.lines > maxFuncLines {
				t.Errorf("%s:%d %s 有 %d 行，超过单函数上限 %d 行；请按步骤拆分",
					path, s.line, s.name, s.lines, maxFuncLines)
			}
		}
		return nil
	})
}

type funcSpan struct {
	name  string
	line  int
	lines int
}

// goFuncSpans 用 go/ast 精确取每个函数/方法的起止行，函数字面量一并计入
// （长回调同样是难读的信号）。
func goFuncSpans(t *testing.T, path string, src []byte) []funcSpan {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, src, 0)
	if err != nil {
		t.Fatalf("解析 %s 失败: %v", path, err)
	}
	var spans []funcSpan
	ast.Inspect(file, func(n ast.Node) bool {
		var name string
		var start, end token.Pos
		switch x := n.(type) {
		case *ast.FuncDecl:
			name = x.Name.Name
			if x.Recv != nil && len(x.Recv.List) > 0 {
				name = recvTypeName(x.Recv.List[0].Type) + "." + name
			}
			start, end = x.Pos(), x.End()
		case *ast.FuncLit:
			name = "func literal"
			start, end = x.Pos(), x.End()
		default:
			return true
		}
		s, e := fset.Position(start).Line, fset.Position(end).Line
		spans = append(spans, funcSpan{name: name, line: s, lines: e - s + 1})
		return true
	})
	return spans
}

func recvTypeName(expr ast.Expr) string {
	switch x := expr.(type) {
	case *ast.StarExpr:
		return "*" + recvTypeName(x.X)
	case *ast.Ident:
		return x.Name
	case *ast.IndexExpr:
		return recvTypeName(x.X)
	}
	return "?"
}

// jsFuncSpans 粗略但够用地识别 Vue/JS 函数体：
// `function f() {`、`f() {`（对象/类方法）、`const f = () => {`、
// `x.map(v => {`。只关心行数，不需要完整语法树。
func jsFuncSpans(path, src string) []funcSpan {
	if strings.HasSuffix(path, ".vue") {
		src = vueScriptOnly(src)
	}
	return newJSScanner(src).scan()
}

// vueScriptOnly 把 <script> 之外的字符抹成空格（保留换行，行号不变），
// 避免模板里的 `{{ }}` 影响括号匹配。
func vueScriptOnly(src string) string {
	out := []byte(src)
	mask := func(from, to int) {
		for i := from; i < to && i < len(out); i++ {
			if out[i] != '\n' {
				out[i] = ' '
			}
		}
	}
	pos := 0
	for {
		open := strings.Index(src[pos:], "<script")
		if open < 0 {
			mask(pos, len(src))
			break
		}
		open += pos
		mask(pos, open)
		gt := strings.IndexByte(src[open:], '>')
		if gt < 0 {
			break
		}
		body := open + gt + 1
		closeIdx := strings.Index(src[body:], "</script>")
		if closeIdx < 0 {
			mask(body, len(src))
			break
		}
		pos = body + closeIdx + len("</script>")
	}
	return string(out)
}

// jsScanner 逐字符扫描，跳过注释、字符串、模板字符串与正则字面量，
// 只在真正的代码花括号上做配对。
type jsScanner struct {
	src   string
	spans []funcSpan
	stack []jsFrame
	line  int
}

type jsFrame struct {
	start int
	name  string
	isFn  bool
}

func newJSScanner(src string) *jsScanner {
	return &jsScanner{src: src, line: 1}
}

// 这些关键字后面的 `(...) {` 是普通代码块，不是函数体。
var jsBlockKeywords = map[string]bool{
	"if": true, "for": true, "while": true, "switch": true, "catch": true,
	"with": true, "else": true, "do": true, "try": true, "finally": true,
}

func (s *jsScanner) scan() []funcSpan {
	src := s.src
	n := len(src)
	prevSig := byte(0) // 最近一个非空白字符
	arrow := false     // 上一个 token 是 `=>`
	for i := 0; i < n; i++ {
		c := src[i]
		switch {
		case c == '\n':
			s.line++
			continue
		case c == ' ' || c == '\t' || c == '\r':
			continue
		case c == '/' && i+1 < n && src[i+1] == '/':
			for i < n && src[i] != '\n' {
				i++
			}
			i--
			continue
		case c == '/' && i+1 < n && src[i+1] == '*':
			i += 2
			for i+1 < n && !(src[i] == '*' && src[i+1] == '/') {
				if src[i] == '\n' {
					s.line++
				}
				i++
			}
			i++
			continue
		case c == '\'' || c == '"':
			i = s.skipString(i, c)
			prevSig, arrow = c, false
			continue
		case c == '`':
			i = s.skipTemplate(i)
			prevSig, arrow = c, false
			continue
		case c == '/' && isRegexStart(prevSig):
			i = s.skipRegex(i)
			prevSig, arrow = c, false
			continue
		}

		switch {
		case isIdentChar(c):
			for i < n && isIdentChar(src[i]) {
				i++
			}
			i--
			prevSig, arrow = 'a', false
			continue
		case c == '=' && i+1 < n && src[i+1] == '>':
			arrow = true
			i++
			prevSig = '>'
			continue
		case c == '{':
			f := jsFrame{start: s.line}
			if arrow {
				f.isFn, f.name = true, "arrow fn"
			} else if prevSig == ')' {
				f.name, f.isFn = s.parenOwner(i)
			}
			s.stack = append(s.stack, f)
			arrow, prevSig = false, '{'
			continue
		case c == '}':
			if k := len(s.stack); k > 0 {
				f := s.stack[k-1]
				s.stack = s.stack[:k-1]
				if f.isFn {
					s.spans = append(s.spans, funcSpan{
						name: f.name, line: f.start, lines: s.line - f.start + 1,
					})
				}
			}
			arrow, prevSig = false, '}'
			continue
		}
		arrow, prevSig = false, c
	}
	return s.spans
}

// parenOwner 从 `{` 前的 `)` 回溯到配对的 `(`，取 `(` 前的标识符，
// 判断这个块是不是函数体（`function f() {` / `f() {` / `obj.f() {`）。
func (s *jsScanner) parenOwner(brace int) (string, bool) {
	idx := brace - 1
	for idx >= 0 && isSpace(s.src[idx]) {
		idx--
	}
	if idx < 0 || s.src[idx] != ')' {
		return "", false
	}
	depth := 0
	for ; idx >= 0; idx-- {
		switch s.src[idx] {
		case ')':
			depth++
		case '(':
			depth--
			if depth == 0 {
				goto found
			}
		}
	}
	return "", false
found:
	idx--
	for idx >= 0 && isSpace(s.src[idx]) {
		idx--
	}
	end := idx + 1
	for idx >= 0 && (isIdentChar(s.src[idx]) || s.src[idx] == ']') {
		idx--
	}
	name := s.src[idx+1 : end]
	if name == "" {
		return "", false
	}
	if name == "function" {
		return "func literal", true
	}
	if name == "async" {
		return "", false
	}
	if jsBlockKeywords[name] {
		return "", false
	}
	if i := strings.LastIndexByte(name, ']'); i >= 0 {
		return "computed method", true
	}
	return name, true
}

func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r'
}

func (s *jsScanner) skipString(i int, quote byte) int {
	for i++; i < len(s.src); i++ {
		switch s.src[i] {
		case '\\':
			i++
		case quote:
			return i
		case '\n':
			s.line++
		}
	}
	return i
}

func (s *jsScanner) skipTemplate(i int) int {
	for i++; i < len(s.src); i++ {
		switch s.src[i] {
		case '\\':
			i++
		case '\n':
			s.line++
		case '`':
			return i
		}
	}
	return i
}

func (s *jsScanner) skipRegex(i int) int {
	for i++; i < len(s.src); i++ {
		switch s.src[i] {
		case '\\':
			i++
		case '\n':
			return i
		case '/':
			return i
		}
	}
	return i
}

func isIdentChar(c byte) bool {
	return c == '_' || c == '$' || c == '.' ||
		(c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

func isRegexStart(prev byte) bool {
	switch prev {
	case 0, '(', ',', '=', ':', '[', '!', '&', '|', '?', '{', '}', ';', '+', '-', '*', '%', '^', '~', '<', '>':
		return true
	}
	return false
}

// countLines 统计换行符个数；末行没有换行符时也计入一行。
func countLines(r io.Reader) (int, error) {
	buf := make([]byte, 64*1024)
	lines, last := 0, byte(0)
	for {
		n, err := r.Read(buf)
		for _, b := range buf[:n] {
			if b == '\n' {
				lines++
			}
			last = b
		}
		if err == io.EOF {
			if last != 0 && last != '\n' {
				lines++
			}
			return lines, nil
		}
		if err != nil {
			return lines, err
		}
	}
}

// TestJSFuncScanner 保证前端函数扫描器只认真正的函数体：漏报会让约束形同
// 虚设，误报则会逼着大家去拆对象字面量和 if 块。
func TestJSFuncScanner(t *testing.T) {
	src := strings.Join([]string{
		"const a = () => {",      // 1
		"  1",                    // 2
		"}",                      // 3
		"function b() {",         // 4
		"  2",                    // 5
		"  3",                    // 6
		"}",                      // 7
		"if (x) {",               // 8
		"  y()",                  // 9
		"}",                      // 10
		"const obj = {",          // 11
		"  k: 1,",                // 12
		"}",                      // 13
		"const o2 = {",           // 14
		"  m() {",                // 15
		"    4",                  // 16
		"  },",                   // 17
		"}",                      // 18
		"const s = `{{${a}}}`",   // 19
		"const re = /[{}]/",      // 20
		"for (const i of y) { }", // 21
		"try { } catch (e) { }",  // 22
		"x.map(v => {",           // 23
		"  v",                    // 24
		"})",                     // 25
	}, "\n")

	spans := jsFuncSpans("sample.js", src)
	got := map[string]funcSpan{}
	for _, s := range spans {
		got[s.name] = s
	}
	// 两个箭头函数同名，按名字去重后只留一个；总数单独断言。
	if len(spans) != 4 {
		t.Errorf("识别到 %d 个函数，期望 4（两个箭头函数 + b + m）", len(spans))
	}
	want := map[string]int{"arrow fn": 3, "b": 4, "m": 3}
	for name, lines := range want {
		s, ok := got[name]
		if !ok {
			t.Errorf("未识别出函数 %s", name)
			continue
		}
		if s.lines != lines {
			t.Errorf("%s 行数 = %d，期望 %d", name, s.lines, lines)
		}
	}
	for _, name := range []string{"if", "for", "try", "catch", "obj"} {
		if _, ok := got[name]; ok {
			t.Errorf("把 %s 块误判成函数", name)
		}
	}
	if len(got) != len(want) {
		t.Errorf("识别到 %d 个不同函数，期望 %d：%v", len(got), len(want), got)
	}
}

// TestVueScriptOnly 确认模板里的 {{ }} 不会进入函数扫描。
func TestVueScriptOnly(t *testing.T) {
	src := strings.Join([]string{
		"<template>",                       // 1
		"  <div>{{ n }}</div>",             // 2
		"</template>",                      // 3
		"<script setup>",                   // 4
		"function f() {",                   // 5
		"  1",                              // 6
		"}",                                // 7
		"</script>",                        // 8
		"<style>.a { color: red }</style>", // 9
	}, "\n")
	spans := jsFuncSpans("Sample.vue", src)
	if len(spans) != 1 || spans[0].name != "f" {
		t.Fatalf("Vue 脚本区识别结果 = %v，期望只有 f", spans)
	}
	if spans[0].line != 5 || spans[0].lines != 3 {
		t.Errorf("f 位置 = 行 %d / %d 行，期望行 5 / 3 行", spans[0].line, spans[0].lines)
	}
}
