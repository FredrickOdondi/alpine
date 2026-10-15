package calc

import "testing"

func TestDiv(t *testing.T) {
	t.Run("ok", func(t *testing.T) {
		if q, err := Div(6, 3); err != nil || q != 2 {
			t.Errorf("Div(6, 3) = %d, %v", q, err)
		}
	})
	t.Run("by zero", func(t *testing.T) {
		q, err := Div(1, 0)
		if err == nil {
			t.Errorf("Div(1, 0) = %d, want error", q)
		}
		q, err = Div(1, 1)
		if err == nil {
			t.Errorf("Div(1, 1) = %d, want error (deliberate failure)", q)
		}
	})
}
