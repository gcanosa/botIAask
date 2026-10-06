package irc

import "testing"

func TestEvaluateExpressionModuloByFraction(t *testing.T) {
	if _, err := EvaluateExpression("5 % 0.5"); err == nil {
		t.Fatal("expected modulo-by-zero error, not a panic")
	}
	if got, err := EvaluateExpression("7 % 4"); err != nil || got != "3" {
		t.Fatalf("7 %% 4 = %q, %v", got, err)
	}
}
