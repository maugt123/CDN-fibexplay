package servidor

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"fibex-cdn/internal/config"
	"fibex-cdn/internal/nodos"
	"fibex-cdn/internal/rutas"
)

func TestUsuarioDeRuta(t *testing.T) {
	casos := map[string]string{
		"/live/12345678/clave/586.m3u8":             "12345678",
		"/live/12345678/clave/586.ts":               "12345678",
		"/movie/V123/clave/99.mp4":                  "V123",
		"/series/u/p/12.mkv":                        "u",
		"/timeshift/u/p/60/2026-10-07:12-00/586.ts": "u",
		"/12345678/clave/586":                       "12345678",
		"/12345678/clave/586.ts":                    "12345678",
	}
	for ruta, quiere := range casos {
		if got, ok := UsuarioDeRuta(ruta); !ok || got != quiere {
			t.Errorf("UsuarioDeRuta(%q) = %q,%v; se esperaba %q", ruta, got, ok, quiere)
		}
	}
	for _, ruta := range []string{
		"/player_api.php", "/xmltv.php", "/get.php", "/administracion/x/y",
		"/live/stream/586.m3u8", "/auth/586.m3u8", "/hls/abc/586_1.ts", "/", "/live//p/1.ts",
		"/a/b/c", "/live/u/p",
	} {
		if got, ok := UsuarioDeRuta(ruta); ok {
			t.Errorf("UsuarioDeRuta(%q) no debería reconocer usuario (dio %q)", ruta, got)
		}
	}
}

type mapa map[string]string

func (m mapa) Zona(_ context.Context, u string) (string, bool) { z, ok := m[u]; return z, ok }
func (mapa) Iniciar(context.Context) error                     { return nil }
func (m mapa) Estado() rutas.EstadoFuente {
	return rutas.EstadoFuente{Fuente: "prueba", Usuarios: len(m)}
}

type entorno struct {
	cdn                        *httptest.Server
	caracas, valencia, maracay *httptest.Server
	grupo                      *nodos.Grupo
	cfg                        *config.Config
	peticionesCaracas          chan *http.Request
}

func nuevoEntorno(t *testing.T, hostPublico string) *entorno {
	t.Helper()
	e := &entorno{peticionesCaracas: make(chan *http.Request, 10)}
	e.caracas = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case e.peticionesCaracas <- r.Clone(context.Background()):
		default:
		}
		switch r.URL.Path {
		case "/player_api.php":
			if r.URL.Query().Get("action") != "" {
				io.WriteString(w, `[{"stream_id":586,"name":"Prueba Interna"}]`)
				return
			}
			io.WriteString(w, `{"user_info":{"username":"12345678","auth":1},"server_info":{"url":"ccs.fibexplay.tv","port":"80","https_port":"443","server_protocol":"http","timezone":"America/Caracas"}}`)
		case "/get.php":
			io.WriteString(w, "#EXTM3U\n#EXTINF:-1,Canal\nhttp://ccs.fibexplay.tv:80/live/u/p/586.ts\nhttp://ccs.fibexplay.tv/live/u/p/587.ts\n")
		default:
			io.WriteString(w, "caracas")
		}
	}))
	nodo := func(nombre string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, nombre) }))
	}
	e.valencia, e.maracay = nodo("valencia"), nodo("maracay")
	t.Cleanup(func() { e.caracas.Close(); e.valencia.Close(); e.maracay.Close() })

	mk := func(id string, s *httptest.Server, hostHeader string, alias ...string) config.Nodo {
		u, _ := url.Parse(s.URL)
		return config.Nodo{ID: id, URL: u, HostHeader: hostHeader, Alias: alias}
	}
	e.cfg = &config.Config{
		HostPublico: hostPublico,
		Nodos: []config.Nodo{
			mk("caracas", e.caracas, "ccs.fibexplay.tv", "ccs.fibexplay.tv"),
			mk("valencia", e.valencia, "", "10.0.0.83"),
			mk("maracay", e.maracay, ""),
		},
		Respaldo:         "caracas",
		ZonaPorDefecto:   "caracas",
		SaludModo:        "tcp",
		SaludIntervalo:   time.Hour,
		SaludTimeout:     time.Second,
		FallosParaCaer:   1,
		ExitosParaVolver: 1,
		TokenAdmin:       "secreto",
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	e.grupo = nodos.NuevoGrupo(e.cfg, log)
	e.grupo.Sondear(context.Background())
	fuente := mapa{"vlc1": "valencia", "vlc2": "10.0.0.83", "mcy1": "maracay", "raro": "barquisimeto"}
	e.cdn = httptest.NewServer(Nuevo(e.cfg, e.grupo, fuente, log))
	t.Cleanup(e.cdn.Close)
	return e
}

