package canonical

import (
	"testing"
)

func TestMarshalSortsKeysRecursively(t *testing.T) {
	value := map[string]interface{}{
		"z": 1,
		"a": map[string]interface{}{"y": true, "b": "value"},
	}
	got, err := Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"a":{"b":"value","y":true},"z":1}`
	if string(got) != want {
		t.Fatalf("got %s, want %s", got, want)
	}
}
