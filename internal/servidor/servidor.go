// Package servidor atiende a las apps: redirige el video al nodo de la zona
// del abonado y pasa todo lo demás (player_api, EPG...) al nodo de respaldo.
package servidor

import (
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	fibexcdn "fibex-cdn"
	"fibex-cdn/internal/config"
	"fibex-cdn/internal/nodos"
	"fibex-cdn/internal/rutas"
)

type Servidor struct {
	cfg     *config.Config
	grupo   *nodos.Grupo
	rutas   rutas.Fuente
	log     *slog.Logger
	inicio  time.Time
	proxies map[*nodos.Nodo]*httputil.ReverseProxy

	pasadas atomic.Int64
}

func Nuevo(cfg *config.Config, grupo *nodos.Grupo, fuente rutas.Fuente, log *slog.Logger) *Servidor {
	s := &Servidor{
		cfg:     cfg,
		grupo:   grupo,
		rutas:   fuente,
		log:     log,
		inicio:  time.Now(),
		proxies: map[*nodos.Nodo]*httputil.ReverseProxy{},
	}
	transporte := &http.Transport{
		Proxy:                 nil,
		DialContext:           (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		MaxIdleConnsPerHost:   64,
		IdleConnTimeout:       90 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
	}
	for _, n := range grupo.Nodos() {
		s.proxies[n] = s.nuevoProxy(n, transporte)
	}
	return s
}

func (s *Servidor) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/cdn/salud":
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		io.WriteString(w, "ok\n")
		return
	case r.URL.Path == "/cdn/estado":
		s.estado(w)
		return
	case strings.HasPrefix(r.URL.Path, "/cdn/ruta/"):
		s.consultarRuta(w, r)
		return
	}

	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		if usuario, ok := UsuarioDeRuta(r.URL.Path); ok {
			s.redirigir(w, r, usuario)
			return
		}
	}
	s.pasar(w, r, s.grupo.ElegirParaPasar())
}

// prefijosConUsuario son las rutas de XUI con la forma /<tipo>/<usuario>/<clave>/...
var prefijosConUsuario = map[string]bool{
	"live":      true,
	"movie":     true,
	"series":    true,
	"timeshift": true,
}

// UsuarioDeRuta reconoce las URLs de video de XUI y devuelve el usuario:
//
//	/live/<usuario>/<clave>/<id>.m3u8|.ts
//	/movie|series/<usuario>/<clave>/<id>.<ext>
//	/timeshift/<usuario>/<clave>/<duracion>/<inicio>/<id>.ts
//	/<usuario>/<clave>/<id>            (forma corta de canales en vivo)
func UsuarioDeRuta(ruta string) (string, bool) {
	partes := strings.Split(strings.Trim(ruta, "/"), "/")
	if len(partes) >= 4 && prefijosConUsuario[partes[0]] && partes[1] != "" {
		return partes[1], true
	}
	if len(partes) == 3 && partes[0] != "" && esIDStream(partes[2]) && !prefijosConUsuario[partes[0]] {
		return partes[0], true
	}
	return "", false
}

