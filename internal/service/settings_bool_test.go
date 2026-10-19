package service

import "testing"

func TestParseBool_SettingsSpellings(t *testing.T) {
	tests := []struct {
		value        string
		defaultValue bool
		want         bool
	}{
		{value: "true", want: true},
		{value: " YES ", want: true},
		{value: "1", want: true},
		{value: "false", want: false},
		{value: "0", want: false},
		{value: "no", want: false},
		{value: "", defaultValue: true, want: true},
		{value: "unknown", defaultValue: true, want: false},
	}
	for _, tt := range tests {
		if got := ParseBool(tt.value, tt.defaultValue); got != tt.want {
			t.Errorf("ParseBool(%q, %t) = %t, want %t", tt.value, tt.defaultValue, got, tt.want)
		}
	}
}
