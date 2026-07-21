// Package botsdk es el SDK oficial de bots de Imperio Industrial (ADR-024)
// y la ÚNICA forma soportada de construir bots. En runtime consume
// exclusivamente la API pública REST del contrato OpenAPI
// (docs/api/openapi.yaml, v1.3.0): mismos endpoints y mismos rate limits que
// un jugador humano — igualdad de API literal (ADR-010).
//
// El cliente abstrae la fontanería del contrato:
//
//   - sesión bearer en memoria (Login/Logout/Me);
//   - envelope {data,meta} / {error} decodificado en tipos Go fieles al
//     contrato (snake_case; dinero y stock SIEMPRE como strings — nunca
//     floats);
//   - errores tipados (APIError con Status, Code, Message, Details y
//     RetryAfter);
//   - reintentos con backoff exponencial ante 429 (respetando Retry-After) y
//     ante errores de red, con Idempotency-Key UUIDv7 generada por mutación y
//     reutilizada en sus reintentos — nunca hay doble ejecución;
//   - paginación por cursor (Page, All, CollectAll) y espera de estados
//     (WaitFor).
//
// Ejemplo mínimo:
//
//	c, err := botsdk.New(botsdk.Options{BaseURL: "http://localhost:8080/api/v1"})
//	if err != nil {
//		log.Fatal(err)
//	}
//	if _, err := c.Login(ctx, "mi-bot", secreto); err != nil {
//		log.Fatal(err)
//	}
//	defer c.Logout(ctx)
//
//	page, err := c.Board(ctx, botsdk.BoardQuery{
//		Kind: botsdk.PublicationSell,
//		Sort: botsdk.SortUnitPriceAsc,
//	})
//	if err != nil {
//		log.Fatal(err)
//	}
//	for _, pub := range page.Items {
//		precio, _ := pub.UnitPrice.Int64()
//		if precio <= miPrecioObjetivo {
//			_, err := c.Accept(ctx, pub.ID, pub.MinLot, "")
//			// ...
//		}
//	}
//
// El paquete no importa internal/* — cualquier operación de ciclo de vida
// (provisioning de cuentas, capitalización) pertenece al orquestador
// (cmd/bots), no a este SDK.
package botsdk
