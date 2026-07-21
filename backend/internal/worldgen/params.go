package worldgen

// Parámetros del mundo generado: reglas fiscales y densidad por bioma, catálogo
// de vehículos ferroviarios/marítimos y constantes de la red inter-región. Todo
// determinista; la aleatoriedad (conteos y posiciones) sale de un RNG sembrado
// por (semilla, grid_x, grid_y), nunca de time/entropía.

import (
	"hash/fnv"
	"math/rand"

	"github.com/google/uuid"
)

// biomeParams son las palancas de una región según su bioma: fiscalidad (sink de
// canon e impuestos) y densidad de ciudades/yacimientos. Rangos [min,max]
// inclusivos; el RNG de la celda elige dentro del rango.
type biomeParams struct {
	taxBP       int
	customsBP   int
	canonBase   int64
	cityMin     int
	cityMax     int
	depositMin  int
	depositMax  int
	baseSalary  int64
	population  int64
	cityLevel   int
	supplyIndex int64
	influenceM  int
	demandD0    int64
	demandPrice int64 // dentro de [iron_ore.price_floor, iron_ore.price_ceiling] = [20,400]
}

// paramsForBiome devuelve las palancas de un bioma. Las regiones montañosas y
// litorales son más caras (recursos/puertos valiosos); el desierto, barato.
func paramsForBiome(biome string) biomeParams {
	base := biomeParams{
		taxBP: 500, customsBP: 200, canonBase: 1_000,
		cityMin: 1, cityMax: 2, depositMin: 2, depositMax: 4,
		baseSalary: 30, population: 40_000, cityLevel: 2,
		supplyIndex: 150_000, influenceM: 20_000,
		demandD0: 1_000, demandPrice: 100,
	}
	switch biome {
	case BiomeMountain:
		base.taxBP, base.customsBP, base.canonBase = 600, 200, 1_500
		base.cityMin, base.cityMax = 1, 1
		base.depositMin, base.depositMax = 3, 4
		base.population, base.baseSalary = 30_000, 35
	case BiomeDesert:
		base.taxBP, base.customsBP, base.canonBase = 300, 150, 500
		base.cityMin, base.cityMax = 1, 1
		base.depositMin, base.depositMax = 2, 3
		base.population, base.baseSalary = 25_000, 28
	case BiomeForest:
		base.taxBP, base.customsBP, base.canonBase = 500, 200, 1_000
		base.population, base.baseSalary = 45_000, 30
	case BiomeCoast:
		base.taxBP, base.customsBP, base.canonBase = 450, 300, 1_200
		base.population, base.baseSalary = 55_000, 32
	case BiomePlains:
		base.taxBP, base.customsBP, base.canonBase = 500, 200, 1_000
		base.population, base.baseSalary = 50_000, 30
	}
	return base
}

// depositProducts elige, de forma determinista con el RNG de la celda, los
// productos de los `count` yacimientos de una región según su bioma (recursos
// correlados: montaña rica en hierro y carbón; desierto/costa carbón; bosque y
// llanura hierro). Solo usa productos existentes (iron_ore, coal): madera y
// petróleo aún no están en el catálogo (GDD 10, mandato Incremento 7).
func depositProducts(biome string, rng *rand.Rand, ironOreID, coalID uuid.UUID, count int) []uuid.UUID {
	// weights: probabilidad relativa de iron_ore frente a coal (sobre 100).
	ironWeight := 50
	switch biome {
	case BiomeMountain:
		ironWeight = 60 // veta rica: mezcla equilibrada con sesgo a hierro
	case BiomeDesert:
		ironWeight = 20 // predominio de carbón
	case BiomeCoast:
		ironWeight = 35
	case BiomeForest:
		ironWeight = 70
	case BiomePlains:
		ironWeight = 65
	}
	out := make([]uuid.UUID, 0, count)
	for i := 0; i < count; i++ {
		if rng.Intn(100) < ironWeight {
			out = append(out, ironOreID)
		} else {
			out = append(out, coalID)
		}
	}
	return out
}

// depositAmount es el tamaño (finito, grande) de un yacimiento generado. Grande
// para que una región sostenga producción durante mucho tiempo de juego antes de
// declinar (GDD 10).
const depositAmount int64 = 3_000_000

