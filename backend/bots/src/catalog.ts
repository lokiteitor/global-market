/**
 * Catálogos estáticos del mundo, cargados una vez por bot al arrancar
 * (productos, tipos de edificio, recetas, regiones, ciudades).
 */

import type { ApiClient } from "./client.js";
import type { BuildingType, City, Product, Recipe, Region } from "./types.js";

export interface Catalog {
  /** por code ('iron_ore', 'coal', …) */
  products: Map<string, Product>;
  /** por code ('iron_mine', 'blast_furnace', …) */
  buildingTypes: Map<string, BuildingType>;
  /** por code ('mine_iron', 'smelt_steel', …) */
  recipes: Map<string, Recipe>;
  /** por name ('Norte', 'Este', …) */
  regions: Map<string, Region>;
  /** por name ('Ferrópolis', 'Costaverde') */
  cities: Map<string, City>;
}

export async function loadCatalog(client: ApiClient): Promise<Catalog> {
  const [products, buildingTypes, recipes, regions, cities] = await Promise.all([
    client.get<Product[]>("/world/products", { limit: 200 }),
    client.get<BuildingType[]>("/world/building-types", { limit: 200 }),
    client.get<Recipe[]>("/world/recipes", { limit: 200 }),
    client.get<Region[]>("/world/regions", { limit: 200 }),
    client.get<City[]>("/world/cities", { limit: 200 }),
  ]);
  return {
    products: new Map(products.data.map((p) => [p.code, p])),
    buildingTypes: new Map(buildingTypes.data.map((b) => [b.code, b])),
    recipes: new Map(recipes.data.map((r) => [r.code, r])),
    regions: new Map(regions.data.map((r) => [r.name, r])),
    cities: new Map(cities.data.map((c) => [c.name, c])),
  };
}

export function required<T>(value: T | undefined, what: string): T {
  if (value === undefined) throw new Error(`catálogo incompleto: falta ${what}`);
  return value;
}
