// El binario stress es el CLUSTER DE STRESS TEST de Imperio Industrial
// (GDD §13.4 modo 3 y §15.4): un harness desacoplado y TEMPORAL que conecta
// muchos bots ligeros contra las MISMAS APIs públicas que juegan los humanos
// (ADR-010), mide la respuesta del sistema bajo carga y emite un informe con
// los disparadores medidos del SAD §13.
//
// SALVAGUARDA (GDD §13.4: el modo stress test corre en un entorno de pruebas
// independiente y NUNCA toca el mundo de producción). El binario rehúsa
// arrancar si:
//
//   - II_STRESS_API_URL no está definida (no hay default: elegir el target es
//     siempre una decisión consciente del operador);
//   - II_ENV vale prod/production;
//   - el host de la API o el de la base de datos no casan la allowlist de
//     entornos no productivos (II_STRESS_ALLOW_HOSTS; por defecto
//     localhost/127.0.0.1/::1/*.stress.*/staging.*).
//
// Además, TODA cuenta que crea lleva el prefijo reconocible
// "stress-<run_id>-…" y, al terminar, se marca como retirada
// (II_STRESS_CLEANUP=true por defecto) sin borrar nada del ledger, que es
// append-only.
//
// Configuración (12-factor, prefijo II_STRESS_*):
//
//	II_STRESS_API_URL        raíz de la API del entorno de pruebas (OBLIGATORIA)
//	II_STRESS_BOTS           bots totales (default 200)
//	II_STRESS_RAMP           rampa de arranque (default 30s)
//	II_STRESS_DURATION       duración de la carga (default 120s)
//	II_STRESS_TICK           periodo de acción por bot (default 1s)
//	II_STRESS_MIX            mezcla por arquetipo (default producer=50,trader=30,freighter=10,transformer=10)
//	II_STRESS_WRITE_RATIO    fracción de escrituras (default 0.3)
//	II_STRESS_STOCK_ENDOWMENT dotación de stock por cuenta (default 10000; 0 la desactiva)
//	II_STRESS_SELL_SHARE     fracción de publicaciones sell (default 0.5)
//	II_STRESS_REPORT         ruta del informe JSON (default stress-report.json)
//	II_STRESS_ADDR           métricas propias del harness (default :8083)
//	II_STRESS_CLEANUP        retirar las cuentas al terminar (default true)
//	II_STRESS_ALLOW_HOSTS    allowlist de hosts no productivos
//	II_STRESS_DATABASE_URL   BD del entorno de pruebas (default II_DATABASE_URL)
//
// REPRESENTATIVIDAD del perfil de carga. La operación cara del mix es la
// ACEPTACIÓN (escrow + ventana de sorteo + contención SERIALIZABLE), y solo se
// puede aceptar lo que alguien ofrece: por eso el provisioner DOTA STOCK a cada
// cuenta (II_STRESS_STOCK_ENDOWMENT) y una fracción de las publicaciones sale
// como sell (II_STRESS_SELL_SHARE), de modo que el harness sea su propia
// contraparte y la tasa de aceptación escale con la población en lugar de
// agotar una oferta ajena y finita. Cuando aun así una operación del mix se
// queda sin emitir, el veredicto lo denuncia en sus PRIMERAS líneas y la lista
// en verdict.unexercised_paths: un informe que no midió el camino caliente no
// puede leerse como sano por lo que sí midió.
//
// Códigos de salida: 0 corrida sana; 1 error de configuración o de ejecución;
// 2 la corrida terminó con veredicto NEGATIVO: 5xx recibidos por el harness,
// 5xx registrados por el propio sistema durante la corrida (delta de sus
// métricas contra la línea base previa) o errores inesperados. La contención
// SERIALIZABLE agotada (ii_tx_serialization_exhausted_total > 0 durante la
// corrida) NO tumba la corrida —encontrar el techo es el objetivo del harness—
// pero sale como ADVERTENCIA explícita en el veredicto: es una transacción
// revertida entera, y un trabajo de fondo caído no lo recibe ningún cliente.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"

	"github.com/lokiteitor/global-market/backend/internal/ledger"
	"github.com/lokiteitor/global-market/backend/internal/platform/config"
	"github.com/lokiteitor/global-market/backend/internal/platform/service"
	"github.com/lokiteitor/global-market/backend/internal/stress"
)

// serviceName etiqueta las métricas y los logs del binario.
const serviceName = "stress"

// Códigos de salida del harness.
const (
	exitFailure = 1
	exitVerdict = 2
)

func main() {
	report, err := run()
	if err != nil {
		fmt.Fprintln(os.Stderr, prefixed(err))
		os.Exit(exitFailure)
	}
	if report != nil && !report.Verdict.OK {
		os.Exit(exitVerdict)
	}
}

// prefixed antepone el nombre del binario al error SOLO si el mensaje no lo
// trae ya: los errores del paquete internal/stress (y la salvaguarda) se
// formatean con el prefijo "stress: ", y repetirlo produciría "stress: stress:".
func prefixed(err error) string {
	msg := err.Error()
	if strings.HasPrefix(msg, serviceName+":") {
		return msg
	}
	return serviceName + ": " + msg
}

func run() (*stress.Report, error) {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// La SALVAGUARDA se aplica ANTES de tocar nada: si el target huele a
	// producción, el harness no abre ni el pool de BD.
	opts, err := stress.OptionsFromEnv()
	if err != nil {
		return nil, err
	}
	ledgerOpts, err := ledger.OptionsFromEnv()
	if err != nil {
		return nil, err
	}

	// El harness NO usa config.Load: su entorno de destino puede llamarse
	// «staging» y la configuración compartida solo admite dev|prod. Toma de la
	// plataforma lo que necesita (BD del entorno de pruebas y nivel de log) y
	// deja la decisión de entorno a su propia salvaguarda.
	cfg := config.Config{
		DatabaseURL:   opts.DatabaseURL,
		HTTPAddr:      config.DefaultHTTPAddr,
		EngineAddr:    config.DefaultEngineAddr,
		LogLevel:      logLevel(),
		Env:           config.DefaultEnvironment,
		MigrationsDir: config.DefaultMigrationsDir,
	}
	app, err := service.New(ctx, serviceName, opts.Addr, cfg)
	if err != nil {
		return nil, err
	}
	defer app.Close()

	runner, err := stress.NewRunner(app.Pool(), opts, ledgerOpts, app.Logger(), app.Metrics().Registry())
	if err != nil {
		return nil, err
	}

	// Observabilidad propia del harness (/healthz, /readyz, /metrics) mientras
	// dura la corrida; se apaga cuando la carga termina.
	obsCtx, stopObs := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := app.Run(obsCtx); err != nil {
			app.Logger().Error("stress: el servidor de observabilidad terminó con error", slog.Any("error", err))
		}
	}()

	report, runErr := runner.Run(ctx)
	stopObs()
	wg.Wait()
	if runErr != nil {
		return nil, runErr
	}

	// El informe JSON ya lo escribió el harness en II_STRESS_REPORT; aquí solo
	// queda la lectura humana por consola.
	fmt.Print(report.Console())
	return report, nil
}

// logLevel lee el nivel de log compartido de la plataforma con el default
// documentado.
func logLevel() string {
	if v := strings.ToLower(strings.TrimSpace(os.Getenv(config.EnvLogLevel))); v != "" {
		return v
	}
	return config.DefaultLogLevel
}
