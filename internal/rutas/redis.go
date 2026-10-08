package rutas

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

const maxCacheRedis = 300000

// FuenteRedis consulta las rutas que escribe el orquestador
// (proxy:route:{usuario} → IP del nodo) y guarda cada respuesta un rato.
type FuenteRedis struct {
	cli      *redis.Client
	prefijo  string
	ttl      time.Duration
	refresco time.Duration
	log      *slog.Logger

	mu    sync.Mutex
	cache map[string]entradaRedis

	estMu sync.Mutex
	est   EstadoFuente
}

type entradaRedis struct {
	zona  string
	ok    bool
	vence time.Time
}

func NuevaRedis(rawURL, prefijo string, ttl, refresco time.Duration, log *slog.Logger) (*FuenteRedis, error) {
	opt, err := redis.ParseURL(rawURL)
	if err != nil {
		return nil, err
	}
	opt.DialTimeout = 2 * time.Second
	opt.ReadTimeout = 300 * time.Millisecond
	opt.WriteTimeout = 300 * time.Millisecond
	return &FuenteRedis{
		cli:      redis.NewClient(opt),
		prefijo:  prefijo,
		ttl:      ttl,
		refresco: refresco,
		log:      log,
		cache:    map[string]entradaRedis{},
	}, nil
}

func (f *FuenteRedis) Zona(ctx context.Context, usuario string) (string, bool) {
	ahora := time.Now()
	f.mu.Lock()
	e, hay := f.cache[usuario]
	f.mu.Unlock()
	if hay && ahora.Before(e.vence) {
		return e.zona, e.ok
	}

	ctx, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	defer cancel()
	v, err := f.cli.Get(ctx, f.prefijo+usuario).Result()
	switch {
	case errors.Is(err, redis.Nil):
		e = entradaRedis{vence: ahora.Add(f.ttl)}
	case err != nil:
		f.anotarError(err)
		if hay {
			return e.zona, e.ok // mejor una ruta vieja que ninguna
		}
		return "", false
	default:
		e = entradaRedis{zona: v, ok: true, vence: ahora.Add(f.ttl)}
	}

	f.mu.Lock()
	if len(f.cache) >= maxCacheRedis {
		f.cache = map[string]entradaRedis{}
	}
	f.cache[usuario] = e
	f.mu.Unlock()
	return e.zona, e.ok
}

func (f *FuenteRedis) Iniciar(ctx context.Context) error {
	err := f.verificar(ctx)
	go func() {
		t := time.NewTicker(f.refresco)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				f.cli.Close()
				return
			case <-t.C:
				f.verificar(ctx)
			}
		}
	}()
	return err
}

func (f *FuenteRedis) verificar(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if err := f.cli.Ping(ctx).Err(); err != nil {
		f.anotarError(err)
		f.log.Error("Redis del orquestador no responde", "error", err)
		return err
	}
	f.estMu.Lock()
	f.est.UltimaCarga = time.Now()
	f.est.UltimoError = ""
	f.estMu.Unlock()
	return nil
}

func (f *FuenteRedis) anotarError(err error) {
	f.estMu.Lock()
	f.est.UltimoError = err.Error()
	f.estMu.Unlock()
}

func (f *FuenteRedis) Estado() EstadoFuente {
	f.estMu.Lock()
	e := f.est
	f.estMu.Unlock()
	f.mu.Lock()
	e.Usuarios = len(f.cache) // usuarios en caché, no el total del orquestador
	f.mu.Unlock()
	e.Fuente = "redis"
	return e
}
