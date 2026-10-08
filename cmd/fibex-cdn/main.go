// fibex-cdn: redirige a cada abonado al XUI de su ciudad y lo manda a
// Caracas si ese nodo se cae.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	fibexcdn "fibex-cdn"
	"fibex-cdn/internal/config"
	"fibex-cdn/internal/instalar"
	"fibex-cdn/internal/nodos"
	"fibex-cdn/internal/rutas"
	"fibex-cdn/internal/servidor"
)

const nombreConfig = "fibex-cdn.env"

const ayuda = `fibex-cdn %s — CDN geográfico para los XUI de Fibexplay

Uso:
  fibex-cdn [servir]        arranca el CDN (lo que pasa con doble clic)
  fibex-cdn diagnostico     revisa configuración, nodos y fuente de rutas, y sale
  fibex-cdn estado          muestra el estado del CDN que está corriendo
  fibex-cdn instalar        (Linux, root) lo instala como servicio systemd
  fibex-cdn desinstalar     (Linux, root) quita el servicio, conserva la configuración
  fibex-cdn plantilla       imprime una configuración de ejemplo
  fibex-cdn version

Opción para todos los comandos:
  --config RUTA             archivo de configuración. Si no se indica, se busca
                            FIBEX_CDN_CONFIG, luego fibex-cdn.env junto al
                            ejecutable, en la carpeta actual y en /etc/fibex-cdn/.
`

func main() {
	args := os.Args[1:]
	comando := "servir"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		comando, args = args[0], args[1:]
	}
	banderas := flag.NewFlagSet(comando, flag.ContinueOnError)
	rutaConfig := banderas.String("config", "", "archivo de configuración")
	banderas.Usage = func() { fmt.Printf(ayuda, fibexcdn.Version) }
	if err := banderas.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		os.Exit(2)
	}

	var err error
	switch comando {
	case "servir":
		err = servir(*rutaConfig)
	case "diagnostico", "diagnóstico":
		err = diagnostico(*rutaConfig)
	case "estado":
		err = mostrarEstado(*rutaConfig)
	case "instalar":
		err = instalar.Instalar(*rutaConfig, fibexcdn.PlantillaConfig)
	case "desinstalar":
		err = instalar.Desinstalar()
	case "plantilla":
		fmt.Print(fibexcdn.PlantillaConfig)
	case "version", "versión":
		fmt.Println("fibex-cdn", fibexcdn.Version)
	case "ayuda", "help":
		fmt.Printf(ayuda, fibexcdn.Version)
	default:
		fmt.Printf(ayuda, fibexcdn.Version)
		err = fmt.Errorf("comando desconocido: %q", comando)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "\nERROR:", err)
		pausarEnWindows()
		os.Exit(1)
	}
}

// buscarConfig devuelve la ruta del archivo de configuración. Si no existe
// ninguno, crea una plantilla junto al ejecutable y pide editarla.
func buscarConfig(indicada string) (string, error) {
	if indicada != "" {
		return indicada, nil
	}
	if v := os.Getenv("FIBEX_CDN_CONFIG"); v != "" {
		return v, nil
	}
	var candidatos []string
	if exe, err := os.Executable(); err == nil {
		candidatos = append(candidatos, filepath.Join(filepath.Dir(exe), nombreConfig))
	}
	if cwd, err := os.Getwd(); err == nil {
		candidatos = append(candidatos, filepath.Join(cwd, nombreConfig))
	}
	candidatos = append(candidatos, "/etc/fibex-cdn/"+nombreConfig)
	for _, c := range candidatos {
		if _, err := os.Stat(c); err == nil {
			return c, nil
		}
	}
	for _, c := range candidatos[:len(candidatos)-1] {
		if os.WriteFile(c, []byte(fibexcdn.PlantillaConfig), 0o600) == nil {
			return "", fmt.Errorf("no había configuración: se creó %s\nRevísala (sobre todo RUTAS_FUENTE) y vuelve a ejecutar", c)
		}
	}
	return "", errors.New("no se encontró " + nombreConfig + " ni se pudo crear una plantilla")
}

func cargar(indicada string) (*config.Config, string, error) {
	ruta, err := buscarConfig(indicada)
	if err != nil {
		return nil, "", err
	}
	cfg, err := config.Cargar(ruta)
	if err != nil {
		return nil, ruta, fmt.Errorf("%s: %w", ruta, err)
	}
	return cfg, ruta, nil
}

func nuevoLog(nivel string) *slog.Logger {
	var l slog.Level
	l.UnmarshalText([]byte(nivel))
	return slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: l}))
}

