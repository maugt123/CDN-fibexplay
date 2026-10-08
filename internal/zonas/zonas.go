// Package zonas decide la ciudad de un abonado a partir de sus notas en XUI.
package zonas

import "strings"

const (
	Caracas  = "caracas"
	Valencia = "valencia"
	Maracay  = "maracay"
)

var porPrefijo = map[byte]string{
	'C': Caracas,
	'V': Valencia,
	'A': Maracay,
	'M': Maracay,
}

var porRevendedor = []struct {
	patrones []string
	zona     string
}{
	{[]string{"CARACAS"}, Caracas},
	{[]string{"VALENCIA"}, Valencia},
	{[]string{"ARAGUA", "MARACAY", "MARGARITA"}, Maracay},
}

// Clasificar replica getServerByNotes del orquestador
// (BackendCDNFP/src/services/proxy-route.service.ts): primero la letra inicial
// de admin_notes, después el texto de reseller_notes y, si nada coincide, Caracas.
// Si se cambia la regla allá, hay que cambiarla aquí.
func Clasificar(adminNotes, resellerNotes string) string {
	if adminNotes != "" {
		if z, ok := porPrefijo[aMayuscula(adminNotes[0])]; ok {
			return z
		}
	}
	if resellerNotes != "" {
		texto := strings.ToUpper(resellerNotes)
		for _, regla := range porRevendedor {
			for _, p := range regla.patrones {
				if strings.Contains(texto, p) {
					return regla.zona
				}
			}
		}
	}
	return Caracas
}

func aMayuscula(b byte) byte {
	if b >= 'a' && b <= 'z' {
		return b - ('a' - 'A')
	}
	return b
}
