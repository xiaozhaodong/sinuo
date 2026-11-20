package store

import (
	"fmt"
	"sort"
	"time"
)

// RateWindow 按价表时区匹配；星期一为 1，星期日为 7。跨午夜窗口归开始日。
type RateWindow struct {
	Weekdays    []int `json:"weekdays"`
	StartMinute int   `json:"start_minute"`
	EndMinute   int   `json:"end_minute"` // 右边界，1440 表示午夜。
}

type weeklyRateSpan struct {
	start, end int
	period     string
}

// 以 Go 的 Weekday 编号索引本地星期；只在读取或发布版本时编译。
type weeklyRateSchedule [7][]weeklyRateSpan

func compileWeeklyRateSchedule(periods []RatePeriod) (*weeklyRateSchedule, error) {
	if len(periods) == 0 || !periods[0].Default {
		for _, p := range periods {
			if p.Default || len(p.Windows) > 0 {
				return nil, fmt.Errorf("按星期计费时，首档必须为默认档")
			}
		}
		return nil, nil
	}
	var schedule weeklyRateSchedule
	for i, p := range periods {
		if p.StartMinute != 0 || (i > 0 && p.Default) {
			return nil, fmt.Errorf("星期窗口不能混用每日分界，且只能指定一个默认档")
		}
		if p.Default == (len(p.Windows) > 0) {
			return nil, fmt.Errorf("默认档无需窗口，其他价格档至少需要一个窗口：%q", p.Name)
		}
		for _, w := range p.Windows {
			if len(w.Weekdays) == 0 || w.StartMinute < 0 || w.StartMinute >= 1440 ||
				w.EndMinute < 1 || w.EndMinute > 1440 || w.StartMinute == w.EndMinute {
				return nil, fmt.Errorf("价格档 %q 的星期或时间范围无效", p.Name)
			}
			for _, day := range w.Weekdays {
				if day < 1 || day > 7 {
					return nil, fmt.Errorf("价格档 %q 的星期须为 1 至 7", p.Name)
				}
				index := day % 7
				end := w.EndMinute
				if end < w.StartMinute {
					next := (index + 1) % 7
					schedule[next] = append(schedule[next], weeklyRateSpan{0, end, p.Name})
					end = 1440
				}
				schedule[index] = append(schedule[index], weeklyRateSpan{w.StartMinute, end, p.Name})
			}
		}
	}
	for _, spans := range schedule {
		sort.Slice(spans, func(i, j int) bool { return spans[i].start < spans[j].start })
		for i := 1; i < len(spans); i++ {
			if spans[i].start < spans[i-1].end {
				return nil, fmt.Errorf("价格档 %q 与 %q 的星期时间窗口重叠", spans[i-1].period, spans[i].period)
			}
		}
	}
	return &schedule, nil
}

func (s *weeklyRateSchedule) periodAt(day time.Weekday, minute int, defaultPeriod string) string {
	spans := s[day]
	i := sort.Search(len(spans), func(i int) bool { return spans[i].end > minute })
	if i < len(spans) && spans[i].start <= minute {
		return spans[i].period
	}
	return defaultPeriod
}