var sinSeguir = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

func (e *entorno) pedir(t *testing.T, ruta string) *http.Response {
	t.Helper()
	resp, err := sinSeguir.Get(e.cdn.URL + ruta)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func TestRedireccionPorZona(t *testing.T) {
	e := nuevoEntorno(t, "")
	casos := []struct {
		ruta string
		nodo *httptest.Server
		id   string
	}{
		{"/live/vlc1/clave/586.m3u8?token=a%2Fb", e.valencia, "valencia"},
		{"/live/vlc2/clave/586.ts", e.valencia, "valencia"}, // ruta escrita con IP privada
		{"/mcy1/clave/586", e.maracay, "maracay"},
		{"/movie/desconocido/clave/9.mp4", e.caracas, "caracas"},
		{"/live/raro/clave/1.ts", e.caracas, "caracas"}, // zona que no es un nodo
	}
	for _, c := range casos {
		resp := e.pedir(t, c.ruta)
		if resp.StatusCode != http.StatusFound {
			t.Fatalf("%s: código %d", c.ruta, resp.StatusCode)
		}
		quiere := c.nodo.URL + c.ruta
		if loc := resp.Header.Get("Location"); loc != quiere {
			t.Errorf("%s: Location = %s, se esperaba %s", c.ruta, loc, quiere)
		}
		if resp.Header.Get("X-Fibex-CDN-Nodo") != c.id || resp.Header.Get("Cache-Control") != "no-store" {
			t.Errorf("%s: cabeceras %v", c.ruta, resp.Header)
		}
	}
}

func TestRespaldoCuandoCaeValencia(t *testing.T) {
	e := nuevoEntorno(t, "")
	e.valencia.Close()
	e.grupo.Sondear(context.Background())

	resp := e.pedir(t, "/live/vlc1/clave/586.m3u8")
	if !strings.HasPrefix(resp.Header.Get("Location"), e.caracas.URL) {
		t.Fatalf("con Valencia caída debe ir a Caracas, fue %s", resp.Header.Get("Location"))
	}
	resp = e.pedir(t, "/live/mcy1/clave/586.m3u8")
	if !strings.HasPrefix(resp.Header.Get("Location"), e.maracay.URL) {
		t.Fatal("Maracay no debe verse afectada")
	}
}

func TestPasaLoQueNoEsVideo(t *testing.T) {
	e := nuevoEntorno(t, "")
	resp := e.pedir(t, "/player_api.php?username=vlc1&password=x")
	cuerpo, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || !strings.Contains(string(cuerpo), `"url":"ccs.fibexplay.tv"`) {
		t.Fatalf("sin CDN_HOST_PUBLICO debe pasar intacto: %d %s", resp.StatusCode, cuerpo)
	}
	r := <-e.peticionesCaracas
	if r.Host != "ccs.fibexplay.tv" {
		t.Errorf("a Caracas debe llegar Host ccs.fibexplay.tv, llegó %q", r.Host)
	}
	if r.Header.Get("X-Forwarded-For") == "" {
		t.Error("falta X-Forwarded-For con la IP real del abonado")
	}
}

func TestReescribePlayerAPIyM3U(t *testing.T) {
	e := nuevoEntorno(t, "cdn.fibexplay.tv:8080")

	resp := e.pedir(t, "/player_api.php?username=vlc1&password=x")
	var r struct {
		ServerInfo map[string]any `json:"server_info"`
		UserInfo   map[string]any `json:"user_info"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		t.Fatal(err)
	}
	if r.ServerInfo["url"] != "cdn.fibexplay.tv" || r.ServerInfo["port"] != "8080" || r.ServerInfo["timezone"] != "America/Caracas" {
		t.Errorf("server_info = %v", r.ServerInfo)
	}
	if r.UserInfo["username"] != "12345678" {
		t.Errorf("user_info alterado: %v", r.UserInfo)
	}

	resp = e.pedir(t, "/player_api.php?username=vlc1&password=x&action=get_live_streams")
	cuerpo, _ := io.ReadAll(resp.Body)
	if string(cuerpo) != `[{"stream_id":586,"name":"Prueba Interna"}]` {
		t.Errorf("las listas de canales no se tocan: %s", cuerpo)
	}

	resp = e.pedir(t, "/get.php?username=vlc1&password=x&type=m3u_plus")
	cuerpo, _ = io.ReadAll(resp.Body)
	if strings.Contains(string(cuerpo), "ccs.fibexplay.tv") ||
		!strings.Contains(string(cuerpo), "http://cdn.fibexplay.tv:8080/live/u/p/586.ts") ||
		!strings.Contains(string(cuerpo), "http://cdn.fibexplay.tv:8080/live/u/p/587.ts") {
		t.Errorf("M3U mal reescrita:\n%s", cuerpo)
	}
}

func TestNoRedirigeASiMismo(t *testing.T) {
	e := nuevoEntorno(t, "")
	// Simula que el nodo de Valencia apunta por error al propio CDN.
	u, _ := url.Parse(e.cdn.URL)
	if n := e.grupo.Resolver("valencia"); n != nil {
		n.URL = u
	}
	resp := e.pedir(t, "/live/vlc1/clave/586.m3u8")
	if resp.StatusCode != http.StatusLoopDetected {
		t.Fatalf("debe cortar el bucle con 508, dio %d", resp.StatusCode)
	}

	// Caracas apunta al CDN y la petición no es de video: tampoco debe girar.
	if n := e.grupo.Resolver("caracas"); n != nil {
		n.URL = u
	}
	resp = e.pedir(t, "/player_api.php")
	if resp.StatusCode != http.StatusLoopDetected {
		t.Fatalf("pasar a sí mismo debe dar 508, dio %d", resp.StatusCode)
	}
}

func TestEstadoYRuta(t *testing.T) {
	e := nuevoEntorno(t, "")
	e.pedir(t, "/live/vlc1/clave/586.m3u8")

	resp := e.pedir(t, "/cdn/estado")
	var est struct {
		Nodos []nodos.EstadoNodo `json:"nodos"`
		Rutas rutas.EstadoFuente `json:"rutas"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&est); err != nil {
		t.Fatal(err)
	}
	if len(est.Nodos) != 3 || est.Nodos[1].Redirecciones != 1 || est.Rutas.Usuarios != 4 {
		t.Errorf("estado = %+v", est)
	}

	if resp := e.pedir(t, "/cdn/ruta/vlc1"); resp.StatusCode != http.StatusNotFound {
		t.Error("sin token /cdn/ruta debe responder 404")
	}
	req, _ := http.NewRequest(http.MethodGet, e.cdn.URL+"/cdn/ruta/vlc1", nil)
	req.Header.Set("X-Token-Admin", "secreto")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var ruta map[string]any
	json.NewDecoder(resp.Body).Decode(&ruta)
	if ruta["nodo"] != "valencia" || ruta["encontrado"] != true {
		t.Errorf("ruta = %v", ruta)
	}
}
