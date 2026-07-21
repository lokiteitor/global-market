package worldgen

// Geometrías del generador: WKT en metros de mundo (SRID 0 planar, ADR-019) y
// utilidades de puntos/distancias/fronteras. Réplica mínima de las helpers del
// seed (no exportadas allí) para no acoplar los paquetes.

import (
	"fmt"
	"math"

	"github.com/google/uuid"
)

// rectWKT construye el WKT de un rectángulo cerrado (anillo CCW) en metros de
// mundo. Acepta coordenadas negativas (regiones al oeste/sur del origen).
func rectWKT(minX, minY, maxX, maxY int64) string {
	return fmt.Sprintf("POLYGON((%d %d,%d %d,%d %d,%d %d,%d %d))",
		minX, minY, maxX, minY, maxX, maxY, minX, maxY, minX, minY)
}

// pointWKT construye el WKT de un punto en metros de mundo.
func pointWKT(x, y int64) string {
	return fmt.Sprintf("POINT(%d %d)", x, y)
}

// lineWKT construye el WKT de un segmento recto entre dos puntos.
func lineWKT(x1, y1, x2, y2 int64) string {
	return fmt.Sprintf("LINESTRING(%d %d,%d %d)", x1, y1, x2, y2)
}

// line3WKT construye el WKT de una polilínea de tres vértices (trazado de un
// enlace inter-región: origen → punto de cruce de frontera → destino).
func line3WKT(x1, y1, x2, y2, x3, y3 int64) string {
	return fmt.Sprintf("LINESTRING(%d %d,%d %d,%d %d)", x1, y1, x2, y2, x3, y3)
}

// euclideanM devuelve la distancia euclídea entre dos puntos, redondeada al
// entero más cercano y con suelo 1 (length_m > 0 exigido por la BD).
func euclideanM(x1, y1, x2, y2 int64) int64 {
	d := int64(math.Round(math.Hypot(float64(x2-x1), float64(y2-y1))))
	if d < 1 {
		d = 1
	}
	return d
}

// point es un punto entero en metros de mundo.
type point struct{ X, Y int64 }

// borderCrossing calcula el punto en el que el segmento recto from→to cruza la
// frontera común de dos regiones adyacentes. Para vecinos horizontales la
// frontera es vertical (x = axisValue); para verticales, horizontal (y =
// axisValue). El cruce reparte el enlace inter-región en dos segmentos, uno por
// región (GDD 7.3 / 15.1). Como los junctions caen en el interior de sus regiones
// y las regiones son adyacentes, la recta cruza la frontera exactamente una vez.
func borderCrossing(from, to point, vertical bool, axisValue int64) point {
	if vertical {
		// Frontera x = axisValue: interpola y en ese x.
		dx := to.X - from.X
		if dx == 0 {
			return point{X: axisValue, Y: (from.Y + to.Y) / 2}
		}
		t := float64(axisValue-from.X) / float64(dx)
		y := float64(from.Y) + t*float64(to.Y-from.Y)
		return point{X: axisValue, Y: int64(math.Round(y))}
	}
	// Frontera y = axisValue: interpola x en ese y.
	dy := to.Y - from.Y
	if dy == 0 {
		return point{X: (from.X + to.X) / 2, Y: axisValue}
	}
	t := float64(axisValue-from.Y) / float64(dy)
	x := float64(from.X) + t*float64(to.X-from.X)
	return point{X: int64(math.Round(x)), Y: axisValue}
}

// newID genera un UUIDv7 (los IDs los produce la aplicación, ADR-018).
func newID() (uuid.UUID, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return uuid.Nil, fmt.Errorf("worldgen: generando UUIDv7: %w", err)
	}
	return id, nil
}