func esIDStream(s string) bool {
	if punto := strings.IndexByte(s, '.'); punto >= 0 {
		s = s[:punto]
	}
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func (s *Servidor) zonaDe(r *http.Request, usuario string) (string, bool) {
	zona, ok := s.rutas.Zona(r.Context(), usuario)
	if !ok || s.grupo.Resolver(zona) == nil {
		return s.cfg.ZonaPorDefecto, false
	}
	return zona, true
}

func (s *Servidor) redirigir(w http.ResponseWriter, r *http.Request, usuario string) {
	zona, _ := s.zonaDe(r, usuario)
	n, motivo := s.grupo.Elegir(zona, usuario)

	// Si el nodo elegido es este mismo servidor (p. ej. NODO_CARACAS_URL usa
	// un dominio que ya apunta al CDN), redirigir o pasar crearía un bucle.
	if s.esEsteServidor(n, r) {
		s.bucle(w, n)
		return
	}

	destino := *n.URL
	destino.Path = r.URL.Path
	destino.RawPath = r.URL.RawPath
	destino.RawQuery = r.URL.RawQuery

	n.ContarRedireccion()
	if motivo != "zona" {
		s.log.Debug("abonado desviado", "usuario", ocultar(usuario), "zona", zona, "nodo", n.ID, "motivo", motivo)
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Fibex-CDN-Nodo", n.ID)
	http.Redirect(w, r, destino.String(), http.StatusFound)
}

func (s *Servidor) esEsteServidor(n *nodos.Nodo, r *http.Request) bool {
	host := strings.ToLower(n.URL.Host)
	return host == strings.ToLower(r.Host) || (s.cfg.HostPublico != "" && host == strings.ToLower(s.cfg.HostPublico))
}

// cabeceraBucle marca las peticiones que el CDN reenvía. Si una vuelve a
// entrar con la marca, algún nodo apunta al propio CDN.
const cabeceraBucle = "X-Fibex-CDN-Paso"

func (s *Servidor) pasar(w http.ResponseWriter, r *http.Request, n *nodos.Nodo) {
	if r.Header.Get(cabeceraBucle) != "" || s.esEsteServidor(n, r) {
		s.bucle(w, n)
		return
	}
	s.pasadas.Add(1)
	s.proxies[n].ServeHTTP(w, r)
}

func (s *Servidor) bucle(w http.ResponseWriter, n *nodos.Nodo) {
	s.log.Error("bucle: el nodo apunta al propio CDN; usa su IP en NODO_<NOMBRE>_URL", "nodo", n.ID, "url", n.URL.String())
	http.Error(w, "bucle de CDN: revisar configuración de nodos", http.StatusLoopDetected)
}

func (s *Servidor) nuevoProxy(n *nodos.Nodo, transporte http.RoundTripper) *httputil.ReverseProxy {
	return &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(n.URL)
			pr.SetXForwarded()
			pr.Out.Header.Set(cabeceraBucle, "1")
			if n.HostHeader != "" {
				pr.Out.Host = n.HostHeader
			}
			if s.cfg.HostPublico != "" && hayQueReescribir(pr.In.URL.Path) {
				// Sin compresión para poder corregir el cuerpo.
				pr.Out.Header.Del("Accept-Encoding")
			}
		},
		ModifyResponse: s.reescribir,
		Transport:      transporte,
		FlushInterval:  -1,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			s.log.Warn("el nodo no respondió a una petición pasada", "nodo", n.ID, "ruta", r.URL.Path, "error", err)
			http.Error(w, "origen no disponible", http.StatusBadGateway)
		},
	}
}

func hayQueReescribir(ruta string) bool {
	return strings.HasSuffix(ruta, "/player_api.php") || strings.HasSuffix(ruta, "/get.php")
}

// reescribir corrige las respuestas que le dicen a la app a qué servidor
// pedir el video, para que siga pidiéndoselo al CDN.
func (s *Servidor) reescribir(resp *http.Response) error {
	if s.cfg.HostPublico == "" || resp.StatusCode != http.StatusOK ||
		!hayQueReescribir(resp.Request.URL.Path) || resp.Header.Get("Content-Encoding") != "" {
		return nil
	}
	cuerpo, err := io.ReadAll(io.LimitReader(resp.Body, 256<<20))
	resp.Body.Close()
	if err != nil {
		return err
	}
	if strings.HasSuffix(resp.Request.URL.Path, "/player_api.php") {
		cuerpo = s.reescribirServerInfo(cuerpo)
	} else {
		cuerpo = s.reescribirHosts(cuerpo)
	}
	resp.Body = io.NopCloser(bytes.NewReader(cuerpo))
	resp.ContentLength = int64(len(cuerpo))
	resp.Header.Set("Content-Length", strconv.Itoa(len(cuerpo)))
	return nil
}

