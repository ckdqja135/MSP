import type { ServiceRole } from '../scene'

// 등각 아이콘은 직접 그린 2D SVG다. 색은 CSS 변수로 받아 상태에 따라 바뀐다.
// box는 바닥 중심과 크기로 직육면체의 세 면을 만든다.
function box(cx: number, floorY: number, halfWidth: number, height: number) {
  const dy = halfWidth * 0.577
  const top = floorY - height
  return {
    top: `M${cx} ${top - dy}L${cx + halfWidth} ${top}L${cx} ${top + dy}L${cx - halfWidth} ${top}Z`,
    left: `M${cx - halfWidth} ${top}L${cx} ${top + dy}L${cx} ${floorY + dy}L${cx - halfWidth} ${floorY}Z`,
    right: `M${cx + halfWidth} ${top}L${cx} ${top + dy}L${cx} ${floorY + dy}L${cx + halfWidth} ${floorY}Z`,
    topY: top,
    dy,
  }
}

function Box({ cx, floorY, halfWidth, height }: { cx: number; floorY: number; halfWidth: number; height: number }) {
  const shape = box(cx, floorY, halfWidth, height)
  return (
    <>
      <path className="iso-left" d={shape.left} />
      <path className="iso-right" d={shape.right} />
      <path className="iso-top" d={shape.top} />
    </>
  )
}

function Shadow({ cx, y, size }: { cx: number; y: number; size: number }) {
  return <path className="iso-shadow" d={`M${cx - size * 0.2} ${y}L${cx + size} ${y + size * 0.42}L${cx + size * 0.4} ${y + size * 0.78}L${cx - size * 0.8} ${y + size * 0.36}Z`} />
}

function AppIcon() {
  return (
    <>
      <Shadow cx={44} y={60} size={38} />
      <Box cx={40} floorY={58} halfWidth={26} height={34} />
      <path className="iso-line" d="M19 34l16 9M19 40l16 9M19 46l16 9" />
      <path className="iso-fill" d="M46 46l13-7.500v9L46 55z" />
    </>
  )
}

function WebIcon() {
  return (
    <>
      <Shadow cx={44} y={60} size={38} />
      <Box cx={40} floorY={58} halfWidth={26} height={34} />
      <path className="iso-fill" d="M18 34l18 10.400v5L18 39z" />
      <path className="iso-line" d="M18 46l18 10.400M18 52l12 7" />
      <path className="iso-line" d="M45 45l15-8.700M45 52l15-8.700M45 59l9-5.200" />
    </>
  )
}

function DatabaseIcon() {
  const disc = (y: number) => (
    <g key={y}>
      <path className="iso-right" d={`M12 ${y}v12a28 14 0 0 0 56 0v-12z`} />
      <ellipse className="iso-top" cx="40" cy={y} rx="28" ry="14" />
    </g>
  )
  return (
    <>
      <Shadow cx={44} y={62} size={36} />
      {disc(44)}
      {disc(31)}
      {disc(18)}
    </>
  )
}

function CacheIcon() {
  return (
    <>
      <Shadow cx={44} y={60} size={36} />
      <Box cx={40} floorY={58} halfWidth={27} height={10} />
      <Box cx={40} floorY={45} halfWidth={27} height={10} />
      <Box cx={40} floorY={32} halfWidth={27} height={10} />
      <path className="iso-mark-solid" d="M44 9l-9 11h6l-4 8 10-12h-6z" />
    </>
  )
}

function ProxyIcon() {
  return (
    <>
      <Shadow cx={46} y={60} size={40} />
      <Box cx={40} floorY={56} halfWidth={31} height={16} />
      <path className="iso-line" d="M24 29l16 9 16-9M32 24l8 5 8-5" />
      <path className="iso-line" d="M15 47l5 3M23 51.600l5 3M31 56.200l5 3" />
      <path className="iso-fill" d="M46 58l18-10.400v5L46 63z" />
    </>
  )
}

function StorageIcon() {
  return (
    <>
      <Shadow cx={44} y={60} size={38} />
      <Box cx={40} floorY={58} halfWidth={27} height={30} />
      <path className="iso-line" d="M13 38l27 15.600 27-15.600M13 48l27 15.600 27-15.600" />
    </>
  )
}

const ROLE_ICON: Record<ServiceRole, () => React.JSX.Element> = {
  app: AppIcon,
  web: WebIcon,
  database: DatabaseIcon,
  cache: CacheIcon,
  proxy: ProxyIcon,
  storage: StorageIcon,
}

export function ServiceIcon({ role }: { role: ServiceRole }) {
  const Icon = ROLE_ICON[role]
  return (
    <svg className="iso-device" viewBox="0 0 90 90" width="92" height="92" aria-hidden="true">
      <Icon />
    </svg>
  )
}

export function StackIcon() {
  return (
    <svg className="iso-device" viewBox="0 0 96 96" width="98" height="98" aria-hidden="true">
      <Shadow cx={50} y={68} size={40} />
      <Box cx={30} floorY={52} halfWidth={18} height={20} />
      <Box cx={66} floorY={52} halfWidth={18} height={20} />
      <Box cx={48} floorY={68} halfWidth={18} height={20} />
      <path className="iso-line" d="M37 58l9 5M37 63l9 5" />
    </svg>
  )
}

export function HostIcon() {
  return (
    <svg className="iso-device" viewBox="0 0 120 130" width="112" height="122" aria-hidden="true">
      <Shadow cx={64} y={94} size={58} />
      <Box cx={56} floorY={88} halfWidth={42} height={64} />
      <path className="iso-fill" d="M42 18l14 8 14-8-14-8z" />
      {[0, 1, 2].map((row) => (
        <g key={row}>
          <path className="iso-rack" d={`M20 ${44 + row * 18}l30 17.300v12l-30-17.300z`} />
          <path className="iso-line" d={`M25 ${50 + row * 18}l6 3.500M35 ${56 + row * 18}l10 5.800`} />
        </g>
      ))}
      <path className="iso-line" d="M63 62l26-15v14l-13 7.500v12l-13 7.500z" />
      <circle className="iso-led" cx="82" cy="72" r="2.200" />
    </svg>
  )
}

export function SwitchIcon() {
  return (
    <svg className="iso-device" viewBox="0 0 90 70" width="84" height="65" aria-hidden="true">
      <Shadow cx={46} y={44} size={32} />
      <Box cx={42} floorY={40} halfWidth={32} height={11} />
      <path className="iso-fill" d="M31 29l11 6.400 11-6.400-11-6.400z" />
      {[0, 1, 2, 3].map((port) => (
        <rect key={port} className="iso-port" x={14 + port * 6.500} y={33 + port * 3.800} width="4" height="4" />
      ))}
    </svg>
  )
}

export function CloudIcon() {
  return (
    <svg className="iso-device iso-cloud" viewBox="0 0 120 84" width="116" height="82" aria-hidden="true">
      <path
        className="iso-top"
        d="M28 72a19 19 0 0 1-3-37.700 25 25 0 0 1 46-11.800 19 19 0 0 1 27 16.500 17 17 0 0 1-6 33z"
      />
      <circle className="iso-globe" cx="60" cy="47" r="15" />
      <path className="iso-globe" d="M45 47h30M60 32c-11 10-11 20 0 30M60 32c11 10 11 20 0 30M48 38h24M48 56h24" />
    </svg>
  )
}
