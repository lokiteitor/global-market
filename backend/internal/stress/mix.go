package stress

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Archetype es un arquetipo de carga del harness. Son los cuatro arquetipos del
// GDD §13.2, pero con comportamientos LIGEROS y de alta frecuencia orientados a
// generar CARGA (no a jugar bien): el gameplay «bueno» de cada arquetipo vive en
// internal/bots.
type Archetype string

const (
	// ArchetypeProducer carga el camino de catálogo del mundo, inventario propio
	// y publicación (productor primario del GDD §13.2).
	ArchetypeProducer Archetype = "producer"
	// ArchetypeTrader carga el tablón con filtros, el histórico OHLC y las
	// aceptaciones (comerciante/arbitrajista).
	ArchetypeTrader Archetype = "trader"
	// ArchetypeFreighter carga la red logística y el planificador de rutas
	// (transportista).
	ArchetypeFreighter Archetype = "freighter"
	// ArchetypeTransformer carga recetas, tablón de insumos y contratos
	// (transformador industrial).
	ArchetypeTransformer Archetype = "transformer"
)

// Archetypes es el orden canónico de los arquetipos del harness (determinista:
// fija el desempate del reparto y el orden de los informes).
var Archetypes = []Archetype{ArchetypeProducer, ArchetypeTrader, ArchetypeFreighter, ArchetypeTransformer}

// BotArchetype traduce el arquetipo de carga al valor del enum
// auth.bot_archetype que persiste el provisioner.
func (a Archetype) BotArchetype() string {
	switch a {
	case ArchetypeProducer:
		return "primary_producer"
	case ArchetypeTrader:
		return "arbitrageur"
	case ArchetypeFreighter:
		return "freighter"
	case ArchetypeTransformer:
		return "industrial_transformer"
	}
	return ""
}

// Valid informa de si el arquetipo es uno de los soportados.
func (a Archetype) Valid() bool { return a.BotArchetype() != "" }

// DefaultMixSpec es la mezcla por defecto (porcentajes) de II_STRESS_MIX.
const DefaultMixSpec = "producer=50,trader=30,freighter=10,transformer=10"

// Mix es la mezcla de arquetipos de una corrida: pesos por arquetipo en el
// orden en que se declararon (el orden fija el desempate del reparto).
type Mix struct {
	// Order son los arquetipos en orden de declaración.
	Order []Archetype
	// Weights es el peso de cada arquetipo (>= 0; al menos uno > 0). Si los
	// pesos suman 100 se leen directamente como porcentajes.
	Weights map[Archetype]int
}

// ParseMix interpreta la especificación de mezcla "producer=50,trader=30,…".
// Los pesos son enteros >= 0 y no necesitan sumar 100 (se normalizan); si suman
// 100 son literalmente porcentajes. Rechaza arquetipos desconocidos, duplicados,
// pesos negativos y mezclas con todos los pesos a cero.
func ParseMix(spec string) (Mix, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return Mix{}, fmt.Errorf("stress: la mezcla de arquetipos no puede estar vacía (formato %q)", DefaultMixSpec)
	}
	m := Mix{Weights: map[Archetype]int{}}
	total := 0
	for _, entry := range strings.Split(spec, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		rawName, rawWeight, ok := strings.Cut(entry, "=")
		if !ok {
			return Mix{}, fmt.Errorf("stress: entrada de mezcla inválida %q (formato arquetipo=peso)", entry)
		}
		name := Archetype(strings.ToLower(strings.TrimSpace(rawName)))
		if !name.Valid() {
			return Mix{}, fmt.Errorf("stress: arquetipo desconocido %q en la mezcla (válidos: %s)", name, joinArchetypes(Archetypes))
		}
		if _, dup := m.Weights[name]; dup {
			return Mix{}, fmt.Errorf("stress: arquetipo %q duplicado en la mezcla", name)
		}
		w, err := strconv.Atoi(strings.TrimSpace(rawWeight))
		if err != nil {
			return Mix{}, fmt.Errorf("stress: peso inválido %q para el arquetipo %q (entero >= 0): %w", rawWeight, name, err)
		}
		if w < 0 {
			return Mix{}, fmt.Errorf("stress: el peso del arquetipo %q no puede ser negativo (%d)", name, w)
		}
		m.Order = append(m.Order, name)
		m.Weights[name] = w
		total += w
	}
	if len(m.Order) == 0 {
		return Mix{}, fmt.Errorf("stress: la mezcla de arquetipos no declara ninguna entrada (formato %q)", DefaultMixSpec)
	}
	if total == 0 {
		return Mix{}, fmt.Errorf("stress: la mezcla de arquetipos tiene todos los pesos a cero")
	}
	return m, nil
}

