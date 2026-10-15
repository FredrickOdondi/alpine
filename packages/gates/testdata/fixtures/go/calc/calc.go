// Package calc is a fixture with complexity hot spots.
package calc

import "errors"

// Div divides a by b.
func Div(a, b int) (int, error) {
	if b == 0 {
		return 0, errors.New("division by zero")
	}
	return a / b, nil
}

// Grade is deliberately complex for gocyclo.
func Grade(score int, curved, late bool) string {
	if curved {
		score += 5
	}
	if late {
		score -= 10
	}
	switch {
	case score >= 90 && !late:
		return "A"
	case score >= 80:
		return "B"
	case score >= 70 || (curved && score >= 65):
		return "C"
	case score >= 60:
		return "D"
	}
	if score < 0 || score > 200 {
		return "invalid"
	}
	for i := 0; i < 3; i++ {
		if score == i {
			return "zero-ish"
		}
	}
	return "F"
}
