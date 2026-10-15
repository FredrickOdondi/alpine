// Package util has functions nothing calls (deadcode fixture).
package util

import "strings"

// Reverse is exported but unreachable from main.
func Reverse(s string) string {
	r := []rune(s)
	for i, j := 0, len(r)-1; i < j; i, j = i+1, j-1 {
		r[i], r[j] = r[j], r[i]
	}
	return string(r)
}

// PadLeft is also unreachable.
func PadLeft(s string, n int) string {
	if len(s) >= n {
		return s
	}
	return strings.Repeat(" ", n-len(s)) + s
}
