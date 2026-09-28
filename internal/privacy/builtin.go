package privacy

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	BuiltinEmail        = "builtin:email"
	BuiltinCNMobile     = "builtin:cn_mobile"
	BuiltinCNIDCard     = "builtin:cn_id_card"
	BuiltinBankCardLuhn = "builtin:bank_card_luhn"
)

type builtinMatcher func(seg *segmentText) [][2]int

var (
	emailBuiltinRe               = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`)
	emailImageScaleAssetSuffixRe = regexp.MustCompile(`(?i)@[1-9][0-9]*(?:\.[0-9]+)?x\.(?:png|jpe?g|webp|gif|svg|avif|heic|heif|bmp|tiff?)$`)
)

// 手机号、身份证、银行卡都要求前后不紧邻数字，因此命中只可能是一整个极大数字串
// （手机号可带紧邻的 +86，身份证可带紧邻的末位 X）。三者共用段内一次数字串扫描，
// 不再各跑一遍没有字面量前缀、只能逐字节匹配的正则。
const (
	cnMobileDigits   = 11
	cnIDCardDigits   = 18
	bankCardMinDigit = 13
	bankCardMaxDigit = 19
)

// BuiltinPIIRules 返回默认确定型 PII 内置规则定义。
func BuiltinPIIRules() []Rule {
	return []Rule{
		builtinRule("中国身份证号", "18 位格式、省级地址码、合法出生日期与校验位", BuiltinCNIDCard, "[身份证]", 20),
		builtinRule("银行卡号", "主流卡组织前缀/长度匹配，并通过 Luhn 校验", BuiltinBankCardLuhn, "[银行卡]", 30),
		builtinRule("中国手机号", "中国大陆手机号段格式，支持 +86 前缀", BuiltinCNMobile, "[手机号]", 40),
	}
}

func builtinRule(name, description, pattern, placeholder string, priority int) Rule {
	return Rule{
		Enabled:     true,
		Name:        name,
		Description: description,
		Priority:    priority,
		MatchType:   MatchTypeBuiltin,
		Pattern:     pattern,
		Placeholder: placeholder,
		Action:      ActionRedact,
		Source:      SourceBuiltin,
	}
}

func compileBuiltinMatcher(pattern string) (builtinMatcher, error) {
	switch strings.TrimSpace(pattern) {
	case BuiltinEmail:
		return matchBuiltinEmail, nil
	case BuiltinCNMobile:
		return matchBuiltinCNMobile, nil
	case BuiltinCNIDCard:
		return matchBuiltinCNIDCard, nil
	case BuiltinBankCardLuhn:
		return matchBuiltinBankCardLuhn, nil
	default:
		return nil, fmt.Errorf("unknown builtin pattern: %s", pattern)
	}
}

func matchBuiltinEmail(seg *segmentText) [][2]int {
	text := seg.text
	if strings.IndexByte(text, '@') < 0 {
		return nil
	}
	locs := emailBuiltinRe.FindAllStringIndex(text, -1)
	out := make([][2]int, 0, len(locs))
	for _, loc := range locs {
		if isCommandAddressContext(text, loc[0], loc[1]) {
			continue
		}
		if isImageScaleAssetName(text[loc[0]:loc[1]]) {
			continue
		}
		out = append(out, [2]int{loc[0], loc[1]})
	}
	return out
}

func isImageScaleAssetName(value string) bool {
	return emailImageScaleAssetSuffixRe.MatchString(value)
}

func isCommandAddressContext(text string, start, end int) bool {
	if end < len(text) && text[end] == ':' && strings.HasPrefix(strings.ToLower(text[start:end]), "git@") {
		return true
	}
	lineStart := strings.LastIndexByte(text[:start], '\n') + 1
	prefix := strings.ToLower(strings.TrimSpace(text[lineStart:start]))
	if prefix == "" {
		return false
	}
	fields := strings.Fields(prefix)
	if len(fields) == 0 {
		return false
	}
	last := fields[len(fields)-1]
	return last == "ssh" || last == "scp"
}

func matchBuiltinCNMobile(seg *segmentText) [][2]int {
	text := seg.text
	var out [][2]int
	for _, run := range seg.digitRunSpans() {
		start, end := run[0], run[1]
		switch {
		case end-start == cnMobileDigits && isCNMobileNumber(text[start:end]):
			out = append(out, [2]int{start, end})
		case end-start == cnMobileDigits+2 && hasPlusPrefix(text, start) &&
			strings.HasPrefix(text[start:end], "86") && isCNMobileNumber(text[start+2:end]):
			out = append(out, [2]int{start - 1, end})
		}
	}
	return out
}

// hasPlusPrefix 数字串紧跟在 + 之后，且 + 前面不是数字（+86 前缀本身也要满足数字边界）
func hasPlusPrefix(text string, start int) bool {
	return start > 0 && text[start-1] == '+' && (start == 1 || !isASCIIDigit(text[start-2]))
}

// isCNMobileNumber 11 位大陆手机号段：1740[0-5] 卫星号段，或 13x/15x/18x/19x 等常规号段
func isCNMobileNumber(value string) bool {
	if value[0] != '1' {
		return false
	}
	if strings.HasPrefix(value, "1740") {
		return value[4] <= '5'
	}
	second, third := value[1], value[2]
	switch second {
	case '3', '8':
		return true
	case '4':
		return third == '5' || third == '7'
	case '5', '9':
		return third != '4'
	case '6':
		return third == '2' || (third >= '5' && third <= '7')
	case '7':
		return third != '4' && third != '9'
	default:
		return false
	}
}

func matchBuiltinCNIDCard(seg *segmentText) [][2]int {
	text := seg.text
	var out [][2]int
	for _, run := range seg.digitRunSpans() {
		start, end := run[0], run[1]
		switch {
		case end-start == cnIDCardDigits:
		case end-start == cnIDCardDigits-1 && end < len(text) && (text[end] == 'X' || text[end] == 'x') &&
			(end+1 == len(text) || !isASCIIDigit(text[end+1])):
			end++
		default:
			continue
		}
		value := text[start:end]
		if !validCNIDBirthCentury(value) || !validCNIDCard(value) {
			continue
		}
		out = append(out, [2]int{start, end})
	}
	return out
}

// validCNIDBirthCentury 出生年份限定 18xx / 19xx / 20xx
func validCNIDBirthCentury(value string) bool {
	century := value[6:8]
	return century == "18" || century == "19" || century == "20"
}

func matchBuiltinBankCardLuhn(seg *segmentText) [][2]int {
	text := seg.text
	var out [][2]int
	for _, run := range seg.digitRunSpans() {
		length := run[1] - run[0]
		if length < bankCardMinDigit || length > bankCardMaxDigit {
			continue
		}
		value := text[run[0]:run[1]]
		if !matchesKnownPaymentCardProfile(value) || !validLuhn(value) {
			continue
		}
		out = append(out, run)
	}
	return out
}

func matchesKnownPaymentCardProfile(value string) bool {
	length := len(value)
	switch len(value) {
	case 13, 14, 15, 16, 17, 18, 19:
	default:
		return false
	}

	return matchesVisa(value, length) ||
		matchesMastercard(value, length) ||
		matchesAmericanExpress(value, length) ||
		matchesDinersClub(value, length) ||
		matchesDiscover(value, length) ||
		matchesJCB(value, length) ||
		matchesUnionPay(value, length)
}

func matchesVisa(value string, length int) bool {
	return strings.HasPrefix(value, "4") && hasLength(length, 13, 16, 18, 19)
}

func matchesMastercard(value string, length int) bool {
	if length != 16 {
		return false
	}
	prefix2 := prefixInt(value, 2)
	if prefix2 >= 51 && prefix2 <= 55 {
		return true
	}
	prefix4 := prefixInt(value, 4)
	return prefix4 >= 2221 && prefix4 <= 2720
}

func matchesAmericanExpress(value string, length int) bool {
	if length != 15 {
		return false
	}
	prefix2 := prefixInt(value, 2)
	return prefix2 == 34 || prefix2 == 37
}

func matchesDinersClub(value string, length int) bool {
	if length != 14 {
		return false
	}
	prefix2 := prefixInt(value, 2)
	prefix3 := prefixInt(value, 3)
	return (prefix3 >= 300 && prefix3 <= 305) || prefix2 == 36 || prefix2 == 38 || prefix2 == 39
}

func matchesDiscover(value string, length int) bool {
	if !hasLength(length, 16, 19) {
		return false
	}
	prefix2 := prefixInt(value, 2)
	prefix3 := prefixInt(value, 3)
	prefix4 := prefixInt(value, 4)
	return prefix4 == 6011 || (prefix3 >= 644 && prefix3 <= 649) || prefix2 == 65
}

func matchesJCB(value string, length int) bool {
	prefix4 := prefixInt(value, 4)
	if length == 15 && (prefix4 == 2131 || prefix4 == 1800) {
		return true
	}
	return length >= 16 && length <= 19 && prefix4 >= 3528 && prefix4 <= 3589
}

func matchesUnionPay(value string, length int) bool {
	if length < 14 || length > 19 {
		return false
	}
	prefix3 := prefixInt(value, 3)
	if prefix3 == 620 || (prefix3 >= 623 && prefix3 <= 626) || prefix3 == 810 {
		return true
	}
	prefix4 := prefixInt(value, 4)
	if prefix4 == 6270 || prefix4 == 6272 || prefix4 == 6276 ||
		(prefix4 >= 6282 && prefix4 <= 6289) || prefix4 == 6291 || prefix4 == 6292 ||
		(prefix4 >= 8110 && prefix4 <= 8171) {
		return true
	}
	prefix5 := prefixInt(value, 5)
	if (prefix5 >= 62100 && prefix5 <= 62182) ||
		(prefix5 >= 62184 && prefix5 <= 62197) ||
		(prefix5 >= 62200 && prefix5 <= 62205) ||
		(prefix5 >= 62207 && prefix5 <= 62209) {
		return true
	}
	prefix6 := prefixInt(value, 6)
	return (prefix6 >= 622010 && prefix6 <= 622999) ||
		(prefix6 >= 627700 && prefix6 <= 627779) ||
		(prefix6 >= 627781 && prefix6 <= 627799)
}

func hasLength(length int, allowed ...int) bool {
	for _, item := range allowed {
		if length == item {
			return true
		}
	}
	return false
}

func prefixInt(value string, length int) int {
	if len(value) < length {
		return -1
	}
	prefix, err := strconv.Atoi(value[:length])
	if err != nil {
		return -1
	}
	return prefix
}

func isASCIIDigit(b byte) bool {
	return b >= '0' && b <= '9'
}

func validCNIDCard(value string) bool {
	if len(value) != 18 {
		return false
	}
	if !validCNIDProvinceCode(value[:2]) {
		return false
	}
	birth := value[6:14]
	parsed, err := time.Parse("20060102", birth)
	if err != nil || parsed.Format("20060102") != birth {
		return false
	}
	weights := []int{7, 9, 10, 5, 8, 4, 2, 1, 6, 3, 7, 9, 10, 5, 8, 4, 2}
	checks := []byte{'1', '0', 'X', '9', '8', '7', '6', '5', '4', '3', '2'}
	sum := 0
	for i := 0; i < 17; i++ {
		if !isASCIIDigit(value[i]) {
			return false
		}
		sum += int(value[i]-'0') * weights[i]
	}
	got := value[17]
	if got == 'x' {
		got = 'X'
	}
	return got == checks[sum%11]
}

func validCNIDProvinceCode(value string) bool {
	switch value {
	case "11", "12", "13", "14", "15",
		"21", "22", "23",
		"31", "32", "33", "34", "35", "36", "37",
		"41", "42", "43", "44", "45", "46",
		"50", "51", "52", "53", "54",
		"61", "62", "63", "64", "65",
		"71", "81", "82":
		return true
	default:
		return false
	}
}

func validLuhn(value string) bool {
	sum := 0
	double := false
	for i := len(value) - 1; i >= 0; i-- {
		if !isASCIIDigit(value[i]) {
			return false
		}
		digit := int(value[i] - '0')
		if double {
			digit *= 2
			if digit > 9 {
				digit -= 9
			}
		}
		sum += digit
		double = !double
	}
	return sum > 0 && sum%10 == 0
}