func servir(indicada string) error {
	cfg, ruta, err := cargar(indicada)
	if err != nil {
		return err
	}
	log := nuevoLog(cfg.LogNivel)
	log.Info("iniciando fibex-cdn", "version", fibexcdn.Version, "config", ruta, "fuente_rutas", cfg.RutasFuente)

	ctx, parar := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer parar()

	grupo := nodos.NuevoGrupo(cfg, log)
	grupo.Sondear(ctx)
	for _, e := range grupo.Instantanea() {
		log.Info("nodo", "id", e.ID, "url", e.URL, "estado", e.Estado, "error", e.UltimoError)
	}
	go grupo.Vigilar(ctx)

	fuente, err := rutas.Nueva(cfg, log)
	if err != nil {
		return err
	}

	escucha, err := net.Listen("tcp", cfg.Escuchar)
	if err != nil {
		return fmt.Errorf("no se pudo abrir %s: %w (¿otro programa usa ese puerto o falta permiso?)", cfg.Escuchar, err)
	}
	srv := &http.Server{
		Handler:           servidor.Nuevo(cfg, grupo, fuente, log),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelDebug),
	}
	go func() {
		<-ctx.Done()
		log.Info("deteniendo...")
		apagar, cancelar := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancelar()
		srv.Shutdown(apagar)
	}()
	go resumen(ctx, grupo, log)

	// Las rutas se cargan con el puerto ya abierto: si la fuente tarda o está
	// caída, el CDN igual atiende y manda a todos a la zona por defecto.
	go func() {
		if err := fuente.Iniciar(ctx); err != nil {
			log.Warn("sin rutas por ahora: todos van a la zona por defecto hasta que la fuente responda",
				"zona_por_defecto", cfg.ZonaPorDefecto, "error", err)
		}
	}()

	log.Info("CDN listo", "escuchando", escucha.Addr().String(), "estado", "http://"+urlLocal(cfg.Escuchar)+"/cdn/estado")
	if err := srv.Serve(escucha); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// resumen escribe cada 5 minutos cuántas redirecciones recibió cada nodo.
func resumen(ctx context.Context, grupo *nodos.Grupo, log *slog.Logger) {
	t := time.NewTicker(5 * time.Minute)
	defer t.Stop()
	anteriores := map[string]int64{}
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		var partes []any
		for _, e := range grupo.Instantanea() {
			partes = append(partes, e.ID, fmt.Sprintf("%s +%d", e.Estado, e.Redirecciones-anteriores[e.ID]))
			anteriores[e.ID] = e.Redirecciones
		}
		log.Info("resumen 5 min", partes...)
	}
}

func urlLocal(escuchar string) string {
	host, puerto, err := net.SplitHostPort(escuchar)
	if err != nil {
		return "127.0.0.1"
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	if puerto == "80" {
		return host
	}
	return net.JoinHostPort(host, puerto)
}

func diagnostico(indicada string) error {
	cfg, ruta, err := cargar(indicada)
	if err != nil {
		return err
	}
	fmt.Printf("Configuración: %s  ✔\n\n", ruta)
	fallas := 0
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	fmt.Printf("Nodos (sondeo %s):\n", cfg.SaludModo)
	grupo := nodos.NuevoGrupo(cfg, log)
	ctx := context.Background()
	for _, n := range grupo.Nodos() {
		dur, err := grupo.Probar(ctx, n)
		marca := ""
		if n == grupo.Respaldo() {
			marca = " (respaldo)"
		}
		if err != nil {
			fallas++
			fmt.Printf("  ✘ %-10s %-28s %v%s\n", n.ID, n.URL, err, marca)
		} else {
			fmt.Printf("  ✔ %-10s %-28s %s%s\n", n.ID, n.URL, dur.Round(time.Millisecond), marca)
		}
	}

	fmt.Printf("\nFuente de rutas: %s\n", cfg.RutasFuente)
	fuente, err := rutas.Nueva(cfg, log)
	if err == nil {
		ctxFuente, cancelar := context.WithTimeout(ctx, 3*time.Minute)
		err = fuente.Iniciar(ctxFuente)
		e := fuente.Estado()
		cancelar()
		if err == nil {
			fmt.Printf("  ✔ %d abonados conocidos\n", e.Usuarios)
		}
	}
	if err != nil {
		fallas++
		fmt.Printf("  ✘ %v\n", err)
	}

	fmt.Printf("\nPuerto %s: ", cfg.Escuchar)
	if l, err := net.Listen("tcp", cfg.Escuchar); err != nil {
		fmt.Printf("✘ %v (si el servicio ya está corriendo, es normal)\n", err)
	} else {
		l.Close()
		fmt.Println("✔ libre")
	}

	if fallas > 0 {
		return fmt.Errorf("%d problema(s) encontrados", fallas)
	}
	fmt.Println("\nTodo en orden.")
	pausarEnWindows()
	return nil
}

func mostrarEstado(indicada string) error {
	cfg, _, err := cargar(indicada)
	if err != nil {
		return err
	}
	cliente := &http.Client{Timeout: 5 * time.Second}
	resp, err := cliente.Get("http://" + urlLocal(cfg.Escuchar) + "/cdn/estado")
	if err != nil {
		return fmt.Errorf("el CDN no responde en %s: %w", cfg.Escuchar, err)
	}
	defer resp.Body.Close()
	_, err = io.Copy(os.Stdout, resp.Body)
	return err
}

// pausarEnWindows evita que la ventana se cierre sola cuando se abrió con doble clic.
func pausarEnWindows() {
	if runtime.GOOS != "windows" {
		return
	}
	fmt.Print("\nPresiona Enter para cerrar...")
	bufio.NewReader(os.Stdin).ReadString('\n')
}
