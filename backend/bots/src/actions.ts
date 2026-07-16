/**
 * Acciones que un arquetipo puede decidir en un tick (máx. 1 escritura/tick)
 * y utilidades puras compartidas (aritmética entera de precios y geometría).
 */

import type { GeoPolygon } from "./types.js";

export type BotAction =
  | { type: "none"; reason: string }
  | { type: "create_concession"; regionId: string; parcel: GeoPolygon }
  | {
      type: "create_building";
      buildingTypeId: string;
      concessionId: string;
      footprint: GeoPolygon;
    }
  | { type: "queue_batches"; buildingId: string; recipeId: string; batches: number }
  | {
      type: "publish_sell";
      productId: string;
      quantity: bigint;
      unitPrice: bigint;
      originNodeId: string;
    }
  | {
      type: "publish_buy";
      productId: string;
      quantity: bigint;
      unitPrice: bigint;
      destinationNodeId: string;
    }
  | {
      type: "accept_publication";
      publicationId: string;
      quantity: bigint;
      /** Contexto para logging y memoria del arbitrajista. */
      productId: string;
      unitPrice: bigint;
      side: "buying" | "selling";
    };

export function none(reason: string): BotAction {
  return { type: "none", reason };
}

/** Parseo estricto de importes/stock serializados como string decimal. */
export function toBig(value: string): bigint {
  if (!/^-?[0-9]+$/.test(value)) {
    throw new Error(`importe no entero: ${JSON.stringify(value)}`);
  }
  return BigInt(value);
}

/** (value × num) / den con truncamiento — nunca floats para valor económico. */
export function mulDiv(value: bigint, num: bigint, den: bigint): bigint {
  return (value * num) / den;
}

/** Cuadrado GeoJSON de lado 2×half grados centrado en (lon, lat). */
export function squareAround(lon: number, lat: number, half: number): GeoPolygon {
  return {
    type: "Polygon",
    coordinates: [
      [
        [lon - half, lat - half],
        [lon + half, lat - half],
        [lon + half, lat + half],
        [lon - half, lat + half],
        [lon - half, lat - half],
      ],
    ],
  };
}

/** Centroide aproximado del anillo exterior (media de vértices sin repetir el cierre). */
export function polygonCenter(polygon: GeoPolygon): [number, number] {
  const ring = polygon.coordinates[0];
  if (!ring || ring.length < 4) throw new Error("polígono sin anillo exterior válido");
  const vertices = ring.slice(0, ring.length - 1);
  let lon = 0;
  let lat = 0;
  for (const v of vertices) {
    lon += v[0] ?? 0;
    lat += v[1] ?? 0;
  }
  return [lon / vertices.length, lat / vertices.length];
}
