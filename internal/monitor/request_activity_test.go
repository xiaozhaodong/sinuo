package monitor

import (
	"sync"
	"testing"
)

func TestRequestActivity_DrainOwnersAndCompletion(t *testing.T) {
	var activity RequestActivity
	changes, unsubscribe := activity.Subscribe()
	defer unsubscribe()
	<-changes
	if !activity.AcceptAuxiliary() || activity.Count() != 0 {
		t.Fatal("预热不应产生请求计数")
	}
	lease, ok := activity.TryStart()
	if !ok || activity.Count() != 1 {
		t.Fatal("未登记已受理请求")
	}
	<-changes
	if activity.BeginDrain("tray") != 1 {
		t.Fatal("排空未保留在途请求")
	}
	activity.BeginDrain("update")
	if activity.AcceptAuxiliary() {
		t.Fatal("排空期间仍受理预热")
	}
	if _, ok := activity.TryStart(); ok {
		t.Fatal("排空期间仍受理新请求")
	}
	activity.CancelDrain("tray")
	if _, ok := activity.TryStart(); ok {
		t.Fatal("取消托盘等待解除了更新的排空")
	}
	lease.End()
	lease.End()
	if activity.Count() != 0 {
		t.Fatal("请求没有恰好结束一次")
	}
	<-changes
	activity.CancelDrain("update")
	lease, ok = activity.TryStart()
	if !ok {
		t.Fatal("取消全部等待后没有恢复接收")
	}
	lease.End()
}

func TestRequestActivity_ConcurrentAdmissionAndDrain(t *testing.T) {
	var activity RequestActivity
	start := make(chan struct{})
	var workers sync.WaitGroup
	for range 64 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			if lease, accepted := activity.TryStart(); accepted {
				lease.End()
			}
		}()
	}
	close(start)
	activity.BeginDrain("quit")
	for range 100 {
		if _, accepted := activity.TryStart(); accepted {
			t.Fatal("排空开始后受理了新请求")
		}
	}
	workers.Wait()
	if activity.Count() != 0 {
		t.Fatal("并发结束后仍有请求")
	}
}

func TestRequestActivity_ContinuationRequiresLiveParent(t *testing.T) {
	var activity RequestActivity
	parent, _ := activity.TryStart()
	activity.BeginDrain("quit")
	child, accepted := parent.TryContinue()
	if !accepted || activity.Count() != 2 {
		t.Fatal("已受理请求的续接被排空打断")
	}
	parent.End()
	if _, accepted := parent.TryContinue(); accepted {
		t.Fatal("已完成请求仍能派生新工作")
	}
	if activity.Count() != 1 {
		t.Fatal("父请求结束干扰了续接")
	}
	child.End()
	if activity.Count() != 0 {
		t.Fatal("续接结束后未归零")
	}
}
