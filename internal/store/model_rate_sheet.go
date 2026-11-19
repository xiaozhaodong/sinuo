package store

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

// RateSheet 的行号从 1 开始，与已发布的版本 ID 共同唯一标识历史费率行。
type RateSheet struct {
	Timezone      string       `json:"timezone,omitempty"`
	Periods       []RatePeriod `json:"periods,omitempty"`
	ContextLimits []int64      `json:"context_limits,omitempty"`
	Rows          []RateRow    `json:"rows"`
	location      *time.Location
}
type RatePeriod struct {
	Name        string `json:"name"`
	StartMinute int    `json:"start_minute"` // 当地午夜起分钟数，最后一个时段延续到次日第一个时段。
}
type RateRow struct {
	Period    string   `json:"period,omitempty"`
	Tier      int      `json:"tier"`
	Fast      bool     `json:"fast"`
	Input     *float64 `json:"input"`
	Output    *float64 `json:"output"`
	Cache5m   *float64 `json:"cache_5m,omitempty"`
	Cache1h   *float64 `json:"cache_1h,omitempty"`
	Cache30m  *float64 `json:"cache_30m,omitempty"`
	Cache12h  *float64 `json:"cache_12h,omitempty"`
	CacheRead *float64 `json:"cache_read,omitempty"`
}

func (s *RateSheet) PeriodAt(at time.Time) string {
	if len(s.Periods) == 0 {
		return ""
	}
	t := at.In(s.location)
	minute := t.Hour()*60 + t.Minute()
	i := sort.Search(len(s.Periods), func(i int) bool { return s.Periods[i].StartMinute > minute })
	if i == 0 {
		i = len(s.Periods)
	}
	return s.Periods[i-1].Name
}
func (s *RateSheet) Row(period string, tier int, fast bool) (int, *RateRow) {
	for i := range s.Rows {
		r := &s.Rows[i]
		if r.Period == period && r.Tier == tier && r.Fast == fast {
			return i + 1, r
		}
	}
	return 0, nil
}
func validAmount(v *float64) bool {
	return v == nil || (*v >= 0 && !math.IsNaN(*v) && !math.IsInf(*v, 0))
}
func (s *RateSheet) validate() error {
	periods := map[string]bool{"": len(s.Periods) == 0}
	if len(s.Periods) > 0 {
		if s.Timezone == "" || s.Timezone == "Local" {
			return fmt.Errorf("分时价必须指定 IANA 时区")
		}
		loc, err := time.LoadLocation(s.Timezone)
		if err != nil {
			return err
		}
		s.location = loc
	}
	for i, p := range s.Periods {
		if strings.TrimSpace(p.Name) == "" || p.Name != strings.TrimSpace(p.Name) || periods[p.Name] || p.StartMinute < 0 || p.StartMinute >= 1440 || (i > 0 && p.StartMinute <= s.Periods[i-1].StartMinute) {
			return fmt.Errorf("无效或重复的时段 %q", p.Name)
		}
		periods[p.Name] = true
	}
	for i, n := range s.ContextLimits {
		if n <= 0 || (i > 0 && n <= s.ContextLimits[i-1]) {
			return fmt.Errorf("上下文边界必须为严格递增的正整数")
		}
	}
	for i, r := range s.Rows {
		if !periods[r.Period] || r.Tier < 0 || r.Tier > len(s.ContextLimits) || r.Input == nil || r.Output == nil {
			return fmt.Errorf("费率行 %d 缺少单价或档位无效", i+1)
		}
		for _, v := range []*float64{r.Input, r.Output, r.Cache5m, r.Cache1h, r.Cache30m, r.Cache12h, r.CacheRead} {
			if !validAmount(v) {
				return fmt.Errorf("费率行 %d 单价必须是非负有限数", i+1)
			}
		}
		if first, _ := s.Row(r.Period, r.Tier, r.Fast); first != i+1 {
			return fmt.Errorf("费率行 %d 条件重复", i+1)
		}
	}
	for period, enabled := range periods {
		if enabled {
			if _, r := s.Row(period, 0, false); r == nil {
				return fmt.Errorf("时段 %q 缺少标准基础行", period)
			}
		}
	}
	return nil
}

type ChannelTier struct {
	Threshold  *int64   `json:"threshold,omitempty"`
	Multiplier *float64 `json:"multiplier,omitempty"`
}
type ChannelModelRule struct {
	Period         string        `json:"period,omitempty"`
	FastMultiplier *float64      `json:"fast_multiplier,omitempty"`
	Tiers          []ChannelTier `json:"tiers,omitempty"`
}

func (r ChannelModelRule) Limits(official []int64) []int64 {
	if len(r.Tiers) == 0 {
		return official
	}
	limits := make([]int64, max(len(official), len(r.Tiers)))
	copy(limits, official)
	for i, t := range r.Tiers {
		if t.Threshold != nil {
			limits[i] = *t.Threshold
		}
	}
	return limits
}
func (r ChannelModelRule) validate(s *RateSheet) error {
	if r.Period != "" {
		if _, row := s.Row(r.Period, 0, false); row == nil {
			return fmt.Errorf("固定时段 %q 不存在", r.Period)
		}
	}
	if !validAmount(r.FastMultiplier) {
		return fmt.Errorf("Fast 倍率必须为非负有限数")
	}
	for i, t := range r.Tiers {
		if !validAmount(t.Multiplier) {
			return fmt.Errorf("长档倍率必须为非负有限数")
		}
		if i >= len(s.ContextLimits) && (t.Threshold == nil || t.Multiplier == nil) {
			return fmt.Errorf("该模型没有官方长档单价，请填写阈值和倍率或清空该档")
		}
	}
	limits := r.Limits(s.ContextLimits)
	for i, n := range limits {
		if n <= 0 || (i > 0 && n <= limits[i-1]) {
			return fmt.Errorf("渠道上下文边界必须为严格递增的正整数")
		}
	}
	return nil
}

func (s *RateSheet) HasFast() bool {
	for _, r := range s.Rows {
		if r.Fast {
			return true
		}
	}
	return false
}

// HasResponsesWritePrices 表示该版本按写入时长拆分 Responses 输入费。
func (s *RateSheet) HasResponsesWritePrices() bool {
	if s != nil {
		for _, r := range s.Rows {
			if r.Cache30m != nil || r.Cache12h != nil {
				return true
			}
		}
	}
	return false
}
