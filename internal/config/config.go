// Package config lee el archivo fibex-cdn.env y las variables de entorno.
package config

import (
	"bufio"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type Nodo struct {
	ID         string
	URL        *url.URL
	HostHeader string
	Alias      []string
}

type MySQL struct {
	Host    string
	Puerto  int
	Usuario string
	Clave   string
	BD      string
	Tabla   string
}

type Config struct {
	Escuchar    string
	HostPublico string

	Nodos          []Nodo
	Respaldo       string
	ZonaPorDefecto string

	SaludModo        string
	SaludRuta        string
	SaludIntervalo   time.Duration
	SaludTimeout     time.Duration
	FallosParaCaer   int
	ExitosParaVolver int
	Rampa            time.Duration

	RutasFuente          string
	MySQL                MySQL
	RedisURL             string
	RedisPrefijo         string
	RedisCache           time.Duration
	RutasArchivo         string
	RutasRefresco        time.Duration
	RutasRecargaCompleta time.Duration

	TokenAdmin string
	LogNivel   string
}

var (
	reNombreNodo = regexp.MustCompile(`^[a-z0-9_]+$`)
	reTabla      = regexp.MustCompile(`^[A-Za-z0-9_]+$`)
)

// LeerArchivo interpreta líneas CLAVE=valor. Admite comillas y comentarios
// al final de la línea (" # ...") cuando el valor no va entre comillas.
func LeerArchivo(ruta string) (map[string]string, error) {
	f, err := os.Open(ruta)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	vals := map[string]string{}
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		linea := strings.TrimSpace(strings.TrimPrefix(sc.Text(), "\xef\xbb\xbf"))
		if linea == "" || strings.HasPrefix(linea, "#") {
			continue
		}
		clave, valor, ok := strings.Cut(linea, "=")
		if !ok {
			return nil, fmt.Errorf("%s línea %d: falta '=' en %q", ruta, n, linea)
		}
		clave = strings.TrimSpace(strings.TrimPrefix(clave, "export "))
		vals[clave] = limpiarValor(strings.TrimSpace(valor))
	}
	return vals, sc.Err()
}

func limpiarValor(v string) string {
	if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') {
		if fin := strings.IndexByte(v[1:], v[0]); fin >= 0 {
			return v[1 : fin+1]
		}
	}
	if i := strings.Index(v, " #"); i >= 0 {
		v = v[:i]
	}
	return strings.TrimSpace(v)
}

// Cargar arma la configuración desde el archivo (si ruta no está vacía) y el
// entorno, y la valida.
func Cargar(ruta string) (*Config, error) {
	archivo := map[string]string{}
	if ruta != "" {
		v, err := LeerArchivo(ruta)
		if err != nil {
			return nil, err
		}
		archivo = v
	}
	l := lector{archivo: archivo}

	c := &Config{
		Escuchar:    l.texto("CDN_ESCUCHAR", ":80"),
		HostPublico: l.texto("CDN_HOST_PUBLICO", ""),

		Respaldo:       strings.ToLower(l.texto("NODO_RESPALDO", "caracas")),
		ZonaPorDefecto: strings.ToLower(l.texto("ZONA_POR_DEFECTO", "caracas")),

		SaludModo:        strings.ToLower(l.texto("SALUD_MODO", "tcp")),
		SaludRuta:        l.texto("SALUD_RUTA", "/"),
		SaludIntervalo:   l.duracion("SALUD_INTERVALO", 5*time.Second),
		SaludTimeout:     l.duracion("SALUD_TIMEOUT", 3*time.Second),
		FallosParaCaer:   l.entero("SALUD_FALLOS_PARA_CAER", 3),
		ExitosParaVolver: l.entero("SALUD_EXITOS_PARA_VOLVER", 6),
		Rampa:            l.duracion("RAMPA_DURACION", 9*time.Minute),

		RutasFuente: strings.ToLower(l.texto("RUTAS_FUENTE", "ninguna")),
		MySQL: MySQL{
			Host:    l.texto("RUTAS_MYSQL_HOST", ""),
			Puerto:  l.entero("RUTAS_MYSQL_PUERTO", 3306),
			Usuario: l.texto("RUTAS_MYSQL_USUARIO", ""),
			Clave:   l.texto("RUTAS_MYSQL_CLAVE", ""),
			BD:      l.texto("RUTAS_MYSQL_BD", "xui"),
			Tabla:   l.texto("RUTAS_MYSQL_TABLA", "lines"),
		},
		RedisURL:             l.texto("RUTAS_REDIS_URL", ""),
		RedisPrefijo:         l.texto("RUTAS_REDIS_PREFIJO", "proxy:route:"),
		RedisCache:           l.duracion("RUTAS_REDIS_CACHE", 30*time.Second),
		RutasArchivo:         l.texto("RUTAS_ARCHIVO", ""),
		RutasRefresco:        l.duracion("RUTAS_REFRESCO", 60*time.Second),
		RutasRecargaCompleta: l.duracion("RUTAS_RECARGA_COMPLETA", time.Hour),

		TokenAdmin: l.texto("CDN_TOKEN_ADMIN", ""),
		LogNivel:   strings.ToLower(l.texto("LOG_NIVEL", "info")),
	}

	for _, nombre := range strings.Split(l.texto("NODOS", "caracas,valencia,maracay"), ",") {
		nombre = strings.ToLower(strings.TrimSpace(nombre))
		if nombre == "" {
			continue
		}
		if !reNombreNodo.MatchString(nombre) {
			l.errorf("NODOS: nombre inválido %q (solo minúsculas, números y _)", nombre)
			continue
		}
		pre := "NODO_" + strings.ToUpper(nombre) + "_"
		crudo := l.texto(pre+"URL", "")
		if crudo == "" {
			l.errorf("falta %sURL", pre)
			continue
		}
		u, err := url.Parse(crudo)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			l.errorf("%sURL=%q no es una URL http(s) válida", pre, crudo)
			continue
		}
		if u.Path != "" && u.Path != "/" {
			l.errorf("%sURL=%q no debe llevar ruta, solo esquema, host y puerto", pre, crudo)
			continue
		}
		u.Path = ""
		var alias []string
		for _, a := range strings.Split(l.texto(pre+"ALIAS", ""), ",") {
			if a = strings.TrimSpace(a); a != "" {
				alias = append(alias, a)
			}
		}
		c.Nodos = append(c.Nodos, Nodo{
			ID:         nombre,
			URL:        u,
			HostHeader: l.texto(pre+"HOST_HEADER", ""),
			Alias:      alias,
		})
	}

	l.validar(c)
	if len(l.errores) > 0 {
		return nil, fmt.Errorf("configuración inválida:\n  - %s", strings.Join(l.errores, "\n  - "))
	}
	return c, nil
}

