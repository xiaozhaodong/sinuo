package store

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestWeeklyRateWindows_Boundaries(t *testing.T) {
	weekdays := []int{1, 2, 3, 4, 5}
	price := 1.0
	s := RateSheet{
		Timezone: "Asia/Shanghai",
		Periods: []RatePeriod{{Name: "低峰", Default: true}, {Name: "高峰", Windows: []RateWindow{
			{weekdays, 540, 720}, {weekdays, 840, 1080}, {[]int{7}, 1380, 30},
		}}},
		Rows: []RateRow{{Period: "低峰", Input: &price, Output: &price}, {Period: "高峰", Input: &price, Output: &price}},
	}
	require.NoError(t, s.validate())
	for _, tc := range []struct{ at, period string }{
		{"2026-11-20T00:59:59Z", "低峰"}, {"2026-11-20T01:00:00Z", "高峰"},
		{"2026-11-20T03:59:59Z", "高峰"}, {"2026-11-20T04:00:00Z", "低峰"},
		{"2026-11-20T06:00:00Z", "高峰"}, {"2026-11-20T10:00:00Z", "低峰"},
		{"2026-11-21T01:00:00Z", "低峰"}, {"2026-11-22T07:00:00Z", "低峰"},
		{"2026-11-22T15:00:00Z", "高峰"}, {"2026-11-22T16:29:59Z", "高峰"},
		{"2026-11-22T16:30:00Z", "低峰"},
	} {
		at, err := time.Parse(time.RFC3339, tc.at)
		require.NoError(t, err)
		require.Equal(t, tc.period, s.PeriodAt(at), tc.at)
	}
}

func TestWeeklyRateWindows_InvalidSchedule(t *testing.T) {
	base := RatePeriod{Name: "默认", Default: true}
	window := func(w ...RateWindow) RatePeriod { return RatePeriod{Name: "窗口", Windows: w} }
	for name, periods := range map[string][]RatePeriod{
		"没有默认档":  {window(RateWindow{[]int{1}, 60, 120})},
		"多个默认档":  {base, base},
		"默认档带窗口": {{Default: true, Windows: []RateWindow{{[]int{1}, 60, 120}}}},
		"缺窗口":    {base, window()},
		"混用每日分界": {base, {Name: "旧档", StartMinute: 60}},
		"缺星期":    {base, window(RateWindow{nil, 20, 30})},
		"星期越界":   {base, window(RateWindow{[]int{8}, 20, 30})},
		"重复星期":   {base, window(RateWindow{[]int{1, 1}, 20, 30})},
		"零长度":    {base, window(RateWindow{[]int{1}, 30, 30})},
		"越界":     {base, window(RateWindow{[]int{1}, 0, 1441})},
		"跨周重叠":   {base, window(RateWindow{[]int{7}, 1380, 60}), {Name: "另一个档", Windows: []RateWindow{{[]int{1}, 0, 30}}}},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := compileWeeklyRateSchedule(periods)
			require.Error(t, err)
		})
	}
}
