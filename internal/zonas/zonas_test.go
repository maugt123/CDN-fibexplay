package zonas

import "testing"

func TestClasificar(t *testing.T) {
	casos := []struct {
		admin, revendedor, quiere string
	}{
		{"C-123", "", Caracas},
		{"V12345678", "", Valencia},
		{"v minúscula", "", Valencia},
		{"A", "", Maracay},
		{"M-otro", "", Maracay},
		{"X sin letra conocida", "Revendedor VALENCIA centro", Valencia},
		{"", "zona aragua", Maracay},
		{"", "Margarita", Maracay},
		{"", "caracas este", Caracas},
		{"V", "MARACAY", Valencia}, // admin_notes manda sobre reseller_notes
		{"", "", Caracas},
		{"", "sin ciudad", Caracas},
	}
	for _, c := range casos {
		if got := Clasificar(c.admin, c.revendedor); got != c.quiere {
			t.Errorf("Clasificar(%q, %q) = %q, se esperaba %q", c.admin, c.revendedor, got, c.quiere)
		}
	}
}
