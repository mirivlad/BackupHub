export function formatDuration(milliseconds: number | undefined): string {
  if (milliseconds === undefined || !Number.isFinite(milliseconds) || milliseconds < 0) return '—';
  if (milliseconds < 1000) return '< 1 с';
  const seconds = Math.floor(milliseconds / 1000);
  const hours = Math.floor(seconds / 3600);
  const minutes = Math.floor((seconds % 3600) / 60);
  if (hours) return hours + ' ч ' + minutes + ' мин';
  if (minutes) return minutes + ' мин ' + (seconds % 60) + ' с';
  return seconds + ' с';
}
