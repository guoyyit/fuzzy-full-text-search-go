package query

import (
	"strings"
	"unicode"
)

// Node 是查询 AST 的节点。
type Node interface{ queryNode() }

// TermNode 是单个关键词（可字段限定）。
//
// Fuzzy 为三态：
//   - nil  表示是否容错由服务配置（config.MaxEditDistance）决定；
//   - true 表示强制容错（词尾 ~）；
//   - false表示强制精确（词尾 $）。
type TermNode struct {
	Term  string
	Field string // 空表示所有字段
	Fuzzy *bool
}

// PhraseNode 是短语查询：多个词条需在同一字段内顺序相邻出现。
type PhraseNode struct {
	Terms []string
	Field string
	Fuzzy *bool
}

// AndNode 要求所有子句命中。
type AndNode struct{ Children []Node }

// OrNode 任一子句命中即可（查询字符串中相邻词的默认连接方式）。
type OrNode struct{ Children []Node }

// NotNode 排除命中其子句的文档；子句不贡献得分。
type NotNode struct{ Child Node }

func (*TermNode) queryNode()   {}
func (*PhraseNode) queryNode() {}
func (*AndNode) queryNode()    {}
func (*OrNode) queryNode()     {}
func (*NotNode) queryNode()    {}

// ParseError 描述查询语法错误及出错位置。
type ParseError struct {
	Msg string
	Pos int
}

func (e *ParseError) Error() string { return e.Msg }

type tokenKind int

const (
	tkWord tokenKind = iota
	tkLParen
	tkRParen
	tkAnd
	tkOr
	tkNot
)

type token struct {
	kind     tokenKind
	field    string // 仅 tkWord
	word     string // 仅 tkWord：裸词原文（小写）；短语时内部以空格连接
	phrase   bool   // 仅 tkWord：是否来自双引号
	required bool   // + 前缀
	prohibit bool   // - 前缀
	pos      int
}

