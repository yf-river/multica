const sheetMarkup="<svg xmlns=\"http://www.w3.org/2000/svg\" viewBox=\"0 0 1024 1024\">\n  <path d=\"M899.984 19.873h-3.452c-26.123 0-47.296 21.172-47.296 47.296v888.508c0 26.127 21.173 47.298 47.296 47.298h3.452c26.119 0 47.297-21.171 47.297-47.298V67.169c0-26.124-21.177-47.296-47.297-47.296z\" fill=\"#4A5699\"/>\n  <path d=\"M132.643 19.873h-3.449c-26.12 0-47.296 21.172-47.296 47.296v888.508c0 26.127 21.177 47.298 47.296 47.298h3.449c26.123 0 47.299-21.171 47.299-47.298V67.169c0-26.124-21.176-47.296-47.299-47.296z\" fill=\"#C45FA0\"/>\n  <path d=\"M899.463 19.873H129.194c-26.12 0-47.296 21.172-47.296 47.296v3.377c0 26.12 21.177 47.299 47.296 47.299h770.269c26.123 0 47.296-21.179 47.296-47.299v-3.377c0-26.124-21.173-47.296-47.296-47.296z\" fill=\"#6277BA\"/>\n  <path d=\"M899.463 905.006H129.194c-26.12 0-47.296 21.17-47.296 47.29v3.381c0 26.127 21.177 47.298 47.296 47.298h770.269c26.123 0 47.296-21.171 47.296-47.298v-3.381c0-26.12-21.173-47.29-47.296-47.29z\" fill=\"#C45FA0\"/>\n  <path d=\"M717.962 543.153H542.047c-26.121 0-47.298 21.175-47.298 47.297v3.724c0 26.123 21.177 47.293 47.298 47.293h175.915c26.121 0 47.297-21.17 47.297-47.293v-3.724c0-26.122-21.176-47.297-47.297-47.297z\" fill=\"#E5594F\"/>\n  <path d=\"M689.268 198.849H513.355c-26.122 0-47.298 21.175-47.298 47.297v3.722c0 26.12 21.176 47.297 47.298 47.297h175.912c26.122 0 47.298-21.177 47.298-47.297v-3.722c0-26.122-21.175-47.297-47.297-47.297z\" fill=\"#F0D043\"/>\n  <path d=\"M757.789 353.081H261.17c-26.121 0-47.297 21.172-47.297 47.296v3.377c0 26.121 21.177 47.299 47.297 47.299h496.619c26.121 0 47.296-21.178 47.296-47.299v-3.377c0-26.125-21.175-47.296-47.296-47.296z\" fill=\"#E5594F\"/>\n  <path d=\"M762.638 726.225h-496.62c-26.12 0-47.294 21.18-47.294 47.301v3.377c0 26.12 21.174 47.3 47.294 47.3h496.62c26.122 0 47.296-21.18 47.296-47.3v-3.377c0-26.122-21.174-47.301-47.296-47.301z\" fill=\"#6277BA\"/>\n  <path d=\"M355.734 543.328H281.41c-26.122 0-47.297 21.17-47.297 47.293v3.378c0 26.118 21.175 47.297 47.297 47.297h74.324c26.123 0 47.296-21.179 47.296-47.297v-3.378c0-26.123-21.174-47.293-47.296-47.293z\" fill=\"#F39A2B\"/>\n  <path d=\"M334.85 248.006m-48.986 0a48.986 48.986 0 1 0 97.972 0 48.986 48.986 0 1 0-97.972 0Z\" fill=\"#F39A2B\"/>\n</svg>\n"
import { useId, type ReactElement } from 'react'
import type { Translate } from './i18n'

const SHEET_MARKUP = sheetMarkup
  .slice(sheetMarkup.indexOf('>') + 1, sheetMarkup.lastIndexOf('</svg>'))
  .trim()
  .replace(/>\s+</g, '><')

const SHEET_OUTLINE = SHEET_MARKUP.replace(/fill="#[0-9A-Fa-f]{6}"/g, '')

const HARNESS_STROKE = 1024 / 16

const SHEET_STROKE = 98

const INSET = (SHEET_STROKE - HARNESS_STROKE) / 2

const CANVAS = { x: -2 * INSET, y: -2 * INSET, width: 1024 + 4 * INSET, height: 1024 + 4 * INSET }
const CANVAS_ATTRS = `x="${CANVAS.x}" y="${CANVAS.y}" width="${CANVAS.width}" height="${CANVAS.height}"`

const canvasRect = (fill: string, rest = ''): string => `<rect ${CANVAS_ATTRS} fill="${fill}"${rest}/>`

const SHIFTS: [number, number][] = [[INSET, 0], [-INSET, 0], [0, INSET], [0, -INSET]]

function monoMarkup(id: string): string {
  const hole = `${id}-hole`
  const thin = `${id}-thin`
  const shifts = SHIFTS
    .map(([dx, dy]) => canvasRect('#000', ` mask="url(#${hole})" transform="translate(${dx} ${dy})"`))
    .join('')
  return '<defs>'
    + `<mask id="${hole}" maskUnits="userSpaceOnUse" ${CANVAS_ATTRS}>`
    + `${canvasRect('#fff')}<g fill="#000">${SHEET_OUTLINE}</g>`
    + '</mask>'
    + `<mask id="${thin}" maskUnits="userSpaceOnUse" ${CANVAS_ATTRS}>${canvasRect('#fff')}${shifts}</mask>`
    + '</defs>'
    + canvasRect('currentColor', ` mask="url(#${thin})"`)
}

export interface ContextIconProps {
  size?: number
  className?: string
  mono?: boolean
}

export function ContextIcon({ size = 20, className, mono = false }: ContextIconProps): ReactElement {
  const id = `dsh-context-sheet-${useId().replaceAll(':', '')}`
  return (
    <svg
      width={size}
      height={size}
      viewBox="0 0 1024 1024"
      className={className}
      aria-hidden="true"
      xmlns="http://www.w3.org/2000/svg"
      dangerouslySetInnerHTML={{ __html: mono ? monoMarkup(id) : SHEET_MARKUP }}
    />
  )
}

export function InsightPanelIcon({ size = 18 }: { size?: number }): ReactElement {
  return <ContextIcon size={size} mono />
}

export function makeContextTabTitle(t: Translate): () => ReactElement {
  return function ContextTabTitle(): ReactElement {
    return (
      <>
        <ContextIcon size={16} className="lc-title-icon" />
        <span className="lc-title-label">{t('tab.context')}</span>
      </>
    )
  }
}
