// Package fibexcdn guarda lo que va dentro del binario: la plantilla de
// configuración y la versión.
package fibexcdn

import _ "embed"

//go:embed fibex-cdn.env.ejemplo
var PlantillaConfig string

// Version se fija al compilar con -ldflags "-X fibex-cdn.Version=1.0.0".
var Version = "dev"
