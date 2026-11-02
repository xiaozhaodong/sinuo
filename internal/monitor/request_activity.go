package monitor

import "sync"

// RequestActivity 是业务请求接收与退出排空的唯一状态；连接统计不参与退出判断。
// 零值可用。接收和开始排空使用同一把锁，避免读取零请求后又受理新请求。
type RequestActivity struct {
	mu        sync.Mutex
	active    int
	draining  map[string]bool
	listeners map[chan struct{}]struct{}
}

// RequestLease 从受理持有到响应转发结束；重复结束安全。
type RequestLease struct {
	activity *RequestActivity
	ended    bool // 由 activity.mu 保护
}

func (a *RequestActivity) TryStart() (*RequestLease, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.draining) != 0 {
		return nil, false
	}
	a.active++
	a.notifyLocked()
	return &RequestLease{activity: a}, true
}

// AcceptAuxiliary 只校验无需等待的预热是否允许进入，不产生短暂的业务请求计数。
func (a *RequestActivity) AcceptAuxiliary() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.draining) == 0
}

func (l *RequestLease) End() {
	a := l.activity
	a.mu.Lock()
	defer a.mu.Unlock()
	if l.ended {
		return
	}
	l.ended = true
	a.active--
	a.notifyLocked()
}

// TryContinue 只允许尚未结束的已受理请求衍生服务端续接，不重新放开接收门。
func (l *RequestLease) TryContinue() (*RequestLease, bool) {
	a := l.activity
	a.mu.Lock()
	defer a.mu.Unlock()
	if l.ended {
		return nil, false
	}
	a.active++
	a.notifyLocked()
	return &RequestLease{activity: a}, true
}

func (a *RequestActivity) Count() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.active
}

// BeginDrain 按退出来源持有接收门；取消其中一个来源不能解除另一个来源的排空。
func (a *RequestActivity) BeginDrain(owner string) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.draining == nil {
		a.draining = make(map[string]bool)
	}
	a.draining[owner] = true
	return a.active
}

func (a *RequestActivity) CancelDrain(owner string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.draining, owner)
}

func (a *RequestActivity) Subscribe() (<-chan struct{}, func()) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.listeners == nil {
		a.listeners = make(map[chan struct{}]struct{})
	}
	ch := make(chan struct{}, 1)
	a.listeners[ch] = struct{}{}
	ch <- struct{}{}
	var once sync.Once
	return ch, func() {
		once.Do(func() {
			a.mu.Lock()
			defer a.mu.Unlock()
			delete(a.listeners, ch)
			close(ch)
		})
	}
}

func (a *RequestActivity) notifyLocked() {
	for ch := range a.listeners {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

func (m *Metrics) RequestActivity() *RequestActivity { return &m.requestActivity }
