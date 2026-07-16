// Sim-time legible (ADR-IMPL-06): `AÑO-DDD-HH:MM` con año = días_totales/360 + 1
// y DDD = día del año 001..360, calculado desde los segundos de sim-time.
const SIM_DAY = 86_400;
const DAYS_PER_YEAR = 360;

export function formatSimTime(simSeconds: number): string {
  const s = Math.max(0, Math.floor(simSeconds));
  const totalDays = Math.floor(s / SIM_DAY);
  const year = Math.floor(totalDays / DAYS_PER_YEAR) + 1;
  const dayOfYear = (totalDays % DAYS_PER_YEAR) + 1;
  const rem = s % SIM_DAY;
  const hh = Math.floor(rem / 3600);
  const mm = Math.floor((rem % 3600) / 60);
  const ddd = String(dayOfYear).padStart(3, '0');
  const h2 = String(hh).padStart(2, '0');
  const m2 = String(mm).padStart(2, '0');
  return `${year}-${ddd}-${h2}:${m2}`;
}
