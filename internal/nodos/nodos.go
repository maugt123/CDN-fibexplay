// Package nodos vigila los servidores XUI y elige a cuál mandar a cada abonado.
package nodos

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"fibex-cdn/internal/config"
)

type Estado string

const (
	Desconocido Estado = "desconocido"
	Sano        Estado = "sano"
	Caido       Estado = "caido"
	// Volviendo: el nodo respondió de nuevo y recupera a sus abonados por tramos.
	Volviendo Estado = "volviendo"
)

type Nodo struct {
	ID         string
	URL        *url.URL
	HostHeader string

	mu          sync.Mutex
	estado      Estado
	fallos      int
	exitos      int
	desde       time.Time
	rampaDesde  time.Time
	ultimoError string

	redirecciones atomic.Int64
}

func (n *Nodo) ContarRedireccion() { n.redirecciones.Add(1) }

// HostPuerto devuelve host:puerto para conectarse al nodo.
func (n *Nodo) HostPuerto() string {
	if n.URL.Port() != "" {
		return n.URL.Host
	}
	puerto := "80"
	if n.URL.Scheme == "https" {
		puerto = "443"
	}
	return net.JoinHostPort(n.URL.Hostname(), puerto)
}

type Salud struct {
	Modo             string
	Ruta             string
	Intervalo        time.Duration
	Timeout          time.Duration
	FallosParaCaer   int
	ExitosParaVolver int
	Rampa            time.Duration
}

type Grupo struct {
	nodos     []*Nodo
	porNombre map[string]*Nodo
	respaldo  *Nodo
	salud     Salud
	log       *slog.Logger

	// Reemplazables en pruebas.
	ahora   func() time.Time
	sondear func(context.Context, *Nodo) error
}

func NuevoGrupo(c *config.Config, log *slog.Logger) *Grupo {
	g := &Grupo{
		porNombre: map[string]*Nodo{},
		salud: Salud{
			Modo:             c.SaludModo,
			Ruta:             c.SaludRuta,
			Intervalo:        c.SaludIntervalo,
			Timeout:          c.SaludTimeout,
			FallosParaCaer:   c.FallosParaCaer,
			ExitosParaVolver: c.ExitosParaVolver,
			Rampa:            c.Rampa,
		},
		log:   log,
		ahora: time.Now,
	}
	g.sondear = g.sondeoReal
	for _, cn := range c.Nodos {
		n := &Nodo{ID: cn.ID, URL: cn.URL, HostHeader: cn.HostHeader, estado: Desconocido}
		g.nodos = append(g.nodos, n)
		nombres := append([]string{cn.ID, cn.URL.Host, cn.URL.Hostname(), cn.URL.String()}, cn.Alias...)
		for _, nombre := range nombres {
			nombre = strings.ToLower(strings.TrimSuffix(nombre, "/"))
			if _, existe := g.porNombre[nombre]; !existe {
				g.porNombre[nombre] = n
			}
		}
	}
	g.respaldo = g.porNombre[c.Respaldo]
	return g
}

func (g *Grupo) Nodos() []*Nodo  { return g.nodos }
func (g *Grupo) Respaldo() *Nodo { return g.respaldo }

// Resolver traduce un nombre de zona, IP, alias o URL al nodo que corresponde.
func (g *Grupo) Resolver(valor string) *Nodo {
	valor = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(valor), "/"))
	if valor == "" {
		return nil
	}
	if n, ok := g.porNombre[valor]; ok {
		return n
	}
	if u, err := url.Parse(valor); err == nil && u.Host != "" {
		if n, ok := g.porNombre[u.Host]; ok {
			return n
		}
		return g.porNombre[u.Hostname()]
	}
	if host, _, err := net.SplitHostPort(valor); err == nil {
		return g.porNombre[host]
	}
	return nil
}

// Elegir devuelve el nodo para un abonado de la zona dada y el motivo:
// "zona" (su nodo), "respaldo", "alterno" (otro nodo sano) o "todos-caidos".
func (g *Grupo) Elegir(zona, usuario string) (*Nodo, string) {
	ahora := g.ahora()
	if n := g.Resolver(zona); n != nil && g.acepta(n, usuario, ahora) {
		return n, "zona"
	}
	if g.disponible(g.respaldo) {
		return g.respaldo, "respaldo"
	}
	for _, n := range g.nodos {
		if g.disponible(n) {
			return n, "alterno"
		}
	}
	return g.respaldo, "todos-caidos"
}

// ElegirParaPasar elige el nodo al que se reenvían las peticiones que no son
// de video: el respaldo si está disponible, si no el primero que lo esté.
func (g *Grupo) ElegirParaPasar() *Nodo {
	if g.disponible(g.respaldo) {
		return g.respaldo
	}
	for _, n := range g.nodos {
		if g.disponible(n) {
			return n
		}
	}
	return g.respaldo
}

func (g *Grupo) disponible(n *Nodo) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.estado != Caido
}

func (g *Grupo) acepta(n *Nodo, usuario string, ahora time.Time) bool {
	n.mu.Lock()
	estado, desde := n.estado, n.rampaDesde
	n.mu.Unlock()
	switch estado {
	case Sano, Desconocido:
		return true
	case Volviendo:
		return Cubeta(usuario) < PorcentajeRampa(ahora.Sub(desde), g.salud.Rampa)
	default:
		return false
	}
}

