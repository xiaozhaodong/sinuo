package tray

import (
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 4, 1, 0, 0, 0, time.UTC)

func flow() Motion  { return MotionFlow }
func still() Motion { return MotionStill }
func off() Motion   { return MotionOff }

// run 按帧间隔推进 d，返回期间显示过的帧；遇到静态图标即停止。
func run(a *Animator, from time.Time, d time.Duration) (frames []int, end time.Time) {
	now := from
	for now.Sub(from) < d {
		now = now.Add(FrameInterval)
		frame, _ := a.Advance(now, flow)
		frames = append(frames, frame)
		if frame < 0 {
			break
		}
	}
	return frames, now
}

func TestAnimator_StartsOnlyAfterDelay(t *testing.T) {
	var a Animator
	a.SetActive(true, t0)
	if frame, wait := a.Advance(t0.Add(100*time.Millisecond), flow); frame != -1 || wait != 200*time.Millisecond {
		t.Fatalf("起播前应保持静态并等剩余时间: frame=%d wait=%v", frame, wait)
	}
	if frame, wait := a.Advance(t0.Add(StartDelay), flow); frame != 0 || wait != FrameInterval || !a.Playing() {
		t.Fatalf("满 300ms 应从第 0 帧起播: frame=%d wait=%v", frame, wait)
	}
}

func TestAnimator_QuickRequestNeverStarts(t *testing.T) {
	var a Animator
	a.SetActive(true, t0)
	a.SetActive(false, t0.Add(150*time.Millisecond))
	if frame, wait := a.Advance(t0.Add(StartDelay), flow); frame != -1 || wait != 0 || a.Playing() {
		t.Fatalf("瞬时请求不应触发动画: frame=%d wait=%v", frame, wait)
	}
}

func TestAnimator_TailKeepsPlayingThroughShortGaps(t *testing.T) {
	var a Animator
	a.SetActive(true, t0)
	start := t0.Add(StartDelay)
	a.Advance(start, flow)
	a.SetActive(false, start)
	// 工具调用间隙 400ms，小于 600ms 尾巴：期间一直在播
	frames, now := run(&a, start, 400*time.Millisecond)
	for _, f := range frames {
		if f < 0 {
			t.Fatalf("尾巴期间不应停下: %v", frames)
		}
	}
	a.SetActive(true, now)
	frames, _ = run(&a, now, 2*time.Second)
	for _, f := range frames {
		if f < 0 {
			t.Fatalf("恢复请求后应继续播放: %v", frames)
		}
	}
}

func TestAnimator_StopsAtCycleBoundaryAfterTail(t *testing.T) {
	var a Animator
	a.SetActive(true, t0)
	start := t0.Add(StartDelay)
	a.Advance(start, flow)
	a.SetActive(false, start)
	frames, end := run(&a, start, 5*time.Second)
	if frames[len(frames)-1] != -1 || a.Playing() {
		t.Fatalf("尾巴结束后应回到静态: %v", frames)
	}
	if last := frames[len(frames)-2]; last != FlowFrameCount-1 {
		t.Fatalf("应播完当前周期再停，最后一帧 = %d", last)
	}
	if end.Sub(start) < TailDelay {
		t.Fatalf("不应早于尾巴结束: %v", end.Sub(start))
	}
}

func TestAnimator_ReduceMotionHoldsStillFrame(t *testing.T) {
	var a Animator
	a.SetActive(true, t0)
	if frame, wait := a.Advance(t0.Add(StartDelay), still); frame != StillFrame || wait != 0 {
		t.Fatalf("减少动态效果应固定一帧且不逐帧唤醒: frame=%d wait=%v", frame, wait)
	}
	idleAt := t0.Add(time.Second)
	a.SetActive(false, idleAt)
	if frame, wait := a.Advance(idleAt, still); frame != StillFrame || wait != TailDelay {
		t.Fatalf("静止模式也要等尾巴: frame=%d wait=%v", frame, wait)
	}
	if frame, _ := a.Advance(idleAt.Add(TailDelay), still); frame != -1 || a.Playing() {
		t.Fatalf("尾巴结束应直接回到静态: frame=%d", frame)
	}
}

func TestAnimator_MotionOffStaysStatic(t *testing.T) {
	var a Animator
	a.SetActive(true, t0)
	if frame, wait := a.Advance(t0.Add(time.Second), off); frame != -1 || wait != 0 || a.Playing() {
		t.Fatalf("关闭动画时保持静态: frame=%d wait=%v", frame, wait)
	}
}
