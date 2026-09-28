package category

import "sync"

// Live 保存当前生效的规则，替换后下一次匹配立刻使用新规则。
type Live struct {
	mu sync.RWMutex
	m  *Matcher
}

// NewLive 用初始规则创建可替换的规则集。
func NewLive(m *Matcher) *Live {
	return &Live{m: m}
}

// Get 返回当前规则。没有规则时返回 nil。
func (l *Live) Get() *Matcher {
	if l == nil {
		return nil
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.m
}

// Set 替换当前规则。
func (l *Live) Set(m *Matcher) {
	l.mu.Lock()
	l.m = m
	l.mu.Unlock()
}
