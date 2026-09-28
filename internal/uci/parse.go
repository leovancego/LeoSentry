// Package uci 读写 OpenWrt 的 UCI 配置。
// 只读场景直接解析配置文件，避免周期性 fork 进程；
// 需要写回系统配置时通过 uci 命令行完成，由 uci 负责加锁与格式。
package uci

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"
)

// Section 是 UCI 配置中的一个 config 段。
type Section struct {
	Type string
	// Name 为段名，匿名段为空。
	Name string
	// Index 是该段在同类型段中的序号，对应 uci 路径中的 @type[index]。
	Index   int
	Options map[string][]string
}

// Get 返回选项的第一个值，不存在时返回空串。
func (s *Section) Get(name string) string {
	if v := s.Options[name]; len(v) > 0 {
		return v[0]
	}
	return ""
}

// Lookup 返回选项的第一个值以及该选项是否存在。
func (s *Section) Lookup(name string) (string, bool) {
	v, ok := s.Options[name]
	if !ok || len(v) == 0 {
		return "", false
	}
	return v[0], true
}

// List 返回 list 选项的全部值。
func (s *Section) List(name string) []string {
	return s.Options[name]
}

// File 是解析后的一个 UCI 配置文件。
type File struct {
	Sections []*Section
}

// ByType 返回指定类型的所有段，顺序与文件中一致。
func (f *File) ByType(typ string) []*Section {
	var out []*Section
	for _, s := range f.Sections {
		if s.Type == typ {
			out = append(out, s)
		}
	}
	return out
}

// Named 返回指定名称的段，不存在时返回 nil。
func (f *File) Named(name string) *Section {
	for _, s := range f.Sections {
		if s.Name == name {
			return s
		}
	}
	return nil
}

// ParseFile 解析指定路径的 UCI 配置文件。
func ParseFile(path string) (*File, error) {
	fp, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer fp.Close()
	return Parse(fp)
}

// Parse 解析 UCI 配置内容。
func Parse(r io.Reader) (*File, error) {
	f := &File{}
	typeCount := make(map[string]int)
	var cur *Section

	sc := bufio.NewScanner(r)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		tokens, err := tokenize(sc.Text())
		if err != nil {
			return nil, fmt.Errorf("uci: line %d: %w", lineNo, err)
		}
		if len(tokens) == 0 {
			continue
		}
		switch tokens[0] {
		case "config":
			if len(tokens) < 2 {
				return nil, fmt.Errorf("uci: line %d: config without type", lineNo)
			}
			cur = &Section{
				Type:    tokens[1],
				Index:   typeCount[tokens[1]],
				Options: make(map[string][]string),
			}
			if len(tokens) >= 3 {
				cur.Name = tokens[2]
			}
			typeCount[tokens[1]]++
			f.Sections = append(f.Sections, cur)
		case "option":
			if cur == nil || len(tokens) < 3 {
				continue
			}
			cur.Options[tokens[1]] = []string{tokens[2]}
		case "list":
			if cur == nil || len(tokens) < 3 {
				continue
			}
			cur.Options[tokens[1]] = append(cur.Options[tokens[1]], tokens[2])
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return f, nil
}

// tokenize 按 shell 风格切分一行：支持单引号、双引号、反斜杠转义与行尾注释。
func tokenize(line string) ([]string, error) {
	var (
		tokens  []string
		buf     strings.Builder
		inToken bool
	)
	flush := func() {
		if inToken {
			tokens = append(tokens, buf.String())
			buf.Reset()
			inToken = false
		}
	}

	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case c == ' ' || c == '\t':
			flush()
		case c == '#' && !inToken:
			return tokens, nil
		case c == '\'':
			inToken = true
			end := strings.IndexByte(line[i+1:], '\'')
			if end < 0 {
				return nil, fmt.Errorf("unterminated single quote")
			}
			buf.WriteString(line[i+1 : i+1+end])
			i += end + 1
		case c == '"':
			inToken = true
			i++
			for ; i < len(line) && line[i] != '"'; i++ {
				if line[i] == '\\' && i+1 < len(line) {
					i++
				}
				buf.WriteByte(line[i])
			}
			if i >= len(line) {
				return nil, fmt.Errorf("unterminated double quote")
			}
		case c == '\\' && i+1 < len(line):
			inToken = true
			i++
			buf.WriteByte(line[i])
		default:
			inToken = true
			buf.WriteByte(c)
		}
	}
	flush()
	return tokens, nil
}