func (c *Config) TieneNodo(id string) bool {
	for _, n := range c.Nodos {
		if n.ID == id {
			return true
		}
	}
	return false
}

type lector struct {
	archivo map[string]string
	errores []string
}

func (l *lector) errorf(formato string, args ...any) {
	l.errores = append(l.errores, fmt.Sprintf(formato, args...))
}

func (l *lector) texto(clave, porDefecto string) string {
	if v, ok := os.LookupEnv(clave); ok {
		return strings.TrimSpace(v)
	}
	if v, ok := l.archivo[clave]; ok {
		return v
	}
	return porDefecto
}

func (l *lector) entero(clave string, porDefecto int) int {
	v := l.texto(clave, "")
	if v == "" {
		return porDefecto
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		l.errorf("%s=%q debe ser un número entero", clave, v)
		return porDefecto
	}
	return n
}

func (l *lector) duracion(clave string, porDefecto time.Duration) time.Duration {
	v := l.texto(clave, "")
	if v == "" {
		return porDefecto
	}
	if v == "0" {
		return 0
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		l.errorf("%s=%q debe ser una duración como 30s, 5m o 1h", clave, v)
		return porDefecto
	}
	return d
}

func (l *lector) validar(c *Config) {
	if len(c.Nodos) == 0 {
		l.errorf("NODOS no define ningún nodo válido")
	}
	if !c.TieneNodo(c.Respaldo) {
		l.errorf("NODO_RESPALDO=%q no está en NODOS", c.Respaldo)
	}
	if !c.TieneNodo(c.ZonaPorDefecto) {
		l.errorf("ZONA_POR_DEFECTO=%q no está en NODOS", c.ZonaPorDefecto)
	}
	if c.SaludModo != "tcp" && c.SaludModo != "http" {
		l.errorf("SALUD_MODO debe ser tcp o http")
	}
	if c.SaludIntervalo <= 0 || c.SaludTimeout <= 0 {
		l.errorf("SALUD_INTERVALO y SALUD_TIMEOUT deben ser mayores que cero")
	}
	if c.FallosParaCaer < 1 || c.ExitosParaVolver < 1 {
		l.errorf("SALUD_FALLOS_PARA_CAER y SALUD_EXITOS_PARA_VOLVER deben ser al menos 1")
	}
	if c.Rampa < 0 {
		l.errorf("RAMPA_DURACION no puede ser negativa")
	}
	if c.RutasRefresco <= 0 || c.RutasRecargaCompleta <= 0 {
		l.errorf("RUTAS_REFRESCO y RUTAS_RECARGA_COMPLETA deben ser mayores que cero")
	}
	if strings.Contains(c.HostPublico, "/") {
		l.errorf("CDN_HOST_PUBLICO va sin http:// ni rutas, ej. cdn.fibexplay.tv o 203.0.113.10:8080")
	}
	for _, n := range c.Nodos {
		if c.HostPublico != "" && (strings.EqualFold(n.URL.Host, c.HostPublico) || strings.EqualFold(n.URL.Hostname(), c.HostPublico)) {
			l.errorf("NODO_%s_URL apunta al propio CDN (%s); usa la IP del nodo", strings.ToUpper(n.ID), c.HostPublico)
		}
	}

	switch c.RutasFuente {
	case "ninguna":
	case "mysql":
		if c.MySQL.Host == "" || c.MySQL.Usuario == "" || c.MySQL.BD == "" {
			l.errorf("RUTAS_FUENTE=mysql necesita RUTAS_MYSQL_HOST, RUTAS_MYSQL_USUARIO y RUTAS_MYSQL_BD")
		}
		if !reTabla.MatchString(c.MySQL.Tabla) {
			l.errorf("RUTAS_MYSQL_TABLA=%q no es un nombre de tabla válido", c.MySQL.Tabla)
		}
	case "redis":
		if c.RedisURL == "" {
			l.errorf("RUTAS_FUENTE=redis necesita RUTAS_REDIS_URL")
		}
	case "archivo":
		if c.RutasArchivo == "" {
			l.errorf("RUTAS_FUENTE=archivo necesita RUTAS_ARCHIVO")
		}
	default:
		l.errorf("RUTAS_FUENTE=%q debe ser mysql, redis, archivo o ninguna", c.RutasFuente)
	}

	switch c.LogNivel {
	case "debug", "info", "warn", "error":
	default:
		l.errorf("LOG_NIVEL debe ser debug, info, warn o error")
	}
}
