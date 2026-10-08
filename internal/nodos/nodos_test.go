package nodos

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"sync"
	"testing"
	"time"

	"fibex-cdn/internal/config"
)

type reloj struct{ t time.Time }

func (r *reloj) ahora() time.Time        { return r.t }
func (r *reloj) avanzar(d time.Duration) { r.t = r.t.Add(d) }

type caidos struct {
	mu sync.Mutex
	m  map[string]bool
}

func (c *caidos) poner(id string, caido bool) {
	c.mu.Lock()
	c.m[id] = caido
	c.mu.Unlock()
}

func (c *caidos) sondear(_ context.Context, n *Nodo) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m[n.ID] {
		return errors.New("sin respuesta")
	}
	return nil
}

func grupoDePrueba(t *testing.T, rampa time.Duration) (*Grupo, *reloj, *caidos) {
	t.Helper()
	mk := func(id, u string, alias ...string) config.Nodo {
		pu, _ := url.Parse(u)
		return config.Nodo{ID: id, URL: pu, Alias: alias}
	}
	c := &config.Config{
		Nodos: []config.Nodo{
			mk("caracas", "http://203.0.113.62"),
			mk("valencia", "http://203.0.113.63", "10.0.0.83"),
			mk("maracay", "http://198.51.100.158:8080"),
		},
		Respaldo:         "caracas",
		SaludIntervalo:   5 * time.Second,
		SaludTimeout:     time.Second,
		FallosParaCaer:   3,
		ExitosParaVolver: 2,
		Rampa:            rampa,
	}
	g := NuevoGrupo(c, slog.New(slog.NewTextHandler(io.Discard, nil)))
	r := &reloj{t: time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)}
	cd := &caidos{m: map[string]bool{}}
	g.ahora = r.ahora
	g.sondear = cd.sondear
	return g, r, cd
}

func TestResolver(t *testing.T) {
	g, _, _ := grupoDePrueba(t, 0)
	casos := map[string]string{
		"valencia":                   "valencia",
		"VALENCIA":                   "valencia",
		"203.0.113.63":              "valencia",
		"10.0.0.83":                "valencia",
		"http://10.0.0.83/":        "valencia",
		"198.51.100.158":             "maracay",
		"198.51.100.158:8080":        "maracay",
		"http://198.51.100.158:8080": "maracay",
		"203.0.113.62:80":           "caracas",
	}
	for valor, quiere := range casos {
		n := g.Resolver(valor)
		if n == nil || n.ID != quiere {
			t.Errorf("Resolver(%q) = %v, se esperaba %s", valor, n, quiere)
		}
	}
	if g.Resolver("8.8.8.8") != nil || g.Resolver("") != nil {
		t.Error("valores desconocidos deberían dar nil")
	}
}

func TestCaidaYRespaldo(t *testing.T) {
	g, r, cd := grupoDePrueba(t, 0)
	g.Sondear(context.Background())

	if n, motivo := g.Elegir("valencia", "123"); n.ID != "valencia" || motivo != "zona" {
		t.Fatalf("con todo sano: %s %s", n.ID, motivo)
	}

	cd.poner("valencia", true)
	for i := 0; i < 2; i++ {
		r.avanzar(5 * time.Second)
		g.Sondear(context.Background())
	}
	if n, _ := g.Elegir("valencia", "123"); n.ID != "valencia" {
		t.Fatal("con 2 fallos todavía no debe caer (umbral 3)")
	}
	g.Sondear(context.Background())
	if n, motivo := g.Elegir("valencia", "123"); n.ID != "caracas" || motivo != "respaldo" {
		t.Fatalf("con 3 fallos debe ir al respaldo, fue %s %s", n.ID, motivo)
	}

	// Caracas también cae: busca otro nodo sano.
	cd.poner("caracas", true)
	for i := 0; i < 3; i++ {
		g.Sondear(context.Background())
	}
	if n, motivo := g.Elegir("valencia", "123"); n.ID != "maracay" || motivo != "alterno" {
		t.Fatalf("debe ir a maracay como alterno, fue %s %s", n.ID, motivo)
	}

	// Vuelve sin rampa: con 2 éxitos ya recibe a los suyos.
	cd.poner("valencia", false)
	g.Sondear(context.Background())
	if n, _ := g.Elegir("valencia", "123"); n.ID == "valencia" {
		t.Fatal("con 1 éxito todavía no debe volver")
	}
	g.Sondear(context.Background())
	if n, _ := g.Elegir("valencia", "123"); n.ID != "valencia" {
		t.Fatalf("con 2 éxitos debe volver, fue %s", n.ID)
	}
}

func TestArranqueConNodoCaido(t *testing.T) {
	g, _, cd := grupoDePrueba(t, time.Minute)
	cd.poner("maracay", true)
	g.Sondear(context.Background())
	if n, _ := g.Elegir("maracay", "x"); n.ID != "caracas" {
		t.Fatalf("un nodo que no responde al arrancar debe quedar caído, fue %s", n.ID)
	}
	if n, _ := g.Elegir("valencia", "x"); n.ID != "valencia" {
		t.Fatal("un nodo sano al arrancar no debe pasar por la rampa")
	}
}

func TestRampa(t *testing.T) {
	const total = 8 * time.Minute
	g, r, cd := grupoDePrueba(t, total)
	g.Sondear(context.Background())
	cd.poner("valencia", true)
	for i := 0; i < 3; i++ {
		g.Sondear(context.Background())
	}
	cd.poner("valencia", false)
	g.Sondear(context.Background())
	g.Sondear(context.Background()) // 2 éxitos: empieza la rampa

	contar := func() int {
		n := 0
		for i := 0; i < 2000; i++ {
			if nodo, _ := g.Elegir("valencia", fmt.Sprintf("%08d", 10000000+i*7919)); nodo.ID == "valencia" {
				n++
			}
		}
		return n
	}
	anterior := -1
	for paso, quiere := range []int{25, 50, 75, 100} {
		got := contar() * 100 / 2000
		if got < quiere-8 || got > quiere+8 {
			t.Errorf("tramo %d: %d%% vuelve a valencia, se esperaba ~%d%%", paso, got, quiere)
		}
		if got < anterior {
			t.Errorf("la rampa no debe retroceder: %d -> %d", anterior, got)
		}
		anterior = got
		r.avanzar(total / 4)
	}
	g.Sondear(context.Background())
	if e := g.Instantanea()[1]; e.Estado != Sano {
		t.Errorf("al terminar la rampa debe quedar sano, quedó %s", e.Estado)
	}

	// El mismo usuario no cambia de nodo dentro de un mismo tramo.
	if Cubeta("12345678") != Cubeta("12345678") {
		t.Error("Cubeta no es estable")
	}
}

func TestPorcentajeRampa(t *testing.T) {
	total := 4 * time.Minute
	casos := map[time.Duration]int{
		-time.Second:                25,
		0:                           25,
		59 * time.Second:            25,
		time.Minute:                 50,
		2*time.Minute + time.Second: 75,
		3 * time.Minute:             100,
		4 * time.Minute:             100,
	}
	for d, quiere := range casos {
		if got := PorcentajeRampa(d, total); got != quiere {
			t.Errorf("PorcentajeRampa(%v) = %d, se esperaba %d", d, got, quiere)
		}
	}
	if PorcentajeRampa(time.Second, 0) != 100 {
		t.Error("sin rampa debe ser 100")
	}
}