// reescribirServerInfo cambia server_info.url y server_info.port en la
// respuesta de login de player_api.php. Otras respuestas pasan intactas.
func (s *Servidor) reescribirServerInfo(cuerpo []byte) []byte {
	recortado := bytes.TrimSpace(cuerpo)
	if len(recortado) == 0 || recortado[0] != '{' || !bytes.Contains(recortado, []byte(`"server_info"`)) {
		return cuerpo
	}
	var raiz map[string]json.RawMessage
	if json.Unmarshal(recortado, &raiz) != nil {
		return cuerpo
	}
	var info map[string]any
	if json.Unmarshal(raiz["server_info"], &info) != nil || info == nil {
		return cuerpo
	}
	host, puerto := s.hostPuertoPublico()
	info["url"] = host
	info["port"] = puerto
	info["server_protocol"] = "http"
	nuevo, err := json.Marshal(info)
	if err != nil {
		return cuerpo
	}
	raiz["server_info"] = nuevo
	salida, err := json.Marshal(raiz)
	if err != nil {
		return cuerpo
	}
	return salida
}

// reescribirHosts cambia en una lista M3U las direcciones de los nodos por la del CDN.
func (s *Servidor) reescribirHosts(cuerpo []byte) []byte {
	publico := "http://" + s.cfg.HostPublico
	var pares []string
	agregar := func(host string) {
		if host == "" {
			return
		}
		if _, _, err := net.SplitHostPort(host); err != nil {
			pares = append(pares, "http://"+host+":80", publico)
		}
		pares = append(pares, "http://"+host, publico)
	}
	for _, n := range s.grupo.Nodos() {
		agregar(n.URL.Host)
		agregar(n.HostHeader)
	}
	return []byte(strings.NewReplacer(pares...).Replace(string(cuerpo)))
}

func (s *Servidor) hostPuertoPublico() (string, string) {
	if h, p, err := net.SplitHostPort(s.cfg.HostPublico); err == nil {
		return h, p
	}
	return s.cfg.HostPublico, "80"
}

type estadoJSON struct {
	Version  string             `json:"version"`
	Inicio   time.Time          `json:"inicio"`
	Nodos    []nodos.EstadoNodo `json:"nodos"`
	Rutas    rutas.EstadoFuente `json:"rutas"`
	Pasadas  int64              `json:"peticiones_pasadas"`
	Respaldo string             `json:"respaldo"`
	Defecto  string             `json:"zona_por_defecto"`
}

func (s *Servidor) estado(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	e := json.NewEncoder(w)
	e.SetIndent("", "  ")
	e.Encode(estadoJSON{
		Version:  fibexcdn.Version,
		Inicio:   s.inicio,
		Nodos:    s.grupo.Instantanea(),
		Rutas:    s.rutas.Estado(),
		Pasadas:  s.pasadas.Load(),
		Respaldo: s.grupo.Respaldo().ID,
		Defecto:  s.cfg.ZonaPorDefecto,
	})
}

// consultarRuta le dice a soporte a qué nodo va un abonado. Solo existe si se
// configuró CDN_TOKEN_ADMIN, porque revela la zona de un usuario.
func (s *Servidor) consultarRuta(w http.ResponseWriter, r *http.Request) {
	token := r.Header.Get("X-Token-Admin")
	if s.cfg.TokenAdmin == "" || subtle.ConstantTimeCompare([]byte(token), []byte(s.cfg.TokenAdmin)) != 1 {
		http.NotFound(w, r)
		return
	}
	usuario := strings.TrimPrefix(r.URL.Path, "/cdn/ruta/")
	zona, encontrado := s.zonaDe(r, usuario)
	n, motivo := s.grupo.Elegir(zona, usuario)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(map[string]any{
		"usuario":    usuario,
		"zona":       zona,
		"encontrado": encontrado,
		"nodo":       n.ID,
		"motivo":     motivo,
	})
}

// ocultar deja ver solo el comienzo del usuario en los logs.
func ocultar(u string) string {
	if len(u) <= 4 {
		return "****"
	}
	return u[:4] + strings.Repeat("*", len(u)-4)
}
