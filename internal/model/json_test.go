package model

import (
	"encoding/json"
	"testing"
)

func TestValuesAsJSON(t *testing.T) {
	t.Parallel()
	date, err := ParseDate("2027-03-31")
	if err != nil {
		t.Fatal(err)
	}
	dell := Reference{Type: "dcim.manufacturer", Key: map[string]Value{"name": String("Dell")}}
	tests := []struct {
		v    Value
		want string
	}{
		{String(`a "b"`), `"a \"b\""`},
		{Integer(-3), "-3"},
		{Boolean(true), "true"},
		{Choice("front-to-rear"), `"front-to-rear"`},
		{Null{}, "null"},
		{NewDecimal(15, 1), "1.5"},
		{NewDecimal(-5, 2), "-0.05"},
		{NewDecimal(100, 0), "100"},
		// Beyond what a float64 holds exactly.
		{NewDecimal(1234567890123456789, 2), "12345678901234567.89"},
		{date, `"2027-03-31"`},
		{dell, `{"name":"Dell"}`},
		{Reference{Type: "dcim.manufacturer", ID: 7, Key: dell.Key}, `{"id":7,"name":"Dell"}`},
		{
			Reference{Type: "dcim.devicetype", Key: map[string]Value{"manufacturer": dell, "model": String("R670")}},
			`{"manufacturer":{"name":"Dell"},"model":"R670"}`,
		},
		{Set{String("a"), NewDecimal(5, 1), Null{}}, `["a",0.5,null]`},
	}
	for _, tt := range tests {
		got, err := json.Marshal(tt.v)
		if err != nil {
			t.Errorf("Marshal(%s): %v", tt.v, err)
			continue
		}
		if string(got) != tt.want {
			t.Errorf("Marshal(%s) = %s, want %s", tt.v, got, tt.want)
		}
	}
	if _, err := json.Marshal(Date{}); err == nil {
		t.Error("Marshal(Date{}) succeeded")
	}
}
