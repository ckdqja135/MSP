import { useCallback, useEffect, useRef, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { fetchConsole, fetchScreen, screenFrameURL, sendScreenInput, type ScreenView, type Server } from '../api'
import { formatAge } from '../format'
import { captureInput } from '../remoteInput'
import { MonitorIcon } from './Icons'

// 화면 보기 창이 열려 있는 동안만 조회한다. 조회가 끊기면 서버의 도우미가 화면 읽기를 멈춘다.
const SCREEN_POLL_MS = 1000
// 조작하는 동안에는 누른 결과가 바로 보이도록 더 자주 조회한다.
const CONTROL_POLL_MS = 250
const CONSOLE_POLL_MS = 2000
// 이 시간 넘게 새 화면이 없으면 멈춘 화면이라고 밝힌다.
const STALE_AFTER_MS = 5000
const TTYS = [1, 2, 3, 4, 5, 6]

const SESSION_LABEL: Record<string, string> = { x11: 'X11', wayland: 'Wayland' }

function useTick(intervalMs: number): number {
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    const timer = setInterval(() => setNow(Date.now()), intervalMs)
    return () => clearInterval(timer)
  }, [intervalMs])
  return now
}

function Notice({ title, children }: { title: string; children?: React.ReactNode }) {
  return (
    <div className="screen-notice">
      <strong>{title}</strong>
      {children && <p>{children}</p>}
    </div>
  )
}

// DesktopView는 서버 데스크톱 화면을 보여준다. active면 그림 위의 마우스와 키보드 입력을 서버로 보낸다.
function DesktopView({ server, view, failed, active }: { server: string; view?: ScreenView; failed: boolean; active: boolean }) {
  const now = useTick(1000)
  const [image, setImage] = useState<HTMLImageElement | null>(null)
  useEffect(() => {
    if (!active || !image) return
    return captureInput(image, (inputs) => sendScreenInput(server, inputs))
  }, [active, image, server])

  if (failed) return <Notice title="중앙 서버에서 화면 상태를 받지 못했습니다." />
  if (!view) return <Notice title="불러오는 중입니다." />
  if (view.helper !== 'online') {
    return (
      <Notice title="화면 도우미가 연결되어 있지 않습니다.">
        서버 데스크톱에 사용자가 로그인해 있어야 하고, 설치할 때 --screen-user로 그 사용자를 지정해야 합니다.
        서버에서 systemctl --user status msp-screen.service 로 확인할 수 있습니다.
      </Notice>
    )
  }
  if (view.state !== 'ready') return <Notice title="서버 화면을 가져올 수 없습니다.">{view.detail}</Notice>
  if (view.seq === 0 || !view.captured_at) return <Notice title="화면을 가져오는 중입니다." />

  const stale = now - Date.parse(view.captured_at) > STALE_AFTER_MS
  return (
    <>
      <div className="screen-stage">
        <img
          ref={setImage}
          className={`screen-image ${active ? 'controlling' : ''}`}
          src={screenFrameURL(server, view.seq)}
          alt="서버 데스크톱 화면"
          draggable={false}
        />
      </div>
      <footer className="screen-foot">
        <span className="mono">{SESSION_LABEL[view.session] ?? view.session} · {view.width}×{view.height}</span>
        <span className={stale ? 'screen-stale' : ''}>
          {formatAge(view.captured_at, now)} 화면{stale ? ' · 새 화면이 오지 않고 있습니다' : ''}
        </span>
        <span className="muted">
          {active ? '서버의 마우스 커서는 그려지지 않습니다. 이 창의 커서 자리가 서버의 커서 자리입니다.' : '마우스 커서는 화면에 나오지 않습니다.'}
        </span>
      </footer>
    </>
  )
}

function ConsoleView({ server }: { server: string }) {
  const [tty, setTTY] = useState(0)
  const result = useQuery({
    queryKey: ['console', server, tty],
    queryFn: () => fetchConsole(server, tty),
    refetchInterval: CONSOLE_POLL_MS,
    gcTime: 0,
  })
  const screen = result.data
  const lines = screen?.lines ?? []
  const blank = lines.every((line) => line.trim() === '')
  return (
    <>
      <div className="screen-bar">
        <label>
          터미널{' '}
          <select value={tty} onChange={(e) => setTTY(Number(e.target.value))}>
            <option value={0}>지금 켜진 터미널</option>
            {TTYS.map((number) => (
              <option key={number} value={number}>tty{number}</option>
            ))}
          </select>
        </label>
        {screen && !screen.error && (
          <span className="mono">
            tty{screen.tty} · {screen.cols}×{screen.rows}
            {screen.active > 0 && ` · 서버 모니터에 나오는 터미널 tty${screen.active}`}
          </span>
        )}
      </div>
      {result.isError && <Notice title="중앙 서버에서 콘솔 화면을 받지 못했습니다." />}
      {!result.isError && !screen && <Notice title="불러오는 중입니다." />}
      {screen?.error && <Notice title="콘솔 화면을 읽지 못했습니다.">{screen.error}</Notice>}
      {screen && !screen.error && (
        <div className="screen-stage">
          {blank ? (
            <Notice title="이 터미널의 글자 화면은 비어 있습니다.">
              데스크톱이 떠 있는 터미널은 글자 화면이 비어 있습니다. 데스크톱 탭에서 화면을 볼 수 있습니다.
            </Notice>
          ) : (
            <pre className="console-screen">{lines.join('\n')}</pre>
          )}
        </div>
      )}
    </>
  )
}

