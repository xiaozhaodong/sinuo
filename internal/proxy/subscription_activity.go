package proxy

import (
	"errors"

	"sinuo/internal/monitor"
)

var errSubscriptionDraining = errors.New("subscription proxy is shutting down")

// admitActivityLocked 与提交状态共用锁；终态解析后继续持有租约，直到对应消息写出。
func (s *subscriptionSubmissionState) admitActivityLocked(submission *subscriptionSubmission) bool {
	if s.activity == nil {
		return true
	}
	if !submission.Generate {
		return s.activity.AcceptAuxiliary()
	}
	lease, accepted := s.activity.TryStart()
	if !accepted && submission.ServerInitiated {
		// steering 的服务端 successor 属于已受理生成的续接；保留同一连接上的在途工作。
		for _, parent := range s.inFlight {
			lease, accepted = parent.TryContinue()
			if accepted {
				break
			}
		}
	}
	if !accepted {
		return false
	}
	if s.inFlight == nil {
		s.inFlight = make(map[*subscriptionSubmission]*monitor.RequestLease)
	}
	s.inFlight[submission] = lease
	return true
}

func (s *subscriptionSubmissionState) activityForwarded(submission *subscriptionSubmission) func() {
	return func() {
		s.mu.Lock()
		lease := s.inFlight[submission]
		delete(s.inFlight, submission)
		s.mu.Unlock()
		if lease != nil {
			lease.End()
		}
	}
}

// releaseActivity 在桥接已结束、无法再写出尾帧后收尾，覆盖写失败和排队失败。
func (s *subscriptionSubmissionState) releaseActivity() {
	s.mu.Lock()
	leases := s.inFlight
	s.inFlight = nil
	s.mu.Unlock()
	for _, lease := range leases {
		lease.End()
	}
}
