//go:build darwin

package service

import (
	"errors"
	"os"
	"sync"

	"golang.org/x/sys/unix"
)

// 仅补充目录项变化通知。文件内容写入仍交给 fsnotify；链接身份与读取边界由共用逻辑判断。
type privacyDirectoryWatcher struct {
	kq      int
	pipe    [2]int
	paths   map[int]string
	changes chan string
	done    chan struct{}
	stopped chan struct{}
	once    sync.Once
}

// 单个目录的错误随可用监听一起返回，最后一个返回值仅表示监听器整体无法启动。
func newPrivacyDirectoryWatcher(paths []string) (*privacyDirectoryWatcher, []error, error) {
	if len(paths) == 0 {
		return nil, nil, nil
	}
	kq, err := unix.Kqueue()
	if err != nil {
		return nil, nil, err
	}
	unix.CloseOnExec(kq)
	w := &privacyDirectoryWatcher{kq: kq, paths: make(map[int]string), changes: make(chan string, 64), done: make(chan struct{}), stopped: make(chan struct{})}
	if err := unix.Pipe(w.pipe[:]); err != nil {
		_ = unix.Close(kq)
		return nil, nil, err
	}
	unix.CloseOnExec(w.pipe[0])
	unix.CloseOnExec(w.pipe[1])
	started := false
	defer func() {
		if !started {
			_ = unix.Close(w.pipe[1])
			w.release()
		}
	}()
	var stopEvent unix.Kevent_t
	unix.SetKevent(&stopEvent, w.pipe[0], unix.EVFILT_READ, unix.EV_ADD|unix.EV_ENABLE)
	if _, err := unix.Kevent(kq, []unix.Kevent_t{stopEvent}, nil, nil); err != nil {
		return nil, nil, err
	}
	var skipped []error
	for _, path := range paths {
		fd, err := unix.Open(path, unix.O_EVTONLY|unix.O_CLOEXEC, 0)
		if err != nil {
			skipped = append(skipped, &os.PathError{Op: "打开目录", Path: path, Err: err})
			continue
		}
		var event unix.Kevent_t
		unix.SetKevent(&event, fd, unix.EVFILT_VNODE, unix.EV_ADD|unix.EV_CLEAR|unix.EV_ENABLE)
		event.Fflags = unix.NOTE_WRITE | unix.NOTE_DELETE | unix.NOTE_RENAME
		if _, err := unix.Kevent(kq, []unix.Kevent_t{event}, nil, nil); err != nil {
			_ = unix.Close(fd)
			skipped = append(skipped, &os.PathError{Op: "注册目录监听", Path: path, Err: err})
			continue
		}
		w.paths[fd] = path
	}
	if len(w.paths) == 0 {
		return nil, skipped, nil
	}
	started = true
	go w.run()
	return w, skipped, nil
}

func (w *privacyDirectoryWatcher) events() <-chan string {
	if w == nil {
		return nil
	}
	return w.changes
}

func (w *privacyDirectoryWatcher) run() {
	defer close(w.stopped)
	defer close(w.changes)
	events := make([]unix.Kevent_t, 32)
	for {
		n, err := unix.Kevent(w.kq, nil, events, nil)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			// 关闭通知通道，由服务明确报告监听停止，不能伪装成仍在监听。
			return
		}
		for _, event := range events[:n] {
			fd := int(event.Ident)
			if fd == w.pipe[0] {
				return
			}
			if path, ok := w.paths[fd]; ok {
				select {
				case w.changes <- path:
				case <-w.done:
					return
				}
			}
		}
	}
}

func (w *privacyDirectoryWatcher) close() {
	if w == nil {
		return
	}
	w.once.Do(func() {
		close(w.done)
		_ = unix.Close(w.pipe[1])
		<-w.stopped
		w.release()
	})
}

func (w *privacyDirectoryWatcher) release() {
	for fd := range w.paths {
		_ = unix.Close(fd)
	}
	_ = unix.Close(w.pipe[0])
	_ = unix.Close(w.kq)
}
