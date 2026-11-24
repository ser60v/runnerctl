package cli

import (
	"errors"
	"fmt"
	"math/big"
	"strings"
)

// parseSize returns an exact number of MiB. A bare integer is legacy MiB.
func parseSize(input string) (int, error) {
	bad := errors.New("size must be a positive whole number of MiB (for example 8gb or 40.125gb)")
	input = strings.TrimSpace(input)
	if input == "" {
		return 0, bad
	}
	cut := len(input)
	for cut > 0 && ((input[cut-1] >= 'a' && input[cut-1] <= 'z') || (input[cut-1] >= 'A' && input[cut-1] <= 'Z')) {
		cut--
	}
	number, unit := strings.TrimSpace(input[:cut]), strings.ToLower(input[cut:])
	multiplier := int64(1)
	switch unit {
	case "", "mb", "mib":
	case "gb", "gib":
		multiplier = 1024
	case "tb", "tib":
		multiplier = 1024 * 1024
	default:
		return 0, bad
	}
	if strings.Contains(number, ",") {
		if strings.Contains(number, ".") || strings.Count(number, ",") != 1 {
			return 0, bad
		}
		number = strings.Replace(number, ",", ".", 1)
	}
	if number == "" || (unit == "" && strings.Contains(number, ".")) {
		return 0, bad
	}
	whole, fraction, hasDot := strings.Cut(number, ".")
	if whole == "" || (hasDot && fraction == "") {
		return 0, bad
	}
	for _, part := range []string{whole, fraction} {
		for _, digit := range part {
			if digit < '0' || digit > '9' {
				return 0, bad
			}
		}
	}
	// Limit input length before allocating big integers. This is far beyond int size.
	if len(whole)+len(fraction) > 128 {
		return 0, bad
	}
	numerator, ok := new(big.Int).SetString(whole+fraction, 10)
	if !ok {
		return 0, bad
	}
	numerator.Mul(numerator, big.NewInt(multiplier))
	denominator := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(len(fraction))), nil)
	quotient, remainder := new(big.Int).QuoRem(numerator, denominator, new(big.Int))
	if remainder.Sign() != 0 || quotient.Sign() <= 0 || !quotient.IsInt64() || quotient.Int64() > int64(^uint(0)>>1) {
		return 0, bad
	}
	return int(quotient.Int64()), nil
}

// formatSize uses at most three decimal places when that keeps a larger unit exact.
func formatSize(mib int) string {
	// A power-of-two scale can be expressed in at most three decimal
	// places exactly when the number of MiB is divisible by scale/8.
	for _, unit := range []struct {
		scale int
		label string
	}{{1024 * 1024, "tb"}, {1024, "gb"}} {
		if mib >= unit.scale && mib%(unit.scale/8) == 0 {
			whole, eighths := mib/unit.scale, mib%(unit.scale)/(unit.scale/8)
			if eighths == 0 {
				return fmt.Sprintf("%d%s", whole, unit.label)
			}
			return fmt.Sprintf("%d.%s%s", whole, strings.TrimRight(fmt.Sprintf("%03d", eighths*125), "0"), unit.label)
		}
	}
	return fmt.Sprintf("%dmb", mib)
}