func isIdentRune(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' }

// lex 把查询字符串切成语法 token。
func lex(text string) ([]token, error) {
	runes := []rune(text)
	n := len(runes)
	var toks []token
	pos := func(bi int) int { return len(string(runes[:bi])) }

	for i := 0; i < n; {
		r := runes[i]
		switch {
		case unicode.IsSpace(r):
			i++
		case r == '(':
			toks = append(toks, token{kind: tkLParen, pos: pos(i)})
			i++
		case r == ')':
			toks = append(toks, token{kind: tkRParen, pos: pos(i)})
			i++
		default:
			start := i
			required, prohibit := false, false
			if r == '+' || r == '-' {
				required = r == '+'
				prohibit = r == '-'
				i++
				if i >= n || unicode.IsSpace(runes[i]) || runes[i] == '(' || runes[i] == ')' {
					return nil, &ParseError{Msg: "前缀 +/- 后缺少查询词", Pos: pos(i)}
				}
				r = runes[i]
			}
			// field: 限定
			field := ""
			if isIdentRune(r) {
				s := i
				for i < n && isIdentRune(runes[i]) {
					i++
				}
				if i < n && runes[i] == ':' {
					field = strings.ToLower(string(runes[s:i]))
					i++
					if i >= n {
						return nil, &ParseError{Msg: "字段限定符 : 后缺少查询词", Pos: pos(i)}
					}
					r = runes[i]
				} else {
					i = s
					r = runes[s]
				}
			}
			var word string
			phrase := false
			if r == '"' {
				i++
				s := i
				for i < n && runes[i] != '"' {
					i++
				}
				if i >= n {
					return nil, &ParseError{Msg: "短语缺少闭合的双引号", Pos: pos(s)}
				}
				word = strings.ToLower(strings.TrimSpace(string(runes[s:i])))
				phrase = true
				i++ // 跳过闭合引号
			} else {
				s := i
				for i < n && !unicode.IsSpace(runes[i]) && runes[i] != '(' && runes[i] != ')' {
					i++
				}
				word = strings.ToLower(string(runes[s:i]))
			}
			if word == "" {
				return nil, &ParseError{Msg: "空的查询词", Pos: pos(start)}
			}
			toks = append(toks, token{
				kind: tkWord, field: field, word: word, phrase: phrase,
				required: required, prohibit: prohibit, pos: pos(start),
			})
		}
	}
	return toks, nil
}

// classifyKeywords 把裸 AND/OR/NOT 词例识别为操作符（带字段限定或引号短语不识别）。
func classifyKeywords(toks []token) {
	for i := range toks {
		if toks[i].kind != tkWord || toks[i].field != "" || toks[i].phrase {
			continue
		}
		switch toks[i].word {
		case "and":
			toks[i].kind = tkAnd
		case "or":
			toks[i].kind = tkOr
		case "not":
			toks[i].kind = tkNot
		}
	}
}

type parser struct {
	tokens []token
	pos    int
}

// Parse 把查询字符串解析为 AST。
func Parse(text string) (Node, error) {
	toks, err := lex(text)
	if err != nil {
		return nil, err
	}
	if len(toks) == 0 {
		return nil, &ParseError{Msg: "查询不能为空", Pos: 0}
	}
	classifyKeywords(toks)
	p := &parser{tokens: toks}
	node, err := p.parseExpr()
	if err != nil {
		return nil, err
	}
	if p.pos != len(p.tokens) {
		return nil, &ParseError{Msg: "查询存在无法解析的多余部分", Pos: p.tokens[p.pos].pos}
	}
	return node, nil
}

// 文法（优先级从低到高：OR < AND < NOT < atom）：
//   expr  := andExpr ((OR | 隐式相邻) andExpr)*
//   andExpr := unary (AND unary)*
//   unary := NOT unary | atom
//   atom  := '(' expr ')' | WORD

// parseExpr 解析 OR 层；两个 andExpr 之间既可用显式 OR，也可直接相邻（隐式 OR）。
func (p *parser) parseExpr() (Node, error) {
	var children []Node
	for {
		right, err := p.parseAnd()
		if err != nil {
			return nil, err
		}
		if right == nil {
			break
		}
		children = append(children, right)
		if p.peek() == tkOr {
			p.pos++
		}
	}
	return joinOr(children), nil
}

// parseAnd 解析 AND 层。
func (p *parser) parseAnd() (Node, error) {
	left, err := p.parseUnary()
	if err != nil {
		return nil, err
	}
	if left == nil {
		return nil, nil
	}
	children := []Node{left}
	for p.peek() == tkAnd {
		p.pos++
		right, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		if right == nil {
			return nil, &ParseError{Msg: "AND 后缺少子句", Pos: p.pos}
		}
		children = append(children, right)
	}
	if len(children) == 1 {
		return children[0], nil
	}
	return &AndNode{Children: children}, nil
}

// parseUnary 处理一元 NOT。
func (p *parser) parseUnary() (Node, error) {
	if p.peek() == tkNot {
		at := p.pos
		p.pos++
		child, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		if child == nil {
			return nil, &ParseError{Msg: "NOT 后缺少子句", Pos: at}
		}
		return &NotNode{Child: child}, nil
	}
	return p.parseAtom()
}

func (p *parser) parseAtom() (Node, error) {
	if p.pos >= len(p.tokens) {
		return nil, nil
	}
	tk := p.tokens[p.pos]
	switch tk.kind {
	case tkLParen:
		p.pos++
		node, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		if p.peek() != tkRParen {
			return nil, &ParseError{Msg: "缺少右括号 )", Pos: tk.pos}
		}
		p.pos++
		return node, nil
	case tkWord:
		p.pos++
		node, err := buildWordNode(tk)
		if err != nil {
			return nil, err
		}
		if tk.prohibit {
			return &NotNode{Child: node}, nil
		}
		return node, nil
	case tkRParen:
		return nil, nil
	default:
		return nil, &ParseError{Msg: "操作符 AND/OR/NOT 的两侧都需要查询子句", Pos: tk.pos}
	}
}

func (p *parser) peek() tokenKind {
	if p.pos >= len(p.tokens) {
		return -1
	}
	return p.tokens[p.pos].kind
}

func joinOr(children []Node) Node {
	if len(children) == 0 {
		return nil
	}
	if len(children) == 1 {
		return children[0]
	}
	return &OrNode{Children: children}
}

// buildWordNode 处理裸词的容错后缀（~ / $）与短语形态。
// 注意：短语由双引号界定，其内部不解析后缀。
func buildWordNode(tk token) (Node, error) {
	raw := tk.word
	var fuzzy *bool
	if !tk.phrase {
		switch {
		case strings.HasSuffix(raw, "$"):
			no := false
			fuzzy = &no
			raw = strings.TrimSuffix(raw, "$")
		case strings.HasSuffix(raw, "~"):
			yes := true
			fuzzy = &yes
			raw = strings.TrimSuffix(raw, "~")
		}
	}
	if raw == "" {
		return nil, &ParseError{Msg: "查询词不能只由修饰符组成", Pos: tk.pos}
	}
	if tk.phrase {
		terms := strings.Fields(raw)
		if len(terms) == 0 {
			return nil, &ParseError{Msg: "短语不能为空", Pos: tk.pos}
		}
		return &PhraseNode{Terms: terms, Field: tk.field, Fuzzy: fuzzy}, nil
	}
	return &TermNode{Term: raw, Field: tk.field, Fuzzy: fuzzy}, nil
}
