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

const TYPE_LABEL: Record<string, string> = {
  'container.create': '생성',
  'container.start': '시작',
  'container.restart': '재시작',
  'container.stop': '중지',
  'container.kill': '강제 종료',
  'container.die': '종료',
  'container.oom': 'OOM',
  'container.destroy': '삭제',
  'container.health': '헬스체크',
  'container.pause': '일시 정지',
  'container.unpause': '정지 해제',
  'container.rename': '이름 변경',
  'network.connect': '네트워크 연결',
  'network.disconnect': '네트워크 해제',
  'probe.failed': '응답 실패',
  'probe.down': '응답 중단',
  'probe.ok': '응답',
  'probe.recovered': '응답 복구',
  'resource.cpu': 'CPU 경고',
  'resource.memory': '메모리 경고',
  'resource.disk': '디스크 경고',
}

export function typeLabel(type: string): string {
  return TYPE_LABEL[type] ?? type
}

const SOURCE_LABEL: Record<string, string> = {
  docker: 'Docker',
  agent: '에이전트',
  central: '중앙 서버',
  'probe:internal': '내부 프로브',
  'probe:external': '외부 프로브',
}

export function sourceLabel(source: string): string {
  return source ? (SOURCE_LABEL[source] ?? source) : '—'
}

const EVIDENCE_LABEL: Record<string, string> = {
  'docker-event': 'Docker 이벤트',
  probe: '응답 확인',
  threshold: '임계값 판정',
  'agent-receive': '수신 기록',
}

export function evidenceLabel(evidence: string): string {
  return EVIDENCE_LABEL[evidence] ?? evidence
}

export const CATEGORY_LABEL: Record<string, string> = {
  ops: '운영',
  probe: '응답 확인',
  resource: '자원',
}
