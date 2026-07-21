package worldgen

// Clasificación de biomas (GDD 9): a partir de la elevación y la humedad (ambas
// value-noise en [0,1] muestreadas en el centro de la celda) una tabla de
// decisión determinista asigna uno de los valores del enum world.biome. El orden
// de las reglas es significativo (agua y montaña dominan sobre el eje húmedo):
//
//	elev <= oceanElevMax                     -> ocean     (agua profunda)
//	elev <= coastElevMax                     -> coast     (franja baja litoral)
//	elev >= mountainElevMin                  -> mountain  (tierra alta)
//	elev medio  &&  humidity <  desertHumMax -> desert    (medio y seco)
//	elev medio  &&  humidity >= forestHumMin -> forest    (medio y húmedo)
//	resto                                    -> plains
//
// coast y ocean habilitan las rutas marítimas inter-región; ocean es el único
// bioma "de agua" (sin ciudad ni yacimientos), coast es litoral terrestre.

// Biomas del enum world.biome.
const (
	BiomePlains   = "plains"
	BiomeForest   = "forest"
	BiomeDesert   = "desert"
	BiomeMountain = "mountain"
	BiomeOcean    = "ocean"
	BiomeCoast    = "coast"
)

// Umbrales de la tabla de decisión (deterministas, documentados y cubiertos por
// tests). Calibrados para que una grilla 3×3 con II_WORLD_SEED por defecto
// produzca una mezcla de biomas terrestres y litorales/agua.
const (
	oceanElevMax    = 0.28 // elev <= => océano
	coastElevMax    = 0.36 // franja baja junto al océano => costa
	mountainElevMin = 0.70 // elev alto => montaña
	desertHumidMax  = 0.38 // tierra media y seca => desierto
	forestHumidMin  = 0.60 // tierra media y húmeda => bosque
)

// biomeFor aplica la tabla de decisión de biomas a la elevación y humedad de una
// celda. Determinista y total (siempre devuelve un bioma del enum).
func biomeFor(elev, humidity float64) string {
	switch {
	case elev <= oceanElevMax:
		return BiomeOcean
	case elev <= coastElevMax:
		return BiomeCoast
	case elev >= mountainElevMin:
		return BiomeMountain
	case humidity < desertHumidMax:
		return BiomeDesert
	case humidity >= forestHumidMin:
		return BiomeForest
	default:
		return BiomePlains
	}
}

// isTerrestrial indica si el bioma es tierra habitable (todo salvo océano): las
// regiones terrestres reciben ciudades, yacimientos y red vial.
func isTerrestrial(biome string) bool {
	return biome != BiomeOcean
}

// isCoastalOrOcean indica si el bioma da acceso al mar (costa u océano): los
// enlaces inter-región que tocan una de estas regiones son marítimos (sea).
func isCoastalOrOcean(biome string) bool {
	return biome == BiomeCoast || biome == BiomeOcean
}