function ScreenViewer({ server, onClose }: { server: string; onClose: () => void }) {
  const [tab, setTab] = useState<'desktop' | 'console'>('desktop')
  // wanted는 조작을 켜 달라는 요청이다. 서버가 허용하고 입력 통로가 열려야 실제로 조작된다.
  // 창을 열면 켠 채로 시작한다. 조작을 허용하지 않은 서버에서는 아래에서 바로 내려 보기 전용이 된다.
  const [wanted, setWanted] = useState(true)
  const desktop = tab === 'desktop'
  const screen = useQuery({
    queryKey: ['screen', server, wanted],
    queryFn: () => fetchScreen(server, wanted ? '1' : undefined),
    enabled: desktop,
    refetchInterval: wanted ? CONTROL_POLL_MS : SCREEN_POLL_MS,
    gcTime: 0,
    placeholderData: (previous) => previous,
  })
  const view = desktop ? screen.data : undefined
  const allowed = Boolean(view?.control && view.helper === 'online' && view.state === 'ready')
  const controlling = desktop && wanted && allowed
  const active = controlling && Boolean(view?.control_live)

  // 조작을 끄면 조회가 끊기기를 기다리지 않고 서버에 바로 알린다.
  const stopControl = useCallback(() => {
    setWanted(false)
    void fetchScreen(server, '0').catch(() => undefined)
  }, [server])
  const wantedNow = useRef(wanted)
  wantedNow.current = wanted
  useEffect(() => () => {
    if (wantedNow.current) void fetchScreen(server, '0').catch(() => undefined)
  }, [server])
  // 서버가 조작을 더 허용하지 않거나 화면을 가져올 수 없게 되면 켜 둔 요청도 내린다.
  useEffect(() => {
    if (wanted && view && !allowed) setWanted(false)
  }, [wanted, view, allowed])

  // 조작 중에는 Esc도 서버로 보낸다. 창은 조작을 끈 뒤에 닫는다.
  useEffect(() => {
    if (controlling) return
    const onKey = (event: KeyboardEvent) => {
      if (event.key === 'Escape') onClose()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [controlling, onClose])

  // 데스크톱 탭을 벗어나면 조작을 끄고, 돌아오면 처음처럼 켠 채로 시작한다.
  const pickTab = (next: 'desktop' | 'console') => {
    if (next === tab) return
    if (next === 'desktop') setWanted(true)
    else if (wanted) stopControl()
    setTab(next)
  }
  return (
    <div className="screen-overlay" role="dialog" aria-modal="true" aria-label={`${server} 화면`} onClick={controlling ? undefined : onClose}>
      <div className="screen-window" onClick={(event) => event.stopPropagation()}>
        <header className="screen-head">
          <strong>{server} 지금 화면</strong>
          <div className="tabs">
            <button type="button" className={desktop ? 'active' : ''} onClick={() => pickTab('desktop')}>데스크톱</button>
            <button type="button" className={!desktop ? 'active' : ''} onClick={() => pickTab('console')}>텍스트 콘솔</button>
          </div>
          <span className="spacer" />
          {controlling ? (
            <span className="screen-controlling small" role="status">
              {active ? '조작 중입니다. 마우스와 키보드 입력이 서버로 갑니다.' : '조작 연결을 준비하고 있습니다.'}
            </span>
          ) : (
            <span className="muted small">보기 전용입니다. 키보드와 마우스 입력은 서버로 가지 않습니다.</span>
          )}
          {desktop && (
            <button
              type="button"
              className={`tool-button ${controlling ? 'control-on' : ''}`}
              disabled={!allowed}
              title={allowed ? '켠 동안 이 창의 마우스와 키보드 입력이 서버로 전달됩니다' : '이 서버는 보기 전용으로 설치되어 있습니다. 조작하려면 install.sh에 --screen-control을 붙여 다시 설치합니다'}
              onClick={(event) => {
                event.currentTarget.blur()
                if (controlling) stopControl()
                else setWanted(true)
              }}
            >
              {controlling ? '조작 끄기' : '조작 켜기'}
            </button>
          )}
          <button type="button" className="tool-button" onClick={onClose}>닫기</button>
        </header>
        {desktop ? <DesktopView server={server} view={view} failed={screen.isError} active={active} /> : <ConsoleView server={server} />}
      </div>
    </div>
  )
}

// ScreenButton은 고른 서버의 지금 화면을 여는 버튼이다. 데모에는 볼 서버 화면이 없다.
export function ScreenButton({ server, demo }: { server?: Server; demo: boolean }) {
  const [open, setOpen] = useState(false)
  const close = () => setOpen(false)
  return (
    <>
      <button
        type="button"
        className="screen-button"
        disabled={!server || demo}
        title={demo ? '데모에는 볼 서버 화면이 없습니다' : '서버의 지금 화면을 봅니다'}
        onClick={() => setOpen(true)}
      >
        <MonitorIcon />
        화면보기
      </button>
      {open && server && <ScreenViewer server={server.id} onClose={close} />}
    </>
  )
}
