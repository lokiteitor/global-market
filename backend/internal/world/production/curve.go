package production

import (
	"encoding/json"
	"math"
	"strings"
)

// La level_curve del tipo (building_types.level_curve, JSONB) parametriza el
// efecto del nivel del edificio sobre la producción (GDD 6.3). Este incremento
// aplica dos claves; el resto se documenta como expansión:
//
//   - "speed_mult":   factor de velocidad por nivel (índice nivel-1). Reduce la
//     duración efectiva del lote: duración = batch_sim_seconds / speed_mult.
//   - "storage_mult": factor de capacidad de almacén por nivel (índice nivel-1).
//     Capacidad = base_storage * storage_mult.
//   - "lines" y "efficiency_mult": líneas paralelas y eficiencia de insumos;
//     documentadas pero NO aplicadas en este incremento (un lote en curso por
//     edificio; ver worker.go).
//
// Defaults documentados cuando la clave falta o es inválida: speed_mult y
// storage_mult = nivel (crecientes: nivel 1 → 1×, nivel 2 → 2×, …); lines =
// 2^(nivel-1).

// curveValue lee curve[key][level-1] de un JSON de level_curve; ok=false si
// falta o no es un número positivo utilizable.
func curveValue(raw []byte, key string, level int32) (float64, bool) {
	if len(raw) == 0 || level < 1 {
		return 0, false
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil {
		return 0, false
	}
	v, ok := m[key]
	if !ok {
		return 0, false
	}
	var arr []float64
	if json.Unmarshal(v, &arr) != nil {
		return 0, false
	}
	idx := int(level) - 1
	if idx < 0 || idx >= len(arr) {
		return 0, false
	}
	return arr[idx], true
}

// defaultLevelMult es el multiplicador por defecto de speed/storage: el propio
// nivel (>= 1).
func defaultLevelMult(level int32) float64 {
	if level < 1 {
		return 1
	}
	return float64(level)
}

// speedMult devuelve el factor de velocidad del nivel (default = nivel).
func speedMult(raw []byte, level int32) float64 {
	if v, ok := curveValue(raw, "speed_mult", level); ok && v > 0 {
		return v
	}
	return defaultLevelMult(level)
}

// storageMult devuelve el factor de capacidad del nivel (default = nivel).
func storageMult(raw []byte, level int32) float64 {
	if v, ok := curveValue(raw, "storage_mult", level); ok && v > 0 {
		return v
	}
	return defaultLevelMult(level)
}

// effectiveBatchSeconds es la duración efectiva de un lote a un nivel dado:
// batch_sim_seconds / speed_mult, redondeada y con suelo 1 (nunca instantánea).
func effectiveBatchSeconds(batchSimSeconds int64, raw []byte, level int32) int64 {
	mult := speedMult(raw, level)
	if mult <= 0 {
		mult = 1
	}
	eff := int64(math.Round(float64(batchSimSeconds) / mult))
	if eff < 1 {
		eff = 1
	}
	return eff
}

// storageCapacity es la capacidad física del almacén del edificio a un nivel:
// base_storage * storage_mult, acotada a int64.
func storageCapacity(baseStorage int64, raw []byte, level int32) int64 {
	mult := storageMult(raw, level)
	if mult < 1 {
		mult = 1
	}
	capF := float64(baseStorage) * mult
	if capF >= float64(math.MaxInt64) {
		return math.MaxInt64
	}
	return int64(capF)
}

// isMine indica si el tipo de edificio es una mina (extrae de un yacimiento en
// vez de manufacturar): su code contiene "mine" o declara near_resource en sus
// placement_rules (misma convención que world/buildings).
func isMine(code string, placementRules []byte) bool {
	if strings.Contains(strings.ToLower(code), "mine") {
		return true
	}
	if len(placementRules) > 0 {
		var m map[string]json.RawMessage
		if json.Unmarshal(placementRules, &m) == nil {
			if _, ok := m["near_resource"]; ok {
				return true
			}
		}
	}
	return false
}

// extractionRadiusM es el radio de influencia (metros de mundo) con el que una
// mina alcanza su yacimiento: placement_rules.max_distance_m del tipo, o el
// default documentado si no lo declara.
func extractionRadiusM(placementRules []byte) float64 {
	if len(placementRules) > 0 {
		var m map[string]json.RawMessage
		if json.Unmarshal(placementRules, &m) == nil {
			if v, ok := m["max_distance_m"]; ok {
				var f float64
				if json.Unmarshal(v, &f) == nil && f > 0 {
					return f
				}
			}
		}
	}
	return DefaultExtractionRadiusM
}
