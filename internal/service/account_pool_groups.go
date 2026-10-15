package service

import (
	"sort"
	"strings"

	"sinuo/internal/store"
)

const (
	accountGroupPrimary = "primary"
	accountGroupBackup  = "backup"
	accountGroupCold    = "cold"
)

func normalizeAccountGroupKey(value string) string {
	switch strings.TrimSpace(strings.ToLower(value)) {
	case accountGroupPrimary:
		return accountGroupPrimary
	case accountGroupBackup:
		return accountGroupBackup
	case accountGroupCold:
		return accountGroupCold
	default:
		return ""
	}
}

func inferAccountGroupKey(record *store.UpstreamAccountRecord) string {
	if record == nil {
		return accountGroupCold
	}
	if normalized := normalizeAccountGroupKey(record.GroupKey); normalized != "" {
		return normalized
	}
	if record.Priority <= 10 {
		return accountGroupPrimary
	}
	if record.Priority <= 20 {
		return accountGroupBackup
	}
	return accountGroupCold
}

func accountGroupRank(groupKey string) int {
	switch normalizeAccountGroupKey(groupKey) {
	case accountGroupPrimary:
		return 1
	case accountGroupBackup:
		return 2
	case accountGroupCold:
		return 3
	default:
		return 9
	}
}

func accountGroupLabel(groupKey string) string {
	switch normalizeAccountGroupKey(groupKey) {
	case accountGroupPrimary:
		return "主组"
	case accountGroupBackup:
		return "备组"
	case accountGroupCold:
		return "冷备"
	default:
		return "未分组"
	}
}

func sortAccountsWithinGroup(accounts []*store.UpstreamAccountRecord) {
	sort.SliceStable(accounts, func(i, j int) bool {
		left := accounts[i]
		right := accounts[j]
		if left == nil {
			return false
		}
		if right == nil {
			return true
		}
		if left.Priority != right.Priority {
			return left.Priority < right.Priority
		}
		return left.ID < right.ID
	})
}
