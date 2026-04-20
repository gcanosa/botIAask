package irc

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"strings"
)

// EvaluateExpression takes a mathematical string and returns the result as a string.
func EvaluateExpression(exprStr string) (string, error) {
	// Basic sanitation: check for invalid characters to prevent any potential abuse
	// though go/parser is quite safe for simple expressions.
	if strings.ContainsAny(exprStr, "{}[];:=`\"'") {
		return "", fmt.Errorf("invalid characters in expression")
	}

	expr, err := parser.ParseExpr(exprStr)
	if err != nil {
		return "", fmt.Errorf("invalid expression: %v", err)
	}

	result, err := evalAst(expr)
	if err != nil {
		return "", err
	}

	// Format result: remove trailing zeros for cleaner output
	resStr := fmt.Sprintf("%.4f", result)
	resStr = strings.TrimRight(resStr, "0")
	resStr = strings.TrimRight(resStr, ".")
	if resStr == "" {
		resStr = "0"
	}

	return resStr, nil
}

func evalAst(expr ast.Expr) (float64, error) {
	switch x := expr.(type) {
	case *ast.BasicLit:
		if x.Kind == token.INT || x.Kind == token.FLOAT {
			return strconv.ParseFloat(x.Value, 64)
		}
	case *ast.BinaryExpr:
		left, err := evalAst(x.X)
		if err != nil {
			return 0, err
		}
		right, err := evalAst(x.Y)
		if err != nil {
			return 0, err
		}
		switch x.Op {
		case token.ADD:
			return left + right, nil
		case token.SUB:
			return left - right, nil
		case token.MUL:
			return left * right, nil
		case token.QUO:
			if right == 0 {
				return 0, fmt.Errorf("division by zero")
			}
			return left / right, nil
		case token.REM:
			if right == 0 {
				return 0, fmt.Errorf("modulo by zero")
			}
			return float64(int64(left) % int64(right)), nil
		}
	case *ast.ParenExpr:
		return evalAst(x.X)
	case *ast.UnaryExpr:
		val, err := evalAst(x.X)
		if err != nil {
			return 0, err
		}
		switch x.Op {
		case token.SUB:
			return -val, nil
		case token.ADD:
			return val, nil
		}
	}
	return 0, fmt.Errorf("unsupported expression type")
}
