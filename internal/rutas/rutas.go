// Package rutas sabe en qué zona vive cada abonado. La zona puede venir como
// nombre de nodo ("valencia"), IP o alias; el CDN la traduce al nodo.
package rutas

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"fibex-cdn/internal/config"
)

type Fuente interface {
	// Zona devuelve la zona del usuario y si se encontró.
	Zona(ctx context.Context, usuario string) (string, bool)
	// Iniciar hace la primera carga (síncrona) y deja corriendo las recargas.
	// Si la primera carga falla, devuelve el error pero sigue reintentando.
	Iniciar(ctx context.Context) error
	Estado() EstadoFuente
}

type EstadoFuente struct {
	Fuente      string    `json:"fuente"`
	Usuarios    int       `json:"usuarios"`
	UltimaCarga time.Time `json:"ultima_carga,omitempty"`
	UltimoError string    `json:"ultimo_error,omitempty"`
}

func Nueva(c *config.Config, log *slog.Logger) (Fuente, error) {
	switch c.RutasFuente {
	case "ninguna":
		return ninguna{}, nil
	case "mysql":
		return NuevaMySQL(c.MySQL, c.RutasRefresco, c.RutasRecargaCompleta, log)
	case "redis":
		return NuevaRedis(c.RedisURL, c.RedisPrefijo, c.RedisCache, c.RutasRefresco, log)
	case "archivo":
		return NuevaArchivo(c.RutasArchivo, c.RutasRefresco, log), nil
	}
	return nil, fmt.Errorf("fuente de rutas desconocida: %q", c.RutasFuente)
}

type ninguna struct{}

func (ninguna) Zona(context.Context, string) (string, bool) { return "", false }
func (ninguna) Iniciar(context.Context) error               { return nil }
func (ninguna) Estado() EstadoFuente                        { return EstadoFuente{Fuente: "ninguna"} }

// tabla es un mapa usuario→zona protegido para lecturas concurrentes.
type tabla struct {
	mu    sync.RWMutex
	zonas map[string]string

	estMu sync.Mutex
	est   EstadoFuente
}

func (t *tabla) buscar(usuario string) (string, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	z, ok := t.zonas[usuario]
	return z, ok
}

func (t *tabla) reemplazar(nuevas map[string]string) {
	t.mu.Lock()
	t.zonas = nuevas
	t.mu.Unlock()
}

func (t *tabla) agregar(nuevas map[string]string) {
	t.mu.Lock()
	if t.zonas == nil {
		t.zonas = make(map[string]string, len(nuevas))
	}
	for u, z := range nuevas {
		t.zonas[u] = z
	}
	t.mu.Unlock()
}

func (t *tabla) total() int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return len(t.zonas)
}

func (t *tabla) cargaOK() {
	t.estMu.Lock()
	t.est.UltimaCarga = time.Now()
	t.est.UltimoError = ""
	t.estMu.Unlock()
}

func (t *tabla) cargaFallida(err error) {
	t.estMu.Lock()
	t.est.UltimoError = err.Error()
	t.estMu.Unlock()
}

func (t *tabla) estado(fuente string) EstadoFuente {
	t.estMu.Lock()
	e := t.est
	t.estMu.Unlock()
	e.Fuente = fuente
	e.Usuarios = t.total()
	return e
}

// internar evita guardar miles de copias del mismo texto de zona.
type internador map[string]string

func (in internador) de(s string) string {
	if v, ok := in[s]; ok {
		return v
	}
	in[s] = s
	return s
}
