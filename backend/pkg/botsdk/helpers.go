package botsdk

import (
	"context"
	"fmt"
	"iter"
	"net/url"
	"strconv"
	"time"
)

// ── Paginación ──

// Page es una página de un listado con los metadatos de su respuesta
// (Meta.NextCursor apunta a la página siguiente; vacío si no hay más).
type Page[T any] struct {
	Items []T
	Meta  Meta
}

// PageQuery son los parámetros comunes de paginación de todos los listados.
type PageQuery struct {
	// Cursor es el cursor opaco devuelto en meta.next_cursor.
	Cursor string
	// Limit es el tamaño máximo de página (1–200; 0 = default del servidor).
	Limit int
}

// apply añade los parámetros de paginación a la query.
func (p PageQuery) apply(v url.Values) {
	if p.Cursor != "" {
		v.Set("cursor", p.Cursor)
	}
	if p.Limit > 0 {
		v.Set("limit", strconv.Itoa(p.Limit))
	}
}

// All devuelve un iterador que recorre todos los elementos de un listado
// siguiendo meta.next_cursor automáticamente. fetch recibe el cursor de la
// página a pedir ("" para la primera). Si una página falla, el iterador
// produce el error y termina.
//
//	for pub, err := range botsdk.All(ctx, func(ctx context.Context, cursor string) (botsdk.Page[botsdk.Publication], error) {
//		q := baseQuery
//		q.Cursor = cursor
//		return c.Board(ctx, q)
//	}) { ... }
func All[T any](ctx context.Context, fetch func(ctx context.Context, cursor string) (Page[T], error)) iter.Seq2[T, error] {
	return func(yield func(T, error) bool) {
		cursor := ""
		for {
			page, err := fetch(ctx, cursor)
			if err != nil {
				var zero T
				yield(zero, err)
				return
			}
			for _, item := range page.Items {
				if !yield(item, nil) {
					return
				}
			}
			next := page.Meta.NextCursor
			if next == "" {
				return
			}
			if next == cursor {
				// Defensa contra bucles: un servidor sano nunca repite cursor.
				var zero T
				yield(zero, fmt.Errorf("botsdk: paginación con cursor repetido %q", next))
				return
			}
			cursor = next
		}
	}
}

// CollectAll acumula en memoria todos los elementos de un listado siguiendo
// meta.next_cursor. Úsalo solo con listados acotados.
func CollectAll[T any](ctx context.Context, fetch func(ctx context.Context, cursor string) (Page[T], error)) ([]T, error) {
	var all []T
	for item, err := range All(ctx, fetch) {
		if err != nil {
			return nil, err
		}
		all = append(all, item)
	}
	return all, nil
}

// ── Espera de estados ──

// WaitFor sondea cond cada interval hasta que devuelva true (nil), devuelva
// un error (se propaga) o el contexto se cancele (ctx.Err()). Evalúa cond
// inmediatamente antes de la primera espera. Pensado para esperar estados de
// dominio: contrato settled, edificio operational, cargamento delivered...
func WaitFor(ctx context.Context, interval time.Duration, cond func() (bool, error)) error {
	if interval <= 0 {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		done, err := cond()
		if err != nil {
			return err
		}
		if done {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// ── Dinero y stock (enteros serializados como strings — jamás floats) ──

// ParseMoney valida y convierte un importe del contrato (string de dígitos,
// con signo negativo opcional en los campos contables del ledger) a int64 en
// unidades menores. Rechaza vacío, signos '+', espacios, decimales y todo lo
// que no sea un entero decimal.
func ParseMoney(s string) (int64, error) {
	if !validInt(s, true) {
		return 0, fmt.Errorf("botsdk: importe monetario inválido %q", s)
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("botsdk: importe monetario fuera de rango %q", s)
	}
	return v, nil
}

// ParseQty valida y convierte una cantidad de stock del contrato (string de
// dígitos sin signo) a int64.
func ParseQty(s string) (int64, error) {
	if !validInt(s, false) {
		return 0, fmt.Errorf("botsdk: cantidad de stock inválida %q", s)
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("botsdk: cantidad de stock fuera de rango %q", s)
	}
	return v, nil
}

// MoneyFromInt64 serializa un importe en unidades menores como Money.
func MoneyFromInt64(v int64) Money {
	return Money(strconv.FormatInt(v, 10))
}

// QtyFromInt64 serializa una cantidad como Qty; rechaza negativos (el stock
// del contrato es siempre sin signo).
func QtyFromInt64(v int64) (Qty, error) {
	if v < 0 {
		return "", fmt.Errorf("botsdk: cantidad de stock negativa %d", v)
	}
	return Qty(strconv.FormatInt(v, 10)), nil
}

// validInt informa de si s es un entero decimal en ASCII: dígitos [0-9]+ con
// signo '-' inicial opcional si allowNeg.
func validInt(s string, allowNeg bool) bool {
	if s == "" {
		return false
	}
	i := 0
	if allowNeg && s[0] == '-' {
		if len(s) == 1 {
			return false
		}
		i = 1
	}
	for ; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}
