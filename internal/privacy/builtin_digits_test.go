package privacy

import (
	"fmt"
	"math/rand"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// 旧实现（正则 + 前后不紧邻数字）作为参照，确认共享数字串扫描的结果逐项一致。
var (
	referenceCNMobileRe = regexp.MustCompile(`(?:\+86)?(?:1740[0-5]\d{6}|1(?:[38]\d|4[57]|[59][0-35-9]|6[25-7]|7[0-35-8])\d{8})`)
	referenceCNIDCardRe = regexp.MustCompile(`[1-9]\d{5}(?:18|19|20)\d{2}(?:0[1-9]|1[0-2])(?:0[1-9]|[12]\d|3[01])\d{3}[\dXx]`)
	referenceBankCardRe = regexp.MustCompile(`\d{13,19}`)
)

func referenceDigitMatches(re *regexp.Regexp, text string, valid func(string) bool) [][2]int {
	var out [][2]int
	for _, loc := range re.FindAllStringIndex(text, -1) {
		if (loc[0] > 0 && isASCIIDigit(text[loc[0]-1])) || (loc[1] < len(text) && isASCIIDigit(text[loc[1]])) {
			continue
		}
		if valid != nil && !valid(text[loc[0]:loc[1]]) {
			continue
		}
		out = append(out, [2]int{loc[0], loc[1]})
	}
	return out
}

func referenceBuiltinMatches(pattern, text string) [][2]int {
	switch pattern {
	case BuiltinCNMobile:
		return referenceDigitMatches(referenceCNMobileRe, text, nil)
	case BuiltinCNIDCard:
		return referenceDigitMatches(referenceCNIDCardRe, text, validCNIDCard)
	default:
		return referenceDigitMatches(referenceBankCardRe, text, func(value string) bool {
			return matchesKnownPaymentCardProfile(value) && validLuhn(value)
		})
	}
}

func randomDigits(r *rand.Rand, n int) string {
	var b strings.Builder
	for range n {
		b.WriteByte(byte('0' + r.Intn(10)))
	}
	return b.String()
}

func randomValidIDCard(r *rand.Rand) string {
	provinces := []string{"11", "31", "44", "65", "82"}
	body := provinces[r.Intn(len(provinces))] + randomDigits(r, 4) +
		fmt.Sprintf("%d%02d%02d", 1900+r.Intn(125), 1+r.Intn(12), 1+r.Intn(28)) + randomDigits(r, 3)
	weights := []int{7, 9, 10, 5, 8, 4, 2, 1, 6, 3, 7, 9, 10, 5, 8, 4, 2}
	sum := 0
	for i := range 17 {
		sum += int(body[i]-'0') * weights[i]
	}
	check := "10X98765432"[sum%11]
	if check == 'X' && r.Intn(2) == 0 {
		check = 'x'
	}
	return body + string(check)
}

func randomLuhnCard(r *rand.Rand) string {
	prefixes := []string{"4", "51", "6222", "37", "6011"}
	lengths := []int{16, 16, 19, 15, 16}
	i := r.Intn(len(prefixes))
	body := prefixes[i] + randomDigits(r, lengths[i]-len(prefixes[i])-1)
	for d := byte('0'); d <= '9'; d++ {
		if candidate := body + string(d); validLuhn(candidate) {
			return candidate
		}
	}
	return body + "0"
}

func randomDigitHeavyText(r *rand.Rand) string {
	fragments := []func() string{
		func() string {
			return "1" + []string{"38", "45", "59", "66", "73", "74", "99"}[r.Intn(7)] + randomDigits(r, 8)
		},
		func() string { return "1740" + randomDigits(r, 7) },
		func() string { return randomValidIDCard(r) },
		func() string { return randomLuhnCard(r) },
		func() string { return randomDigits(r, 1+r.Intn(22)) },
		func() string {
			return []string{"", " ", "+", "+86", "86", "X", "x", "a", "-", "8", "\n", "号"}[r.Intn(12)]
		},
	}
	var b strings.Builder
	for range 1 + r.Intn(6) {
		b.WriteString(fragments[r.Intn(len(fragments))]())
	}
	return b.String()
}

func TestBuiltinDigitMatchersMatchReferenceRegex(t *testing.T) {
	matchers := map[string]builtinMatcher{
		BuiltinCNMobile:     matchBuiltinCNMobile,
		BuiltinCNIDCard:     matchBuiltinCNIDCard,
		BuiltinBankCardLuhn: matchBuiltinBankCardLuhn,
	}
	r := rand.New(rand.NewSource(20260928))
	hits := map[string]int{}
	for range 200000 {
		text := randomDigitHeavyText(r)
		for pattern, matcher := range matchers {
			got := matcher(&segmentText{text: text})
			want := referenceBuiltinMatches(pattern, text)
			if len(got) == 0 && len(want) == 0 {
				continue
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("%s mismatch on %q: got %v, want %v", pattern, text, got, want)
			}
			hits[pattern] += len(want)
		}
	}
	for pattern := range matchers {
		if hits[pattern] == 0 {
			t.Fatalf("%s: generator produced no positive samples", pattern)
		}
	}
}

func TestBuiltinDigitMatchersEdgeCases(t *testing.T) {
	cases := []struct {
		pattern string
		text    string
		want    [][2]int
	}{
		{BuiltinCNMobile, "+8613812345678", [][2]int{{0, 14}}},
		{BuiltinCNMobile, "5+8613812345678", nil},
		{BuiltinCNMobile, "8613812345678", nil},
		{BuiltinCNMobile, "+86 13812345678", [][2]int{{4, 15}}},
		{BuiltinCNMobile, "17406123456", nil},
		{BuiltinCNIDCard, "11010519491231002X", [][2]int{{0, 18}}},
		{BuiltinCNIDCard, "11010519491231002X5", nil},
		{BuiltinCNIDCard, "11010517491231002X", nil},
	}
	for _, tc := range cases {
		compiled, err := compileBuiltinMatcher(tc.pattern)
		if err != nil {
			t.Fatal(err)
		}
		got := compiled(&segmentText{text: tc.text})
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s(%q) = %v, want %v", tc.pattern, tc.text, got, tc.want)
		}
		if want := referenceBuiltinMatches(tc.pattern, tc.text); !reflect.DeepEqual(got, want) {
			t.Errorf("%s(%q) diverges from reference: %v vs %v", tc.pattern, tc.text, got, want)
		}
	}
}
