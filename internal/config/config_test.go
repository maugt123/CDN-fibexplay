package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func escribir(t *testing.T, contenido string) string {
	t.Helper()
	ruta := filepath.Join(t.TempDir(), "fibex-cdn.env")
	if err := os.WriteFile(ruta, []byte(contenido), 0o600); err != nil {
		t.Fatal(err)
	}
	return ruta
}

const basica = `
# comentario
NODOS=caracas, valencia ,maracay
NODO_CARACAS_URL=http://203.0.113.62
NODO_VALENCIA_URL="http://203.0.113.63:8080"
NODO_MARACAY_URL=http://198.51.100.158 # comentario al final
NODO_VALENCIA_ALIAS=10.0.0.83, vlc.local
NODO_CARACAS_HOST_HEADER=ccs.fibexplay.tv
SALUD_INTERVALO=2s
RAMPA_DURACION=0
`

func TestCargarBasica(t *testing.T) {
	c, err := Cargar(escribir(t, basica))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Nodos) != 3 {
		t.Fatalf("nodos = %d", len(c.Nodos))
	}
	v := c.Nodos[1]
	if v.ID != "valencia" || v.URL.Host != "203.0.113.63:8080" {
		t.Errorf("valencia mal leída: %+v", v)
	}
	if strings.Join(v.Alias, "|") != "10.0.0.83|vlc.local" {
		t.Errorf("alias = %v", v.Alias)
	}
	if c.Nodos[2].URL.Host != "198.51.100.158" {
		t.Errorf("el comentario final no se quitó: %q", c.Nodos[2].URL.Host)
	}
	if c.Nodos[0].HostHeader != "ccs.fibexplay.tv" {
		t.Errorf("host header = %q", c.Nodos[0].HostHeader)
	}
	if c.SaludIntervalo != 2*time.Second || c.Rampa != 0 {
		t.Errorf("duraciones: %v %v", c.SaludIntervalo, c.Rampa)
	}
	if c.Respaldo != "caracas" || c.RutasFuente != "ninguna" || c.Escuchar != ":80" {
		t.Errorf("valores por defecto: %+v", c)
	}
}

func TestEntornoGanaAlArchivo(t *testing.T) {
	t.Setenv("CDN_ESCUCHAR", ":9090")
	c, err := Cargar(escribir(t, basica+"CDN_ESCUCHAR=:8080\n"))
	if err != nil {
		t.Fatal(err)
	}
	if c.Escuchar != ":9090" {
		t.Errorf("Escuchar = %q", c.Escuchar)
	}
}

func TestErroresDeValidacion(t *testing.T) {
	casos := map[string]string{
		"respaldo inexistente":  basica + "NODO_RESPALDO=barquisimeto\n",
		"url con ruta":          strings.Replace(basica, "http://203.0.113.62", "http://203.0.113.62/administracion", 1),
		"url sin esquema":       strings.Replace(basica, "http://203.0.113.62", "203.0.113.62", 1),
		"mysql sin usuario":     basica + "RUTAS_FUENTE=mysql\nRUTAS_MYSQL_HOST=x\n",
		"tabla con inyección":   basica + "RUTAS_FUENTE=mysql\nRUTAS_MYSQL_HOST=x\nRUTAS_MYSQL_USUARIO=u\nRUTAS_MYSQL_TABLA=lines`;drop\n",
		"fuente desconocida":    basica + "RUTAS_FUENTE=excel\n",
		"duración mala":         basica + "SALUD_TIMEOUT=tres\n",
		"host público con http": basica + "CDN_HOST_PUBLICO=http://cdn.fibexplay.tv\n",
		"falta url de nodo":     basica + "NODOS=caracas,valencia,maracay,barquisimeto\n",
	}
	for nombre, contenido := range casos {
		if _, err := Cargar(escribir(t, contenido)); err == nil {
			t.Errorf("%s: se esperaba error", nombre)
		}
	}
}

func TestPlantillaEsValida(t *testing.T) {
	plantilla, err := os.ReadFile("../../fibex-cdn.env.ejemplo")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Cargar(escribir(t, string(plantilla))); err != nil {
		t.Fatalf("la plantilla incluida no carga: %v", err)
	}
}
