export type CheckMode = 'metadata' | 'data';
export type CheckState = 'unknown' | 'queued' | 'running' | 'passed' | 'failed' | 'interrupted' | 'stale';
export type CheckStatus = {state: CheckState; finished_at?: string};
export type VerificationRun = {
 id: string;
 kind: CheckMode | 'restore';
 state: CheckState;
 queued_at: string;
 started_at?: string;
 finished_at?: string;
 duration_ms?: number;
 detail?: string;
 selection?: string;
 progress?: number;
 files?: number;
 bytes?: number;
};
export type CheckSchedule = {mode: CheckMode; interval_hours: number; next_run?: string};

const labels: Record<CheckState, string> = {
 unknown: 'Не проверено',
 queued: 'В очереди',
 running: 'Выполняется',
 passed: 'Проверено',
 failed: 'Ошибка',
 interrupted: 'Прервано',
 stale: 'Проверка устарела'
};

export function checkLabel(state: CheckState) {
 return labels[state];
}
export function checkTone(state: CheckState) {
 if (state === 'passed') return 'good-state';
 if (state === 'queued' || state === 'running') return 'info-state';
 return 'warn-state';
}
export function checkDate(value: string) {
 return new Date(value).toLocaleString('ru-RU', {dateStyle: 'short', timeStyle: 'short'});
}
export function checkActive(state?: CheckState) {
 return state === 'queued' || state === 'running';
}

export function restoreCheckLabel(state: CheckState) {
 if (state === 'passed') return 'Проба пройдена';
 if (state === 'failed') return 'Проба не пройдена';
 return checkLabel(state);
}