// cellRNG construye un RNG determinista para la celda (gx,gy) a partir de la
// semilla del mundo. Derivarlo por celda (en vez de un único stream secuencial)
// hace la generación independiente del orden y robusta ante reintentos.
func cellRNG(seed int64, gx, gy int) *rand.Rand {
	h := fnv.New64a()
	var buf [8]byte
	put := func(v uint64) {
		for i := 0; i < 8; i++ {
			buf[i] = byte(v >> (8 * i))
		}
		_, _ = h.Write(buf[:])
	}
	put(uint64(seed))                                 //nolint:gosec // mezcla de bits
	put(uint64(int64(gx)))                            //nolint:gosec // mezcla de bits
	put(uint64(int64(gy)))                            //nolint:gosec // mezcla de bits
	return rand.New(rand.NewSource(int64(h.Sum64()))) //nolint:gosec // PRNG de simulación determinista, no criptográfico
}

// intInRange devuelve un entero en [min,max] (inclusivo) del RNG dado.
func intInRange(rng *rand.Rand, min, max int) int {
	if max <= min {
		return min
	}
	return min + rng.Intn(max-min+1)
}

// ─── Catálogo de vehículos ferroviarios y marítimos (GDD 8) ───────────────────

// railSeaVehicleTypes es el catálogo inter-región (Fase 2): un tren de carga y un
// buque de carga, ambos con combustible coal (único combustible del mundo). Sus
// velocidades encajan con las de los enlaces rail/sea (el motor de tránsito toma
// min(vehículo, enlace) por segmento). Idempotentes por code.
//
// MATRIZ COSTE / VELOCIDAD / VOLUMEN (GDD 7.2/8) — el eje de decisión modal:
//
//	tipo           modo  capacidad  velocidad  precio     coste/unidad  nicho
//	truck_small    road    2 000       80        40 000       20,0       flexible, caro/unidad, puerta-a-puerta
//	truck_large    road    6 000       70        90 000       15,0       terrestre a granel corto
//	freight_train  rail   40 000      120       500 000       12,5       gran volumen RÁPIDO en tierra
//	cargo_ship     sea   120 000       40     1 200 000       10,0       volumen ENORME, lento, único por mar
//
// El coste/unidad (precio/capacidad) DECRECE de camión→tren→barco: el barco es el
// más barato por unidad pero el más lento; el tren mueve gran volumen deprisa por
// tierra; el camión es flexible pero caro por unidad. Rail/sea solo existen en el
// mundo multi-región (por eso viven aquí, junto a los enlaces que recorren), no en
// el seed de Askadia (solo road).
var railSeaVehicleTypes = []vehicleTypeSpec{
	{code: "freight_train", name: "Tren de carga", mode: "rail", cargoCapacity: 40_000,
		speedKmh: 120, fuelPer100km: 60, autonomyKm: 3_000, purchasePrice: 500_000, operatingCostPerDay: 800},
	{code: "cargo_ship", name: "Buque de carga", mode: "sea", cargoCapacity: 120_000,
		speedKmh: 40, fuelPer100km: 120, autonomyKm: 8_000, purchasePrice: 1_200_000, operatingCostPerDay: 1_500},
}

// vehicleTypeSpec describe un tipo de vehículo del catálogo (réplica de la del
// seed para no acoplar paquetes).
type vehicleTypeSpec struct {
	code                string
	name                string
	mode                string
	cargoCapacity       int64
	speedKmh            int32
	fuelPer100km        int64
	autonomyKm          int32
	purchasePrice       int64
	operatingCostPerDay int64
}

// ─── Parámetros de la red inter-región (GDD 7.2) ──────────────────────────────

const (
	// Enlaces RAIL: alta capacidad, velocidad de tren.
	railCapacityPerHour int32 = 200
	railBaseSpeedKmh    int32 = 120
	// Enlaces SEA: capacidad muy alta, velocidad de buque.
	seaCapacityPerHour int32 = 400
	seaBaseSpeedKmh    int32 = 40
	// Enlaces ROAD intra-región (mismos parámetros que el seed de Askadia).
	genRoadCapacityPerHour int32 = 60
	genRoadBaseSpeedKmh    int32 = 80
	// Terminales intermodales: capacidad de transbordo por hora (GDD 7.3).
	terminalTransshipmentPerHour int32 = 120
	// Slots de prioridad vendibles por terminal (GDD 7.3): cada terminal ofrece
	// terminalSlotTiers slots de priority_tier 1..N a la venta. El PRECIO crece con
	// la prioridad (tier 1 = mejor prioridad = más caro): precio(tier k) =
	// terminalSlotBasePrice · (N − k + 1). El tier N (menor prioridad) cuesta la base.
	terminalSlotTiers     int   = 3
	terminalSlotBasePrice int64 = 10_000
)
