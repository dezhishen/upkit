package engine

import (
	"fmt"
	"strings"

	"github.com/dezhishen/upkit/internal/apps"
)

// ConflictKind 是冲突类型。
type ConflictKind string

const (
	// ConflictSameTarget 两个条目指向同一个安装目录。
	ConflictSameTarget ConflictKind = "same-target"
	// ConflictSameID 两个条目使用了相同 id（手工改配置时可能出现）。
	ConflictSameID ConflictKind = "same-id"
	// ConflictFuzzy 疑似同一个软件（名称/入口文件相同）。
	ConflictFuzzy ConflictKind = "fuzzy"
)

// Conflict 描述一次冲突。
type Conflict struct {
	Kind    ConflictKind
	With    []string // 与之冲突的其它 App ID
	Message string
	Policy  string
}

// normalizeConflicts 做一次冲突归一化：靠前的条目保留，其余标记 shadowed。
//
// 规则（默认全部 block）：
//   - 同 id：保留第一个；
//   - 同目标（规范化后的安装路径相同）：保留第一个；
//   - 疑似（名称相同且有相同的入口文件）：保留第一个，仅提示。
//
// shadowed 的条目仍然保留在列表里，界面上置灰并说明原因，避免「我明明启用了
// 却没被更新」的困惑。
func normalizeConflicts(list []*App, file *apps.File) {
	policy := apps.Conflicts{SameID: "block", SameTarget: "block", Fuzzy: "block"}
	if file != nil {
		policy = file.Conflicts
	}

	seenID := map[string]*App{}
	seenPath := map[string]*App{}
	seenFuzzy := map[string]*App{}

	for _, a := range list {
		// 1) 同 id
		if prev, dup := seenID[a.Ref.ID]; dup {
			markShadow(a, Conflict{
				Kind:    ConflictSameID,
				With:    []string{prev.Ref.ID},
				Policy:  policy.SameID,
				Message: fmt.Sprintf("与 %s 使用了相同的 id", prev.Ref.DisplayName()),
			})
			continue
		}
		seenID[a.Ref.ID] = a

		// 2) 同目标
		key := normalizePath(a.Ref.InstallPath)
		if prev, dup := seenPath[key]; dup {
			markShadow(a, Conflict{
				Kind:    ConflictSameTarget,
				With:    []string{prev.Ref.ID},
				Policy:  policy.SameTarget,
				Message: fmt.Sprintf("安装路径与 %s 相同（%s）", prev.Ref.DisplayName(), a.Ref.InstallPath),
			})
			continue
		}
		seenPath[key] = a

		// 3) 疑似：名称 + 入口文件都相同
		fuzzyKey := strings.ToLower(a.Ref.DisplayName()) + "|" + strings.Join(a.Ref.Entrypoints, ",")
		if len(a.Ref.Entrypoints) > 0 {
			if prev, dup := seenFuzzy[fuzzyKey]; dup {
				markShadow(a, Conflict{
					Kind:    ConflictFuzzy,
					With:    []string{prev.Ref.ID},
					Policy:  policy.Fuzzy,
					Message: fmt.Sprintf("与 %s 的名称和入口文件相同，疑似同一个软件", prev.Ref.DisplayName()),
				})
				continue
			}
			seenFuzzy[fuzzyKey] = a
		}
	}

	// 用户手工声明的等价组：整组按 same-id 处理
	applyEquivalents(list, file)
}

// markShadow 标记被遮蔽的条目。
func markShadow(a *App, c Conflict) {
	if a.Conflict == nil {
		a.Conflict = &c
	} else {
		a.Conflict.With = append(a.Conflict.With, c.With...)
		if a.Conflict.Message == "" {
			a.Conflict.Message = c.Message
		}
	}
	a.Shadowed = true
	a.Action = "noop"
	a.Note = "已被 " + strings.Join(c.With, ", ") + " 取代：" + c.Message
}

// applyEquivalents 处理用户手工声明的等价组与否认声明。
func applyEquivalents(list []*App, file *apps.File) {
	if file == nil {
		return
	}
	denied := map[string]bool{}
	for _, pair := range file.NotEquivalent {
		denied[equivKey(pair)] = true
	}
	for _, eq := range file.Equivalents {
		if len(eq.Members) < 2 {
			continue
		}
		if denied[equivKey(eq.Members[:2])] {
			continue
		}
		var kept string
		for _, member := range eq.Members {
			a := findByID(list, member)
			if a == nil {
				continue
			}
			if kept == "" {
				kept = member
				continue
			}
			markShadow(a, Conflict{
				Kind:    ConflictSameID,
				With:    []string{kept},
				Policy:  firstNonEmpty(eq.Policy, "block"),
				Message: fmt.Sprintf("用户声明与 %s 等价（%s）", kept, firstNonEmpty(eq.Canonical, "equivalents")),
			})
		}
	}
}

func findByID(list []*App, id string) *App {
	for _, a := range list {
		if a.Ref.ID == id && !a.Shadowed {
			return a
		}
	}
	return nil
}

func equivKey(members []string) string {
	cp := append([]string(nil), members...)
	sortStrings(cp)
	return strings.Join(cp, "\x00")
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