// String reconstruye la especificación canónica de la mezcla.
func (m Mix) String() string {
	parts := make([]string, 0, len(m.Order))
	for _, a := range m.Order {
		parts = append(parts, fmt.Sprintf("%s=%d", a, m.Weights[a]))
	}
	return strings.Join(parts, ",")
}

// TotalWeight es la suma de los pesos declarados.
func (m Mix) TotalWeight() int {
	total := 0
	for _, a := range m.Order {
		total += m.Weights[a]
	}
	return total
}

// Counts reparte total bots entre los arquetipos de la mezcla por el método del
// RESTO MAYOR: cada arquetipo recibe floor(total*peso/suma) y los puestos
// sobrantes van a los mayores restos (desempate por orden de declaración). La
// suma de los cupos es SIEMPRE total.
func (m Mix) Counts(total int) map[Archetype]int {
	counts := make(map[Archetype]int, len(m.Order))
	if total <= 0 {
		for _, a := range m.Order {
			counts[a] = 0
		}
		return counts
	}
	sum := m.TotalWeight()
	type slot struct {
		archetype Archetype
		remainder int // resto escalado por sum (entero: sin floats)
		position  int
	}
	assigned := 0
	slots := make([]slot, 0, len(m.Order))
	for i, a := range m.Order {
		num := total * m.Weights[a]
		q := num / sum
		counts[a] = q
		assigned += q
		slots = append(slots, slot{archetype: a, remainder: num - q*sum, position: i})
	}
	sort.SliceStable(slots, func(i, j int) bool {
		if slots[i].remainder != slots[j].remainder {
			return slots[i].remainder > slots[j].remainder
		}
		return slots[i].position < slots[j].position
	})
	for i := 0; assigned < total; i++ {
		s := slots[i%len(slots)]
		if m.Weights[s.archetype] == 0 {
			// Un arquetipo con peso 0 nunca recibe bots salvo que no haya otro.
			if !m.hasPositiveWeight() {
				counts[s.archetype]++
				assigned++
			}
			continue
		}
		counts[s.archetype]++
		assigned++
	}
	return counts
}

// hasPositiveWeight informa de si algún arquetipo tiene peso > 0.
func (m Mix) hasPositiveWeight() bool {
	for _, a := range m.Order {
		if m.Weights[a] > 0 {
			return true
		}
	}
	return false
}

// Allocate devuelve la asignación arquetipo→bot para total bots, INTERCALADA en
// el orden de declaración (producer, trader, freighter, transformer, producer…)
// para que la rampa de arranque mezcle arquetipos desde el primer segundo en
// lugar de arrancar primero toda una familia.
func (m Mix) Allocate(total int) []Archetype {
	counts := m.Counts(total)
	out := make([]Archetype, 0, max(total, 0))
	remaining := make(map[Archetype]int, len(counts))
	for a, n := range counts {
		remaining[a] = n
	}
	for len(out) < total {
		progressed := false
		for _, a := range m.Order {
			if remaining[a] <= 0 {
				continue
			}
			out = append(out, a)
			remaining[a]--
			progressed = true
			if len(out) == total {
				break
			}
		}
		if !progressed {
			break
		}
	}
	return out
}

// joinArchetypes formatea una lista de arquetipos para los mensajes de error.
func joinArchetypes(as []Archetype) string {
	parts := make([]string, 0, len(as))
	for _, a := range as {
		parts = append(parts, string(a))
	}
	return strings.Join(parts, "|")
}
