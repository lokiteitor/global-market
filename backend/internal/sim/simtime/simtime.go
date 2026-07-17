// Package simtime define el reloj lógico del dominio (sim-time).
//
// El sim-time es el único reloj del dominio (ADR-002): todo plazo de juego se
// expresa en segundos de simulación desde el génesis del mundo. El wall-clock
// solo interviene para derivar el sim-time actual a partir de un ancla.
package simtime

import (
	"fmt"
	"time"
)

// SimTime son segundos de sim-time transcurridos desde el génesis del mundo.
// Coincide con el schema `SimTime` del contrato OpenAPI (int64, >= 0).
type SimTime int64

const (
	// Ratio es la relación sim-time/wall-clock por defecto: 24× (ADR-002,
	// 1 día de juego = 1 hora real).
	Ratio int64 = 24
	// SimDay es la duración de un día de juego en segundos de sim-time.
	SimDay int64 = 86_400
	// SimYearDays es la duración del año de juego en días de juego.
	SimYearDays int64 = 360
	// SimYear es la duración de un año de juego en segundos de sim-time.
	SimYear int64 = SimDay * SimYearDays
)

// Format serializa un sim-time en el formato legible del contrato
// `AAA-DDD-HH:MM`: año de juego 1-based (360 días, mínimo 3 dígitos), día del
// año 1-based (3 dígitos) y hora:minuto del día de juego. Ejemplos:
// 0 → "001-001-00:00"; 31104000 → "002-001-00:00".
// Los valores negativos no existen en el dominio y se tratan como el génesis.
func Format(t SimTime) string {
	if t < 0 {
		t = 0
	}
	s := int64(t)
	days := s / SimDay
	year := days/SimYearDays + 1
	dayOfYear := days%SimYearDays + 1
	secondsOfDay := s % SimDay
	hour := secondsOfDay / 3600
	minute := secondsOfDay % 3600 / 60
	return fmt.Sprintf("%03d-%03d-%02d:%02d", year, dayOfYear, hour, minute)
}

// Derive calcula el sim-time actual a partir de un ancla persistida:
// sim = base + elapsed*ratio, donde elapsed = now - wallAnchor. Si el mundo
// está congelado (ventana de mantenimiento, ADR-003) el tiempo no avanza y se
// devuelve base tal cual.
//
// La conversión conserva la precisión sub-segundo del wall-clock antes de
// truncar a segundos de sim-time, evitando además desbordamientos con
// intervalos arbitrariamente largos.
func Derive(base SimTime, wallAnchor, now time.Time, ratio int64, frozen bool) SimTime {
	if frozen {
		return base
	}
	elapsed := now.Sub(wallAnchor)
	secs := int64(elapsed / time.Second)
	rem := int64(elapsed % time.Second) // nanosegundos residuales, mismo signo que elapsed
	return base + SimTime(secs*ratio+rem*ratio/int64(time.Second))
}
