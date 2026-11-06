import type { Service, Status } from './api'

export const STATUS_LABEL: Record<Status, string> = {
  ok: '정상',
  warning: '주의',
  critical: '장애',
  stopped: '중지됨',
  disconnected: '연결 끊김',
}

// serviceHeadline은 컨테이너 실행 상태와 앱 응답 상태를 분리해 한 줄로 표시한다.
export function serviceHeadline(service: Service): string {
  const first = service.containers[0]
  switch (service.runtime) {
    case 'running':
      switch (service.response) {
        case 'down':
          return '실행 중 / 무응답'
        case 'failing':
          return '실행 중 / 응답 확인 실패'
        case 'ok':
          return '실행 중 / 응답 정상'
        case 'historical':
          return '실행 중 / 당시 응답 미표시'
        default:
          return '실행 중 / 응답 미확인'
      }
    case 'partial':
      return '일부 컨테이너만 실행 중'
    case 'restarting':
      return '재시작 중'
    default:
      if (first?.one_off) return `작업 종료 (exit ${first.exit_code})`
      if (first?.oom_killed) return `종료됨 (OOM, exit ${first.exit_code})`
      return `종료됨 (exit ${first?.exit_code ?? '?'})`
  }
}

export function projectLabel(key: string): string {
  return key === '_standalone' ? '미분류' : key
}

export function serviceLabel(key: string): string {
  const [project, name] = key.split('/')
  return project === '_standalone' ? name : key
}