// PorcentajeRampa reparte la vuelta en cuatro tramos: 25, 50, 75 y 100 %.
func PorcentajeRampa(transcurrido, total time.Duration) int {
	if total <= 0 || transcurrido >= total {
		return 100
	}
	if transcurrido < 0 {
		transcurrido = 0
	}
	return int(transcurrido*4/total+1) * 25
}

// Cubeta ubica a un usuario en 0..99 siempre igual, para que durante la rampa
// el mismo abonado no salte de un nodo a otro.
func Cubeta(usuario string) int {
	h := fnv.New32a()
	h.Write([]byte(usuario))
	return int(h.Sum32() % 100)
}

// Sondear revisa todos los nodos una vez, en paralelo.
func (g *Grupo) Sondear(ctx context.Context) {
	var wg sync.WaitGroup
	for _, n := range g.nodos {
		wg.Add(1)
		go func(n *Nodo) {
			defer wg.Done()
			g.registrar(n, g.sondear(ctx, n))
		}(n)
	}
	wg.Wait()
}

// Vigilar sondea cada SALUD_INTERVALO hasta que se cancele el contexto.
func (g *Grupo) Vigilar(ctx context.Context) {
	t := time.NewTicker(g.salud.Intervalo)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			g.Sondear(ctx)
		}
	}
}

func (g *Grupo) registrar(n *Nodo, err error) {
	ahora := g.ahora()
	n.mu.Lock()
	antes := n.estado
	if err == nil {
		n.fallos = 0
		n.exitos++
		switch {
		case n.estado == Desconocido:
			n.estado = Sano
		case n.estado == Caido && n.exitos >= g.salud.ExitosParaVolver:
			if g.salud.Rampa > 0 {
				n.estado = Volviendo
				n.rampaDesde = ahora
			} else {
				n.estado = Sano
			}
		case n.estado == Volviendo && ahora.Sub(n.rampaDesde) >= g.salud.Rampa:
			n.estado = Sano
		}
	} else {
		n.exitos = 0
		n.fallos++
		n.ultimoError = err.Error()
		if n.estado == Desconocido || (n.estado != Caido && n.fallos >= g.salud.FallosParaCaer) {
			n.estado = Caido
		}
	}
	if n.estado != antes {
		n.desde = ahora
	}
	despues, ultimoError := n.estado, n.ultimoError
	n.mu.Unlock()

	if despues == antes {
		return
	}
	switch despues {
	case Caido:
		g.log.Warn("nodo CAÍDO: sus abonados van al respaldo", "nodo", n.ID, "error", ultimoError, "respaldo", g.respaldo.ID)
	case Volviendo:
		g.log.Info("nodo responde de nuevo: retorno escalonado", "nodo", n.ID, "duracion", g.salud.Rampa)
	case Sano:
		g.log.Info("nodo sano", "nodo", n.ID)
	}
}

func (g *Grupo) sondeoReal(ctx context.Context, n *Nodo) error {
	ctx, cancel := context.WithTimeout(ctx, g.salud.Timeout)
	defer cancel()
	if g.salud.Modo == "http" {
		return g.sondeoHTTP(ctx, n)
	}
	var d net.Dialer
	c, err := d.DialContext(ctx, "tcp", n.HostPuerto())
	if err != nil {
		return err
	}
	return c.Close()
}

var clienteSalud = &http.Client{
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

func (g *Grupo) sondeoHTTP(ctx context.Context, n *Nodo) error {
	destino := *n.URL
	destino.Path = g.salud.Ruta
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, destino.String(), nil)
	if err != nil {
		return err
	}
	if n.HostHeader != "" {
		req.Host = n.HostHeader
	}
	req.Header.Set("User-Agent", "fibex-cdn-salud")
	resp, err := clienteSalud.Do(req)
	if err != nil {
		return err
	}
	io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	resp.Body.Close()
	if resp.StatusCode >= 500 {
		return fmt.Errorf("respondió HTTP %d", resp.StatusCode)
	}
	return nil
}

// Probar hace un sondeo suelto y devuelve cuánto tardó (para el diagnóstico).
func (g *Grupo) Probar(ctx context.Context, n *Nodo) (time.Duration, error) {
	inicio := time.Now()
	err := g.sondear(ctx, n)
	if errors.Is(err, context.DeadlineExceeded) {
		err = fmt.Errorf("no respondió en %s", g.salud.Timeout)
	}
	return time.Since(inicio), err
}

type EstadoNodo struct {
	ID              string    `json:"id"`
	URL             string    `json:"url"`
	Estado          Estado    `json:"estado"`
	Desde           time.Time `json:"desde"`
	PorcentajeRampa int       `json:"porcentaje_rampa,omitempty"`
	UltimoError     string    `json:"ultimo_error,omitempty"`
	Redirecciones   int64     `json:"redirecciones"`
	EsRespaldo      bool      `json:"es_respaldo,omitempty"`
}

func (g *Grupo) Instantanea() []EstadoNodo {
	ahora := g.ahora()
	var res []EstadoNodo
	for _, n := range g.nodos {
		n.mu.Lock()
		e := EstadoNodo{
			ID:            n.ID,
			URL:           n.URL.String(),
			Estado:        n.estado,
			Desde:         n.desde,
			UltimoError:   n.ultimoError,
			Redirecciones: n.redirecciones.Load(),
			EsRespaldo:    n == g.respaldo,
		}
		if n.estado == Volviendo {
			e.PorcentajeRampa = PorcentajeRampa(ahora.Sub(n.rampaDesde), g.salud.Rampa)
		}
		if n.estado == Sano {
			e.UltimoError = ""
		}
		n.mu.Unlock()
		res = append(res, e)
	}
	return res
}
