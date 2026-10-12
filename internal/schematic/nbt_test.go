package schematic

import "testing"

func TestNBTOutputIndependentResourceBounds(t *testing.T) {
	t.Run("depth", func(t *testing.T) {
		root := compound(map[string]tag{})
		for range 64 {
			root = compound(map[string]tag{"nested": root})
		}
		if err := validateNBTOutput("", root); err == nil {
			t.Fatal("accepted excessive output depth")
		}
	})
	t.Run("nodes", func(t *testing.T) {
		root := compound(map[string]tag{"list": {kind: 9, element: 1, list: make([]tag, maxNodes)}})
		if err := validateNBTOutput("", root); err == nil {
			t.Fatal("accepted excessive output node count")
		}
	})
	t.Run("bytes boundary", func(t *testing.T) {
		budget := nbtBudget{bytes: maxNBT - 1}
		if err := budget.add(1); err != nil {
			t.Fatal(err)
		}
		if err := budget.add(1); err == nil {
			t.Fatal("accepted excessive output byte count")
		}
	})
	t.Run("malformed array", func(t *testing.T) {
		root := compound(map[string]tag{"words": {kind: 12, raw: []byte{1}}})
		if err := validateNBTOutput("", root); err == nil {
			t.Fatal("accepted partial output long")
		}
	})
	t.Run("list element mismatch", func(t *testing.T) {
		root := compound(map[string]tag{"list": {kind: 9, element: 1, list: []tag{text("wrong")}}})
		if err := validateNBTOutput("", root); err == nil {
			t.Fatal("accepted inconsistent output list")
		}
	})
}
