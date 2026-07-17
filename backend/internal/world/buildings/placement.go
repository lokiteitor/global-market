package buildings

import (
	"encoding/json"
	"fmt"
	"math/big"
	"strings"

	"github.com/lokiteitor/global-market/backend/internal/world/sqlcgen"
)

// parsedRules es la interpretación de building_types.placement_rules soportada
// por este incremento (extensible): near_resource (+ max_distance_m) y
// requires_node_kind. Las claves desconocidas se recogen en Unknown para
// registrarlas con warn e ignorarlas (regla desconocida ⇒ no bloquea).
type parsedRules struct {
	NearResource     string
	HasNearResource  bool
	MaxDistanceM     float64
	HasMaxDistance   bool
	RequiresNodeKind string
	HasRequiresNode  bool
	Unknown          []string
}

// parsePlacementRules interpreta el JSONB de reglas de emplazamiento del tipo.
func parsePlacementRules(raw []byte) (parsedRules, error) {
	var pr parsedRules
	if len(raw) == 0 {
		return pr, nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return pr, fmt.Errorf("world/buildings: placement_rules no es un objeto JSON: %w", err)
	}
	for k, v := range m {
		switch k {
		case "near_resource":
			var s string
			if err := json.Unmarshal(v, &s); err == nil && s != "" {
				pr.NearResource = s
				pr.HasNearResource = true
			}
		case "max_distance_m":
			var f float64
			if err := json.Unmarshal(v, &f); err == nil {
				pr.MaxDistanceM = f
				pr.HasMaxDistance = true
			}
		case "requires_node_kind":
			var s string
			if err := json.Unmarshal(v, &s); err == nil && s != "" {
				pr.RequiresNodeKind = s
				pr.HasRequiresNode = true
			}
		default:
			pr.Unknown = append(pr.Unknown, k)
		}
	}
	return pr, nil
}

// deriveNodeKind deriva el kind del nodo del grafo a partir del tipo: mina→mine,
// resto→factory (decisión del incremento). Una mina es un tipo cuyo code
// contiene "mine" o que declara una regla near_resource.
func deriveNodeKind(code string, rules parsedRules) sqlcgen.WorldNodeKind {
	if strings.Contains(strings.ToLower(code), "mine") || rules.HasNearResource {
		return sqlcgen.WorldNodeKindMine
	}
	return sqlcgen.WorldNodeKindFactory
}

// validNodeKind indica si s es un world.node_kind conocido (una regla
// requires_node_kind con un kind desconocido se ignora con warn, como cualquier
// regla no soportada).
func validNodeKind(s string) bool {
	switch sqlcgen.WorldNodeKind(s) {
	case sqlcgen.WorldNodeKindMine, sqlcgen.WorldNodeKindFactory, sqlcgen.WorldNodeKindWarehouse,
		sqlcgen.WorldNodeKindPort, sqlcgen.WorldNodeKindStation, sqlcgen.WorldNodeKindDistributionCenter,
		sqlcgen.WorldNodeKindJunction, sqlcgen.WorldNodeKindCityGate:
		return true
	}
	return false
}

// levelCurve es la interpretación de building_types.level_curve para el coste de
// mejora. upgrade_cost_factor es un array indexado por nivel (índice L-1 = factor
// para alcanzar el nivel L), misma convención que las demás claves de la curva
// (p. ej. "lines":[1,2,4,8]).
type levelCurve struct {
	UpgradeCostFactor []float64 `json:"upgrade_cost_factor"`
}

// upgradeCost calcula el coste no lineal de subir a destLevel:
// build_cost * factor, donde factor lo da level_curve.upgrade_cost_factor por
// nivel destino; si falta (o no es válido), factor = 2^(destLevel-1). Valida con
// math/big que build_cost*factor no desborda int64.
func upgradeCost(buildCost int64, levelCurveJSON []byte, destLevel int32) (int64, error) {
	factor := defaultUpgradeFactor(destLevel)
	if len(levelCurveJSON) > 0 {
		var lc levelCurve
		if err := json.Unmarshal(levelCurveJSON, &lc); err == nil {
			idx := int(destLevel) - 1
			if idx >= 0 && idx < len(lc.UpgradeCostFactor) {
				if f := int64(lc.UpgradeCostFactor[idx]); f > 0 {
					factor = f
				}
			}
		}
	}
	cost := new(big.Int).Mul(big.NewInt(buildCost), big.NewInt(factor))
	if !cost.IsInt64() {
		return 0, ErrOverflow
	}
	return cost.Int64(), nil
}

// defaultUpgradeFactor es 2^(destLevel-1) (factor por defecto, documentado).
func defaultUpgradeFactor(destLevel int32) int64 {
	if destLevel <= 1 {
		return 1
	}
	factor := int64(1)
	for i := int32(1); i < destLevel; i++ {
		factor *= 2
	}
	return factor
}
