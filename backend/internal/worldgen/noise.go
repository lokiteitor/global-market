package worldgen

// Value-noise 2D determinista, propio (sin dependencias externas): la base de la
// generación procedural del mundo (GDD 9). Una rejilla infinita de vértices
// enteros toma un valor pseudoaleatorio en [0,1) derivado por hash de la semilla
// II_WORLD_SEED y las coordenadas del vértice; el valor en un punto continuo se
// interpola entre los cuatro vértices de su celda con una curva de suavizado
// (fade quíntico), y se suman varias octavas de frecuencia creciente y amplitud
// decreciente (ruido fractal). Es determinista y sin estado: la misma semilla y
// las mismas coordenadas producen SIEMPRE el mismo valor, y el resultado no
// depende del orden de evaluación (a diferencia de un stream de math/rand). Las
// capas elevation() y humidity() usan canales disjuntos para no correlacionarse.

import "math"

// Constantes de mezcla estilo splitmix64 (finalizador de 64 bits).
const (
	mixGamma = 0x9E3779B97F4A7C15
	mixA     = 0xBF58476D1CE4E5B9
	mixB     = 0x94D049BB133111EB
)

// hash64 mezcla un entero de 64 bits en un valor pseudoaleatorio uniforme
// (finalizador splitmix64). Determinista, sin estado.
func hash64(x uint64) uint64 {
	x += mixGamma
	x = (x ^ (x >> 30)) * mixA
	x = (x ^ (x >> 27)) * mixB
	return x ^ (x >> 31)
}

// Canales de ruido: cada capa procedural usa una base de canal muy separada para
// que sus octavas (base+i) no compartan lattice con las de otra capa, evitando
// correlación entre elevación y humedad. noiseOctaves y las constantes de fractal
// definen el detalle del ruido.
const (
	elevationChannel  uint64 = 1000
	humidityChannel   uint64 = 2000
	noiseOctaves             = 4
	fractalLacunarity        = 2.0 // factor de frecuencia entre octavas
	fractalGain              = 0.5 // factor de amplitud entre octavas
)

// noise es el generador de value-noise sembrado por II_WORLD_SEED.
type noise struct {
	seed uint64
}

// newNoise construye el generador con la semilla del mundo.
func newNoise(seed int64) *noise {
	return &noise{seed: uint64(seed)} //nolint:gosec // la semilla es un identificador, no un entero con signo
}

// latticeValue devuelve el valor pseudoaleatorio en [0,1) del vértice entero
// (ix,iy) para un canal dado. El hash combina semilla, canal y coordenadas; el
// desplazamiento a 53 bits produce un flotante uniforme exacto en [0,1).
func (n *noise) latticeValue(ix, iy int64, channel uint64) float64 {
	h := hash64(n.seed ^ (channel * mixGamma))
	h = hash64(h ^ uint64(ix)) //nolint:gosec // reinterpretación de bits para el hash
	h = hash64(h ^ (uint64(iy) * mixA))
	return float64(h>>11) / float64(uint64(1)<<53)
}

// fade es la curva de suavizado quíntica 6t^5-15t^4+10t^3 (primera y segunda
// derivadas nulas en 0 y 1): garantiza continuidad C2 entre celdas del lattice.
func fade(t float64) float64 {
	return t * t * t * (t*(t*6-15) + 10)
}

// lerp interpola linealmente entre a y b.
func lerp(a, b, t float64) float64 {
	return a + (b-a)*t
}

// value2D evalúa una octava de value-noise en (x,y): interpola con fade los
// cuatro vértices enteros que rodean el punto. El resultado está en [0,1).
func (n *noise) value2D(x, y float64, channel uint64) float64 {
	x0 := int64(math.Floor(x))
	y0 := int64(math.Floor(y))
	fx := x - float64(x0)
	fy := y - float64(y0)

	v00 := n.latticeValue(x0, y0, channel)
	v10 := n.latticeValue(x0+1, y0, channel)
	v01 := n.latticeValue(x0, y0+1, channel)
	v11 := n.latticeValue(x0+1, y0+1, channel)

	ux := fade(fx)
	uy := fade(fy)
	top := lerp(v00, v10, ux)
	bottom := lerp(v01, v11, ux)
	return lerp(top, bottom, uy)
}

// fractal suma noiseOctaves octavas de value-noise (frecuencia ×lacunarity,
// amplitud ×gain por octava) y normaliza por la amplitud total, devolviendo un
// valor en [0,1]. Cada octava usa un canal disjunto (channel+i) para decorrelar
// las escalas.
func (n *noise) fractal(x, y float64, channel uint64, octaves int) float64 {
	amp := 1.0
	freq := 1.0
	sum := 0.0
	norm := 0.0
	for i := 0; i < octaves; i++ {
		sum += amp * n.value2D(x*freq, y*freq, channel+uint64(i)) //nolint:gosec // i acotado por octaves
		norm += amp
		amp *= fractalGain
		freq *= fractalLacunarity
	}
	if norm == 0 {
		return 0
	}
	return sum / norm
}

// elevation devuelve la elevación normalizada en [0,1] del punto (x,y) en
// espacio de ruido (coordenadas de mundo divididas por el lado de región).
func (n *noise) elevation(x, y float64) float64 {
	return n.fractal(x, y, elevationChannel, noiseOctaves)
}

// humidity devuelve la humedad normalizada en [0,1] del punto (x,y) en espacio de
// ruido, en un canal disjunto del de elevación.
func (n *noise) humidity(x, y float64) float64 {
	return n.fractal(x, y, humidityChannel, noiseOctaves)
}
