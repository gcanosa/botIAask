package httpx

import (
	"strings"
	"testing"
)

func TestDecodeJSONCapsTheBody(t *testing.T) {
	big := `{"x":"` + strings.Repeat("a", 1000) + `"}`
	var v struct{ X string }
	if err := DecodeJSON(strings.NewReader(big), 100, &v); err == nil {
		t.Fatal("body over the cap decoded")
	}
	if err := DecodeJSON(strings.NewReader(big), 4096, &v); err != nil || len(v.X) != 1000 {
		t.Fatalf("under the cap: %v %d", err, len(v.X))
	}
}

func TestDecodeXMLCapsTheBody(t *testing.T) {
	var v struct {
		A string `xml:"a"`
	}
	if err := DecodeXML(strings.NewReader("<r><a>"+strings.Repeat("b", 1000)+"</a></r>"), 50, &v); err == nil {
		t.Fatal("body over the cap decoded")
	}
}
