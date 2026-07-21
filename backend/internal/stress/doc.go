// Package stress es el CLUSTER DE STRESS TEST del mundo de Imperio Industrial
// (GDD §13.4 modo 3 y §15.4): un harness desacoplado que conecta MUCHOS bots
// ligeros contra las MISMAS APIs públicas que juegan los humanos, mide su
// respuesta bajo carga y emite un informe con los DISPARADORES MEDIDOS del
// SAD §13 (carga del motor, lag de la outbox, latencia de consulta del tablón y
// contención SERIALIZABLE).
//
// # Fronteras
//
//   - Todo el GAMEPLAY del harness pasa por pkg/botsdk contra la API pública
//     (ADR-010: igualdad de API literal; §15.4: «el mismo camino real del
//     sistema»). El harness no importa ningún paquete de dominio para jugar.
//   - El PROVISIONING de cuentas (Provisioner) es una operación de ADMIN DEL
//     ENTORNO DE PRUEBAS, no de juego: el contrato no expone endpoint de
//     registro, así que las cuentas se crean por BD reutilizando el patrón del
//     orquestador (cuenta kind=bot + credencial argon2id + bot_profile +
//     capitalización contabilizada por el banco central) sin importar
//     internal/bots. Por la misma razón el provisioner DOTA STOCK a cada cuenta
//     (production_output: +stock_free / −world_source, ADR-022, con el plano
//     físico movido a la vez): el capital solo habilita el lado comprador, y sin
//     mercancía propia el harness no puede publicar sell ni aceptar buy, de modo
//     que su operación de ACEPTACIÓN quedaría colgada de una oferta ajena y
//     finita. Es la diferencia entre medir el techo de escritura y medir el
//     camino corto.
//
// # Representatividad del perfil de carga
//
// Una corrida que no emite la operación cara de su mezcla NO midió lo que
// declara. El harness lo aborda por los dos lados: genera su propia contraparte
// (dotación de stock + II_STRESS_SELL_SHARE de publicaciones sell, y un
// board_read DIRIGIDO antes de rendirse al aceptar) y, si aun así una operación
// se queda sin emitir, el veredicto lo denuncia en sus PRIMERAS líneas y la lista
// en Verdict.Unexercised.
//   - El harness NO es el modo «mundo vivo» ni «densidad dinámica» (esos viven
//     en internal/bots): es el modo 3, temporal y en entorno separado.
//
// # Salvaguarda (crítica)
//
// El modo stress «nunca toca el mundo de producción» (GDD §13.4). El harness
// rehúsa arrancar si:
//
//   - II_STRESS_API_URL no está definida explícitamente (no hay default: apuntar
//     al target es siempre una decisión consciente del operador);
//   - II_ENV vale prod/production;
//   - el host de la API —o el de la base de datos del provisioner— no casa
//     ninguna entrada de la allowlist de entornos no productivos
//     (II_STRESS_ALLOW_HOSTS; por defecto localhost/127.0.0.1/::1/*.stress.*/
//     staging.*).
//
// Además, TODA cuenta creada por una corrida lleva el prefijo reconocible
// "stress-<run_id>-…", de modo que siempre puede identificarse y limpiarse
// (AccountPrefix / RunAccountPrefix).
//
// # Uso
//
//	opts, err := stress.OptionsFromEnv()   // incluye la salvaguarda
//	runner, err := stress.NewRunner(pool, opts, ledger.DefaultOptions(), logger, reg)
//	report, err := runner.Run(ctx)          // aprovisiona, carga, mide y limpia
//	report.WriteJSON(opts.ReportPath)
//	fmt.Print(report.Console())
//
// El binario cmd/stress hace exactamente eso y expone sus propias métricas
// Prometheus en II_STRESS_ADDR (default :8083).
package stress
