package tray

import "sync"

type QuitChoice int

const (
	QuitCancel QuitChoice = iota
	QuitWait
	QuitNow
)

// QuitState 统一管理所有退出入口；请求数始终由 Metrics 提供。
type QuitState struct {
	mu                         sync.Mutex
	waiting, prompting, forced bool
}

// Request 返回是否放行退出、是否需要新建确认框。
func (q *QuitState) Request(active int, powerOff bool) (allow, prompt bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if powerOff || q.forced || active == 0 {
		return true, false
	}
	if q.prompting {
		return false, false
	}
	q.prompting = true
	return false, true
}

// Choose 在用户作出选择时重新读取请求数，覆盖弹框期间请求结束的情况。
func (q *QuitState) Choose(choice QuitChoice, active int) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.prompting = false
	q.waiting = choice == QuitWait
	q.forced = choice == QuitNow
	return q.forced || (q.waiting && active == 0)
}

func (q *QuitState) Waiting() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.waiting
}

// Drained 不设置强退标记：Quit 到达 ShouldQuit 前若又有新请求，继续等待。
func (q *QuitState) Drained(active int) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.waiting && !q.prompting && active == 0
}

func (q *QuitState) WaitingWithoutPrompt() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.waiting && !q.prompting
}
