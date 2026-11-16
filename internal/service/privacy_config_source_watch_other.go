//go:build !darwin

package service

// 其他平台继续由 fsnotify 提供目录项通知，不启动额外监听。
type privacyDirectoryWatcher struct{}

func newPrivacyDirectoryWatcher([]string) (*privacyDirectoryWatcher, []error, error) {
	return nil, nil, nil
}
func (*privacyDirectoryWatcher) events() <-chan string { return nil }
func (*privacyDirectoryWatcher) close()                {}
