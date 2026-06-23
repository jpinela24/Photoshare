import { useState, useEffect, useCallback, useRef, createContext, useContext, useMemo } from 'react'
import ReactDOM from 'react-dom'
import { useVirtualizer } from '@tanstack/react-virtual'
import L from 'leaflet'
import 'leaflet/dist/leaflet.css'
import './App.css'

// ── Icons ────────────────────────────────────────────────────────────────────

const FolderIcon = ({ size = 40 }) => (
  <svg viewBox="0 0 24 24" fill="currentColor" width={size} height={size}>
    <path d="M10 4H4c-1.1 0-2 .9-2 2v12c0 1.1.9 2 2 2h16c1.1 0 2-.9 2-2V8c0-1.1-.9-2-2-2h-8l-2-2z" />
  </svg>
)

const VideoIcon = () => (
  <svg viewBox="0 0 24 24" fill="currentColor" width="44" height="44">
    <path d="M17 10.5V7a1 1 0 0 0-1-1H4a1 1 0 0 0-1 1v10a1 1 0 0 0 1 1h12a1 1 0 0 0 1-1v-3.5l4 4v-11l-4 4z" />
  </svg>
)

const PlayBadge = () => (
  <svg viewBox="0 0 24 24" fill="currentColor" width="26" height="26">
    <circle cx="12" cy="12" r="12" fill="rgba(0,0,0,0.55)" />
    <polygon points="10,8 18,12 10,16" fill="white" />
  </svg>
)

const ChevronRight = () => (
  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.5" width="13" height="13">
    <polyline points="9 18 15 12 9 6" />
  </svg>
)

const ChevronDown = () => (
  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.5" width="13" height="13">
    <polyline points="6 9 12 15 18 9" />
  </svg>
)

const MenuIcon = () => (
  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" width="20" height="20">
    <line x1="3" y1="6" x2="21" y2="6" />
    <line x1="3" y1="12" x2="21" y2="12" />
    <line x1="3" y1="18" x2="21" y2="18" />
  </svg>
)

const HomeIcon = () => (
  <svg viewBox="0 0 24 24" fill="currentColor" width="15" height="15">
    <path d="M10 20v-6h4v6h5v-8h3L12 3 2 12h3v8z" />
  </svg>
)

const PauseIcon = () => (
  <svg viewBox="0 0 24 24" fill="white" width="22" height="22">
    <rect x="6" y="4" width="4" height="16" rx="1" />
    <rect x="14" y="4" width="4" height="16" rx="1" />
  </svg>
)

// Minimalist stroke-icon set (replaces chunky emoji throughout the UI).
const Svg = ({ size = 15, sw = 2, children }) => (
  <svg viewBox="0 0 24 24" width={size} height={size} fill="none" stroke="currentColor"
    strokeWidth={sw} strokeLinecap="round" strokeLinejoin="round"
    style={{ display: 'inline-block', verticalAlign: '-0.125em', flexShrink: 0 }}>
    {children}
  </svg>
)
const TrashIcon       = (p) => <Svg {...p}><polyline points="3 6 5 6 21 6"/><path d="M19 6l-1 14a2 2 0 0 1-2 2H8a2 2 0 0 1-2-2L5 6"/><path d="M10 11v6M14 11v6"/><path d="M9 6V4a1 1 0 0 1 1-1h4a1 1 0 0 1 1 1v2"/></Svg>
const FolderPlusIcon  = (p) => <Svg {...p}><path d="M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z"/><line x1="12" y1="11" x2="12" y2="15"/><line x1="10" y1="13" x2="14" y2="13"/></Svg>
const PlusIcon        = (p) => <Svg {...p}><line x1="12" y1="5" x2="12" y2="19"/><line x1="5" y1="12" x2="19" y2="12"/></Svg>
const PencilIcon      = (p) => <Svg {...p}><path d="M12 20h9"/><path d="M16.5 3.5a2.12 2.12 0 0 1 3 3L7 19l-4 1 1-4z"/></Svg>
const StatsIcon       = (p) => <Svg {...p}><line x1="6" y1="20" x2="6" y2="12"/><line x1="12" y1="20" x2="12" y2="4"/><line x1="18" y1="20" x2="18" y2="14"/></Svg>
const GearIcon        = (p) => <Svg {...p}><circle cx="12" cy="12" r="3"/><path d="M19.4 15a1.65 1.65 0 0 0 .33 1.82l.06.06a2 2 0 1 1-2.83 2.83l-.06-.06a1.65 1.65 0 0 0-1.82-.33 1.65 1.65 0 0 0-1 1.51V21a2 2 0 0 1-4 0v-.09A1.65 1.65 0 0 0 9 19.4a1.65 1.65 0 0 0-1.82.33l-.06.06a2 2 0 1 1-2.83-2.83l.06-.06a1.65 1.65 0 0 0 .33-1.82 1.65 1.65 0 0 0-1.51-1H3a2 2 0 0 1 0-4h.09A1.65 1.65 0 0 0 4.6 9a1.65 1.65 0 0 0-.33-1.82l-.06-.06a2 2 0 1 1 2.83-2.83l.06.06a1.65 1.65 0 0 0 1.82.33H9a1.65 1.65 0 0 0 1-1.51V3a2 2 0 0 1 4 0v.09a1.65 1.65 0 0 0 1 1.51 1.65 1.65 0 0 0 1.82-.33l.06-.06a2 2 0 1 1 2.83 2.83l-.06.06a1.65 1.65 0 0 0-.33 1.82V9a1.65 1.65 0 0 0 1.51 1H21a2 2 0 0 1 0 4h-.09a1.65 1.65 0 0 0-1.51 1z"/></Svg>
const UploadIcon      = (p) => <Svg {...p}><path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4"/><polyline points="17 8 12 3 7 8"/><line x1="12" y1="3" x2="12" y2="15"/></Svg>
const DownloadIcon    = (p) => <Svg {...p}><path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4"/><polyline points="7 10 12 15 17 10"/><line x1="12" y1="3" x2="12" y2="15"/></Svg>
const OpenFolderIcon  = (p) => <Svg {...p}><path d="M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v1H3z"/><path d="M3 10h18l-2 8a2 2 0 0 1-2 1.6H7A2 2 0 0 1 5 18z"/></Svg>
const PinIcon         = (p) => <Svg {...p}><line x1="12" y1="17" x2="12" y2="22"/><path d="M9 3h6l-1 6 3 3v2H7v-2l3-3z"/></Svg>
const RefreshIcon     = (p) => <Svg {...p}><polyline points="23 4 23 10 17 10"/><path d="M20.49 15a9 9 0 1 1-2.12-9.36L23 10"/></Svg>
const LockIcon        = (p) => <Svg {...p}><rect x="3" y="11" width="18" height="11" rx="2"/><path d="M7 11V7a5 5 0 0 1 10 0v4"/></Svg>
const UnlockIcon      = (p) => <Svg {...p}><rect x="3" y="11" width="18" height="11" rx="2"/><path d="M7 11V7a5 5 0 0 1 9.9-1"/></Svg>
const CalendarIcon    = (p) => <Svg {...p}><rect x="3" y="4" width="18" height="18" rx="2"/><line x1="16" y1="2" x2="16" y2="6"/><line x1="8" y1="2" x2="8" y2="6"/><line x1="3" y1="10" x2="21" y2="10"/></Svg>
const SaveIcon        = (p) => <Svg {...p}><path d="M19 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h11l5 5v11a2 2 0 0 1-2 2z"/><polyline points="17 21 17 13 7 13 7 21"/><polyline points="7 3 7 8 15 8"/></Svg>
const CheckIcon       = (p) => <Svg {...p}><polyline points="20 6 9 17 4 12"/></Svg>
const CloseIcon       = (p) => <Svg {...p}><line x1="18" y1="6" x2="6" y2="18"/><line x1="6" y1="6" x2="18" y2="18"/></Svg>
const WarnIcon        = (p) => <Svg {...p}><path d="M10.29 3.86 1.82 18a2 2 0 0 0 1.71 3h16.94a2 2 0 0 0 1.71-3L13.71 3.86a2 2 0 0 0-3.42 0z"/><line x1="12" y1="9" x2="12" y2="13"/><line x1="12" y1="17" x2="12.01" y2="17"/></Svg>
const CopyIcon        = (p) => <Svg {...p}><rect x="9" y="9" width="13" height="13" rx="2"/><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/></Svg>
const ScissorsIcon    = (p) => <Svg {...p}><circle cx="6" cy="6" r="3"/><circle cx="6" cy="18" r="3"/><line x1="20" y1="4" x2="8.12" y2="15.88"/><line x1="14.47" y1="14.48" x2="20" y2="20"/><line x1="8.12" y1="8.12" x2="12" y2="12"/></Svg>
const RestoreIcon     = (p) => <Svg {...p}><polyline points="1 4 1 10 7 10"/><path d="M3.51 15a9 9 0 1 0 2.13-9.36L1 10"/></Svg>
const FilmIcon        = (p) => <Svg {...p}><rect x="2" y="2" width="20" height="20" rx="2"/><line x1="7" y1="2" x2="7" y2="22"/><line x1="17" y1="2" x2="17" y2="22"/><line x1="2" y1="12" x2="22" y2="12"/><line x1="2" y1="7" x2="7" y2="7"/><line x1="2" y1="17" x2="7" y2="17"/><line x1="17" y1="17" x2="22" y2="17"/><line x1="17" y1="7" x2="22" y2="7"/></Svg>
const GlobeIcon       = (p) => <Svg {...p}><circle cx="12" cy="12" r="10"/><line x1="2" y1="12" x2="22" y2="12"/><path d="M12 2a15.3 15.3 0 0 1 4 10 15.3 15.3 0 0 1-4 10 15.3 15.3 0 0 1-4-10 15.3 15.3 0 0 1 4-10z"/></Svg>
const PlugIcon        = (p) => <Svg {...p}><path d="M9 2v6M15 2v6"/><path d="M6 8h12v3a6 6 0 0 1-12 0z"/><line x1="12" y1="17" x2="12" y2="22"/></Svg>
const LibraryIcon     = (p) => <Svg {...p}><rect x="3" y="3" width="7" height="7" rx="1"/><rect x="14" y="3" width="7" height="7" rx="1"/><rect x="3" y="14" width="7" height="7" rx="1"/><rect x="14" y="14" width="7" height="7" rx="1"/></Svg>
const PlayIcon        = ({ size = 15 }) => <svg viewBox="0 0 24 24" width={size} height={size} fill="currentColor"><polygon points="6 4 20 12 6 20"/></svg>
const ArrowUpIcon     = (p) => <Svg {...p}><line x1="12" y1="19" x2="12" y2="5"/><polyline points="5 12 12 5 19 12"/></Svg>
const ArrowDownIcon   = (p) => <Svg {...p}><line x1="12" y1="5" x2="12" y2="19"/><polyline points="19 12 12 19 5 12"/></Svg>
const GridSmallIcon   = (p) => <Svg {...p}><rect x="3" y="3" width="7" height="7"/><rect x="14" y="3" width="7" height="7"/><rect x="3" y="14" width="7" height="7"/><rect x="14" y="14" width="7" height="7"/></Svg>
const GridLargeIcon   = (p) => <Svg {...p}><rect x="4" y="4" width="16" height="16" rx="1"/></Svg>
const ClockIcon       = (p) => <Svg {...p}><circle cx="12" cy="12" r="9"/><polyline points="12 7 12 12 15 14"/></Svg>
const CameraIcon      = (p) => <Svg {...p}><path d="M23 19a2 2 0 0 1-2 2H3a2 2 0 0 1-2-2V8a2 2 0 0 1 2-2h4l2-3h6l2 3h4a2 2 0 0 1 2 2z"/><circle cx="12" cy="13" r="4"/></Svg>
const ImageIcon       = (p) => <Svg {...p}><rect x="3" y="3" width="18" height="18" rx="2"/><circle cx="8.5" cy="8.5" r="1.5"/><polyline points="21 15 16 10 5 21"/></Svg>
const HardDriveIcon   = (p) => <Svg {...p}><line x1="22" y1="12" x2="2" y2="12"/><path d="M5.45 5.11 2 12v6a2 2 0 0 0 2 2h16a2 2 0 0 0 2-2v-6l-3.45-6.89A2 2 0 0 0 16.76 4H7.24a2 2 0 0 0-1.79 1.11z"/><line x1="6" y1="16" x2="6.01" y2="16"/><line x1="10" y1="16" x2="10.01" y2="16"/></Svg>
const GridMediumIcon  = (p) => <Svg {...p}><rect x="3" y="4" width="8" height="16"/><rect x="13" y="4" width="8" height="16"/></Svg>
const SquareIcon      = (p) => <Svg {...p}><rect x="4" y="4" width="16" height="16" rx="2"/></Svg>
const RotateCcwIcon   = (p) => <Svg {...p}><polyline points="1 4 1 10 7 10"/><path d="M3.51 15a9 9 0 1 0 2.13-9.36L1 10"/></Svg>
const RotateCwIcon    = (p) => <Svg {...p}><polyline points="23 4 23 10 17 10"/><path d="M20.49 15a9 9 0 1 1-2.12-9.36L23 10"/></Svg>
const SunIcon         = (p) => <Svg {...p}><circle cx="12" cy="12" r="4"/><line x1="12" y1="2" x2="12" y2="4"/><line x1="12" y1="20" x2="12" y2="22"/><line x1="4.2" y1="4.2" x2="5.6" y2="5.6"/><line x1="18.4" y1="18.4" x2="19.8" y2="19.8"/><line x1="2" y1="12" x2="4" y2="12"/><line x1="20" y1="12" x2="22" y2="12"/><line x1="4.2" y1="19.8" x2="5.6" y2="18.4"/><line x1="18.4" y1="5.6" x2="19.8" y2="4.2"/></Svg>
const MoonIcon        = (p) => <Svg {...p}><path d="M21 12.79A9 9 0 1 1 11.21 3 7 7 0 0 0 21 12.79z"/></Svg>
const LogoutIcon      = (p) => <Svg {...p}><path d="M9 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h4"/><polyline points="16 17 21 12 16 7"/><line x1="21" y1="12" x2="9" y2="12"/></Svg>
const MonitorIcon     = (p) => <Svg {...p}><rect x="2" y="3" width="20" height="14" rx="2"/><line x1="8" y1="21" x2="16" y2="21"/><line x1="12" y1="17" x2="12" y2="21"/></Svg>
const QrIcon          = (p) => <Svg {...p}><rect x="3" y="3" width="7" height="7"/><rect x="14" y="3" width="7" height="7"/><rect x="3" y="14" width="7" height="7"/><line x1="14" y1="14" x2="14" y2="17"/><line x1="17" y1="14" x2="21" y2="14"/><line x1="21" y1="14" x2="21" y2="17"/><line x1="14" y1="21" x2="21" y2="21"/><line x1="17" y1="17" x2="17" y2="17"/></Svg>
const SparkleIcon     = (p) => <Svg {...p}><path d="M12 3l1.9 5.1L19 10l-5.1 1.9L12 17l-1.9-5.1L5 10l5.1-1.9z"/><path d="M19 15l.8 2.2L22 18l-2.2.8L19 21l-.8-2.2L16 18l2.2-.8z"/></Svg>
const MapPinIcon      = (p) => <Svg {...p}><path d="M21 10c0 7-9 13-9 13s-9-6-9-13a9 9 0 0 1 18 0z"/><circle cx="12" cy="10" r="3"/></Svg>

// ── VideoCard ─────────────────────────────────────────────────────────────────
// Single-click  → play/pause inline in the grid card
// Double-click  → open full-screen modal

// ── Folder action helpers ─────────────────────────────────────────────────────

// Parse drag data — always returns an array of {path, name}
function parseDragFiles(e) {
  try {
    const raw = e.dataTransfer.getData('application/photo-share')
    if (!raw) return []
    const data = JSON.parse(raw)
    // New format: { files: [...] }
    if (data.files) return data.files
    // Legacy format: { path, name }
    if (data.path) return [{ path: data.path, name: data.name }]
  } catch {}
  return []
}

// Move multiple files to a destination folder
async function batchMoveDrop(files, destFolder, token, onFileMoved) {
  const paths = files.map(f => f.path)
  const r = await adminFetch('/api/admin/batch/move', token, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ paths, destFolder }),
  })
  if (r.ok) paths.forEach(p => onFileMoved?.(p))
}

// Cookie-based now — the session cookie is sent automatically on same-origin
// requests, so the token arg is ignored (kept for call-site compatibility).
function adminFetch(url, _token, opts = {}) {
  return fetch(url, { credentials: 'same-origin', ...opts })
}

// ── playingPath is the path of the currently playing video (or null)
// setPlayingPath is shared across all cards so only one plays at a time
function VideoCard({ entry, onOpenModal, playingPath, setPlayingPath, focused, onFocus }) {
  const playing  = playingPath === entry.path
  const videoRef = useRef(null)
  const timerRef = useRef(null)
  const loadedRef = useRef(false) // load the video at most once per card
  const [scrub, setScrub] = useState(false)

  // Hover-scrub: reveal the video and seek it to follow the cursor's X position.
  const startScrub = () => {
    if (playing) return
    const v = videoRef.current
    if (v && !loadedRef.current) {
      v.muted = true; v.preload = 'metadata'
      try { v.load() } catch {}
      loadedRef.current = true
    }
    setScrub(true)
  }
  const scrubVideo = (e) => {
    const v = videoRef.current
    if (!v || playing) return
    const rect = e.currentTarget.getBoundingClientRect()
    const frac = Math.min(1, Math.max(0, (e.clientX - rect.left) / rect.width))
    if (v.duration && isFinite(v.duration)) v.currentTime = frac * v.duration
  }
  const endScrub = () => {
    if (!scrub) return
    setScrub(false)
    const v = videoRef.current
    if (v && !playing) { try { v.pause() } catch {} }
  }

  // Play or pause whenever `playing` changes
  useEffect(() => {
    if (!videoRef.current) return
    if (playing) {
      videoRef.current.play()
    } else {
      videoRef.current.pause()
    }
  }, [playing])

  // Stop + clean up on unmount
  useEffect(() => () => {
    clearTimeout(timerRef.current)
    videoRef.current?.pause()
  }, [])

  const handleClick = () => {
    onFocus?.()
    if (timerRef.current) {
      // Second click within 220 ms → double-click → open modal
      clearTimeout(timerRef.current)
      timerRef.current = null
      setPlayingPath(null)
      onOpenModal(entry)
      return
    }
    timerRef.current = setTimeout(() => {
      timerRef.current = null
      // Single click: start this video (stops any other via shared state)
      setPlayingPath(prev => prev === entry.path ? null : entry.path)
    }, 220)
  }

  const { token: adminTok }                          = useContext(AdminCtx)
  const { selectMode, selected: selSet, toggleSelect } = useContext(SelectCtx)
  const { onMouseEnter: metaEnter, onMouseLeave: metaLeave, onMouseMove: metaMove, tooltip: metaTip } = useMetaTooltip(entry.path)

  return (
    <div
      className={`card card-video ${selSet.has(entry.path) ? 'card-selected' : ''} ${focused ? 'card-grid-focus' : ''}`}
      data-sel-path={entry.path}
      onClick={selectMode ? (e) => toggleSelect(entry.path, e.shiftKey) : handleClick}
      onMouseEnter={metaEnter}
      onMouseLeave={metaLeave}
      onMouseMove={metaMove}
      title={`${entry.name}\nClick to play · Double-click for fullscreen`}
      style={{position:'relative'}}
      draggable={!!adminTok}
      onDragStart={e => {
        const isSelectedVideo = selSet.has(entry.path)
        const files = isSelectedVideo && selSet.size > 1
          ? [...selSet].map(p => ({ path: p, name: p.split('/').pop() }))
          : [{ path: entry.path, name: entry.name }]
        e.dataTransfer.setData('application/photo-share', JSON.stringify({ files }))
        e.dataTransfer.effectAllowed = 'move'
        e.currentTarget.classList.add('card-dragging')
        videoRef.current?.pause(); setPlayingPath(null)
      }}
      onDragEnd={e => e.currentTarget.classList.remove('card-dragging')}
    >
      {selectMode
        ? <div className="card-checkbox">{selSet.has(entry.path) ? '✓' : ''}</div>
        : <TrashBtn entry={entry} />
      }
      <div className="card-thumb video-thumb-wrap"
        onMouseEnter={selectMode ? undefined : startScrub}
        onMouseMove={selectMode ? undefined : scrubVideo}
        onMouseLeave={endScrub}>

        <img
          className={`card-thumb photo-thumb vid-poster ${playing || scrub ? 'vid-poster-hidden' : ''}`}
          src={`/api/thumb?path=${encodeURIComponent(entry.path)}`}
          alt={entry.name}
          loading="lazy"
        />

        <video
          ref={videoRef}
          className={`inline-video ${playing || scrub ? 'inline-video-visible' : ''}`}
          src={`/api/video?path=${encodeURIComponent(entry.path)}`}
          loop
          playsInline
          preload="none"
        />

        <div className={`vid-overlay ${playing ? 'vid-overlay-playing' : ''}`}>
          {playing
            ? <div className="vid-pause-btn"><PauseIcon /></div>
            : (!scrub && <div className="play-overlay"><PlayBadge /></div>)
          }
          {playing && <div className="vid-hint">double-click for fullscreen</div>}
        </div>

      </div>
      <div className="card-label">{entry.name}</div>
      {metaTip}
    </div>
  )
}

// ── Admin context ─────────────────────────────────────────────────────────────

const AdminCtx    = createContext({ token: null, onDeleteRequest: () => {} })
const SelectCtx   = createContext({ selectMode: false, selected: new Set(), toggleSelect: () => {} })

// ── Delete confirmation modal ─────────────────────────────────────────────────

function DeleteConfirm({ entry, onConfirm, onClose }) {
  const [busy, setBusy] = useState(false)

  const confirm = async () => {
    setBusy(true)
    await onConfirm()
    setBusy(false)
  }

  return ReactDOM.createPortal(
    <div className="adm-overlay" onClick={onClose}>
      <div className="adm-modal adm-confirm" onClick={e => e.stopPropagation()}>
        <div className="adm-modal-icon adm-danger-icon"><TrashIcon size={30} /></div>
        <h2 className="adm-modal-title">Delete {entry.isDir ? 'Folder' : 'File'}?</h2>
        <p className="adm-confirm-name">{entry.name}</p>
        {entry.isDir && (
          <p className="adm-warn">⚠ All contents inside will be permanently deleted.</p>
        )}
        <p className="adm-warn">This action cannot be undone.</p>
        <div className="adm-btns">
          <button className="adm-btn" onClick={onClose} disabled={busy}>Cancel</button>
          <button className="adm-btn adm-btn-danger" onClick={confirm} disabled={busy}>
            {busy ? 'Deleting…' : 'Delete'}
          </button>
        </div>
      </div>
    </div>,
    document.body
  )
}

// ── TrashBtn ──────────────────────────────────────────────────────────────────
// Small delete button shown on cards when admin mode is active

function TrashBtn({ entry }) {
  const { token, onDeleteRequest } = useContext(AdminCtx)
  if (!token) return null

  return (
    <button
      className="trash-btn"
      title="Delete"
      onClick={e => { e.stopPropagation(); onDeleteRequest(entry) }}
    >
      <TrashIcon size={15} />
    </button>
  )
}

// ── MetaTooltip ───────────────────────────────────────────────────────────────
// Wraps a grid card. On hover, fetches file metadata and shows an overlay.

function fmtBytes(b) {
  if (!b) return ''
  if (b < 1024 * 1024) return `${(b / 1024).toFixed(0)} KB`
  if (b < 1024 * 1024 * 1024) return `${(b / 1024 / 1024).toFixed(1)} MB`
  return `${(b / 1024 / 1024 / 1024).toFixed(2)} GB`
}

// Global tooltip rendered at root level via portal to avoid overflow clipping
function MetaTooltipPortal({ meta, x, y }) {
  if (!meta) return null
  return ReactDOM.createPortal(
    <div className="meta-tooltip" style={{ position:'fixed', left: x + 12, top: y + 18, zIndex: 9999, pointerEvents:'none' }}>
      {meta.width > 0 && meta.height > 0 && (
        <div className="meta-row"><span className="meta-icon"><ImageIcon size={13} /></span><span>{meta.width} × {meta.height}</span></div>
      )}
      {meta.duration && (
        <div className="meta-row"><span className="meta-icon"><ClockIcon size={13} /></span><span>{meta.duration}</span></div>
      )}
      {meta.taken && (
        <div className="meta-row"><span className="meta-icon"><CameraIcon size={13} /></span><span>{meta.taken}</span></div>
      )}
      <div className="meta-row"><span className="meta-icon"><HardDriveIcon size={13} /></span><span>{fmtBytes(meta.size)}</span></div>
      <div className="meta-row"><span className="meta-icon"><CalendarIcon size={13} /></span><span>{meta.modified}</span></div>
    </div>,
    document.body
  )
}

// useMetaTooltip — returns hover handlers + tooltip JSX to spread onto any card
function useMetaTooltip(path) {
  const [meta, setMeta]   = useState(null)
  const [pos, setPos]     = useState(null)
  const fetchedRef        = useRef(false)

  const onMouseMove  = (e) => setPos({ x: e.clientX, y: e.clientY })
  const onMouseEnter = (e) => {
    setPos({ x: e.clientX, y: e.clientY })
    if (fetchedRef.current) return
    fetchedRef.current = true
    fetch(`/api/meta?path=${encodeURIComponent(path)}`)
      .then(r => r.json()).then(setMeta).catch(() => {})
  }
  const onMouseLeave = () => setPos(null)

  const tooltip = pos && meta
    ? <MetaTooltipPortal meta={meta} x={pos.x} y={pos.y} />
    : null

  return { onMouseEnter, onMouseLeave, onMouseMove, tooltip }
}

// ── FolderThumb ───────────────────────────────────────────────────────────────
// Shows a 2×2 collage of the first photos inside a folder.
// Falls back to the plain icon if the folder has no previews yet.

function FolderThumb({ folderPath }) {
  const [info, setInfo] = useState(null)

  useEffect(() => {
    fetch(`/api/folder-info?path=${encodeURIComponent(folderPath)}`)
      .then(r => r.json())
      .then(setInfo)
      .catch(() => {})
  }, [folderPath])

  const previews = info?.previewImages ?? []

  if (previews.length === 0) {
    return (
      <div className="card-thumb folder-thumb">
        <FolderIcon size={52} />
      </div>
    )
  }

  // Pad to 4 slots by repeating what we have
  const slots = Array.from({ length: 4 }, (_, i) => previews[i % previews.length])

  return (
    <div className="card-thumb folder-collage">
      {slots.map((imgPath, i) => (
        <img
          key={i}
          className="collage-cell"
          src={`/api/thumb?path=${encodeURIComponent(imgPath)}`}
          alt=""
          loading="lazy"
        />
      ))}
      <div className="collage-overlay">
        <FolderIcon size={22} />
      </div>
    </div>
  )
}

// ── Helpers ───────────────────────────────────────────────────────────────────

function fmtSize(bytes) {
  if (!bytes) return ''
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(0)} KB`
  if (bytes < 1024 * 1024 * 1024) return `${(bytes / 1024 / 1024).toFixed(1)} MB`
  return `${(bytes / 1024 / 1024 / 1024).toFixed(2)} GB`
}

function metaLine(info) {
  if (!info) return null
  const parts = []
  if (info.photoCount > 0) parts.push(`${info.photoCount} photo${info.photoCount !== 1 ? 's' : ''}`)
  if (info.videoCount > 0) parts.push(`${info.videoCount} video${info.videoCount !== 1 ? 's' : ''}`)
  if (info.folderCount > 0) parts.push(`${info.folderCount} folder${info.folderCount !== 1 ? 's' : ''}`)
  return parts.join(' · ') || 'Empty'
}

// ── SidebarItem (recursive) ────────────────────────────────────────────────────

function SidebarItem({ entry, currentPath, onNavigate, depth, onRefresh, onFileMoved, onPin }) {
  const { token }               = useContext(AdminCtx)
  const [info, setInfo]         = useState(null)
  const [expanded, setExpanded] = useState(false)
  const [children, setChildren] = useState(null)
  const [renaming, setRenaming] = useState(false)
  const [renamVal, setRenamVal] = useState(entry.name)
  const [creating, setCreating] = useState(false)
  const [newFolderName, setNewFolderName] = useState('')
  const [folderError, setFolderError]             = useState(null)
  const [dragOver, setDragOver]                   = useState(false)
  const [folderPath, setFolderPath]   = useState(null)  // {fullPath, smbURL}
  const [openedToast, setOpenedToast] = useState(false)
  const renameRef   = useRef(null)
  const newFolderRef = useRef(null)

  const isCurrent  = currentPath === entry.path
  const isAncestor = currentPath.startsWith(entry.path + '/')

  useEffect(() => {
    fetch(`/api/folder-info?path=${encodeURIComponent(entry.path)}`)
      .then(r => r.json()).then(setInfo).catch(() => {})
  }, [entry.path])

  useEffect(() => {
    if (isAncestor && !expanded) setExpanded(true)
  }, [isAncestor])

  useEffect(() => {
    if (!expanded || children !== null) return
    fetch(`/api/browse?path=${encodeURIComponent(entry.path)}`)
      .then(r => r.json())
      .then(data => setChildren((data || []).filter(e => e.isDir)))
      .catch(() => setChildren([]))
  }, [expanded])

  useEffect(() => { if (renaming) renameRef.current?.select() }, [renaming])
  useEffect(() => { if (creating) newFolderRef.current?.focus() }, [creating])

  const hasSubfolders = info ? info.folderCount > 0 : (children?.length > 0)
  const isEmpty       = info ? (info.photoCount + info.videoCount + info.folderCount) === 0 : false

  // ── Rename ──
  const submitRename = async () => {
    const name = renamVal.trim()
    if (!name || name === entry.name) { setRenaming(false); return }
    setFolderError(null)
    const r = await adminFetch('/api/admin/folder/rename', token, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ path: entry.path, newName: name }),
    })
    if (!r.ok) { setFolderError(await r.text()); return }
    setRenaming(false)
    onRefresh()
  }

  // ── Create subfolder ──
  const submitCreate = async () => {
    const name = newFolderName.trim()
    if (!name) { setCreating(false); return }
    setFolderError(null)
    const r = await adminFetch(
      `/api/admin/folder/create?path=${encodeURIComponent(entry.path)}&name=${encodeURIComponent(name)}`,
      token, { method: 'POST' }
    )
    if (!r.ok) { setFolderError(await r.text()); return }
    setCreating(false)
    setNewFolderName('')
    setExpanded(true)
    setChildren(null) // re-fetch children
    onRefresh()
  }

  // ── Button click handlers (these buttons only render for admins — see below) ──
  const onRenameClick = () => { setRenamVal(entry.name); setRenaming(true) }
  const onCreateClick = () => { setExpanded(true); setCreating(true) }

  // ── Drop target ──
  const handleDragOver = (e) => {
    if (!token) return
    const has = e.dataTransfer.types.includes('application/photo-share')
    if (!has) return
    e.preventDefault()
    e.dataTransfer.dropEffect = 'move'
    setDragOver(true)
  }
  const handleDragLeave = (e) => {
    if (!e.currentTarget.contains(e.relatedTarget)) setDragOver(false)
  }
  const handleDrop = async (e) => {
    e.preventDefault()
    setDragOver(false)
    if (!token) return
    const files = parseDragFiles(e)
    if (!files.length) return
    await batchMoveDrop(files, entry.path, token, onFileMoved)
  }

  // ── Delete (empty only — button only renders for admins, see below) ──
  const handleDeleteClick = () => { submitDelete() }

  const submitDelete = async () => {
    setFolderError(null)
    const r = await adminFetch(
      `/api/admin/folder/delete?path=${encodeURIComponent(entry.path)}`,
      token, { method: 'DELETE' }
    )
    if (!r.ok) { setFolderError(await r.text()); return }
    onRefresh()
  }

  return (
    <div className="sbi-wrap">
      {/* Main row */}
      {renaming ? (
        <div className="sbi-row sbi-renaming" style={{ paddingLeft: `${10 + depth * 14}px` }}>
          <div className="sbi-thumb-wrap">
            <div className="sbi-thumb sbi-thumb-icon"><FolderIcon size={22} /></div>
          </div>
          <input
            ref={renameRef}
            className="sbi-rename-input"
            value={renamVal}
            onChange={e => setRenamVal(e.target.value)}
            onKeyDown={e => { if (e.key === 'Enter') submitRename(); if (e.key === 'Escape') setRenaming(false) }}
            onBlur={submitRename}
          />
        </div>
      ) : (
        <div
          className={`sbi-row ${isCurrent ? 'sbi-active' : ''} ${isAncestor ? 'sbi-ancestor' : ''} ${dragOver ? 'sbi-drag-over' : ''}`}
          style={{ paddingLeft: `${10 + depth * 14}px` }}
          onClick={() => onNavigate(entry.path)}
          onDragOver={handleDragOver}
          onDragLeave={handleDragLeave}
          onDrop={handleDrop}
        >
          <div className="sbi-thumb-wrap">
            {info?.firstImage
              ? <img className="sbi-thumb" src={`/api/thumb?path=${encodeURIComponent(info.firstImage)}`} alt="" loading="lazy" />
              : <div className="sbi-thumb sbi-thumb-icon"><FolderIcon size={22} /></div>
            }
          </div>

          <div className="sbi-text">
            <div className="sbi-name">{entry.name}</div>
            <div className="sbi-meta">{metaLine(info) ?? <span className="sbi-loading">…</span>}</div>
            {info?.totalSize > 0 && <div className="sbi-size">{fmtSize(info.totalSize)}</div>}
            {/* Action buttons — subtle row under the name */}
            <div className="sbi-actions" onClick={e => e.stopPropagation()}>
              <button className="sbi-act-btn" title="Pin to sidebar" onClick={() => onPin?.(entry.path)}><PinIcon size={14} /></button>
              <button className="sbi-act-btn" title="Open folder in Finder / Explorer"
                onClick={() => fetch(`/api/open-folder?path=${encodeURIComponent(entry.path)}`)
                  .then(r => r.json())
                  .then(d => setFolderPath(d))
                  .catch(() => {})
                }><OpenFolderIcon size={14} /></button>
              {token && <>
                <button className="sbi-act-btn" title="Rename folder" onClick={onRenameClick}><PencilIcon size={14} /></button>
                <button className="sbi-act-btn" title="New subfolder" onClick={onCreateClick}><PlusIcon size={14} /></button>
                <button
                  className={`sbi-act-btn sbi-act-del ${!isEmpty ? 'sbi-act-disabled' : ''}`}
                  title={isEmpty ? 'Delete folder' : 'Cannot delete — folder is not empty'}
                  onClick={isEmpty ? handleDeleteClick : undefined}
                ><TrashIcon size={14} /></button>
              </>}
            </div>
          </div>

          {hasSubfolders && (
            <button className="sbi-toggle" onClick={e => { e.stopPropagation(); setExpanded(v => !v) }}>
              {expanded ? <ChevronDown /> : <ChevronRight />}
            </button>
          )}
        </div>
      )}

      {/* Inline error */}
      {folderError && (
        <div className="sbi-folder-error" onClick={() => setFolderError(null)}>{folderError} ✕</div>
      )}

      {/* Opened toast */}
      {openedToast && <div className="sbi-opened-toast"><OpenFolderIcon size={13} /> Opened in Explorer</div>}

      {/* Folder path popup */}
      {folderPath && (() => {
        const isMac = /mac/i.test(navigator.platform) || /mac os/i.test(navigator.userAgent)
        const addr  = isMac ? folderPath.smbURL : folderPath.fullPath
        const label = isMac ? 'Mac address (smb://)' : 'Server path'

        return ReactDOM.createPortal(
          <div className="adm-overlay" onClick={() => setFolderPath(null)}>
            <div className="adm-modal" onClick={e => e.stopPropagation()}>
              <div className="adm-modal-icon"><OpenFolderIcon size={30} /></div>
              <h2 className="adm-modal-title">Open Folder</h2>

              {/* Share not configured */}
              {!folderPath.smbURL && (
                <p className="adm-warn" style={{marginBottom:10}}>
                  SMB share not configured. Set an <code style={{background:'#1a1a26',padding:'1px 4px',borderRadius:3}}>SMB Share Name</code> in Settings to enable network paths.
                </p>
              )}

              {/* Address for this OS */}
              {addr && (
                <>
                  <p className="adm-modal-sub" style={{marginBottom:6, textAlign:'left'}}>{label}:</p>
                  <div className="path-box">
                    <span className="path-text">{addr}</span>
                    <button className="path-copy-btn" onClick={() =>
                      navigator.clipboard.writeText(addr).catch(()=>{})
                    }>Copy</button>
                  </div>
                </>
              )}

              {/* Mac instructions */}
              {isMac && folderPath.smbURL && (
                <div style={{marginTop:12}}>
                  <a
                    href={folderPath.smbURL}
                    className="adm-btn adm-btn-primary"
                    style={{display:'block', textAlign:'center', marginBottom:10, textDecoration:'none'}}
                    onClick={() => setFolderPath(null)}
                  >
                    Open in Finder
                  </a>
                  <p className="adm-modal-sub" style={{textAlign:'center'}}>
                    Firefox/Chrome: press <kbd style={{background:'#1a1a26',padding:'1px 6px',borderRadius:4,border:'1px solid #2d2d36'}}>⌘K</kbd> in Finder and paste the address above.
                  </p>
                </div>
              )}

              <div className="adm-btns" style={{marginTop:14}}>
                <button className="adm-btn" onClick={() => setFolderPath(null)}>Close</button>
              </div>
            </div>
          </div>,
          document.body
        )
      })()}

      {/* Children + new folder input */}
      {expanded && (
        <div className="sbi-children">
          {creating && (
            <div className="sbi-new-folder-row" style={{ paddingLeft: `${10 + (depth + 1) * 14}px` }}>
              <div className="sbi-thumb sbi-thumb-icon" style={{width:44,height:44,flexShrink:0}}><FolderIcon size={22} /></div>
              <input
                ref={newFolderRef}
                className="sbi-rename-input"
                placeholder="Folder name…"
                value={newFolderName}
                onChange={e => setNewFolderName(e.target.value)}
                onKeyDown={e => { if (e.key === 'Enter') submitCreate(); if (e.key === 'Escape') { setCreating(false); setNewFolderName('') } }}
                onBlur={submitCreate}
              />
            </div>
          )}
          {children?.map(child => (
            <SidebarItem
              key={child.path}
              entry={child}
              currentPath={currentPath}
              onNavigate={onNavigate}
              depth={depth + 1}
              onRefresh={onRefresh}
              onFileMoved={onFileMoved}
              onPin={onPin}
            />
          ))}
        </div>
      )}
    </div>
  )
}

// ── Sidebar ───────────────────────────────────────────────────────────────────

const TRASH_PATH = '__trash__'

// ── PinnedItem ────────────────────────────────────────────────────────────────

function PinnedItem({ pinnedPath, currentPath, onNavigate, onUnpin, onFileMoved }) {
  const { token }         = useContext(AdminCtx)
  const [info, setInfo]   = useState(null)
  const [dragOver, setDragOver] = useState(false)

  useEffect(() => {
    fetch(`/api/folder-info?path=${encodeURIComponent(pinnedPath)}`)
      .then(r => r.json()).then(setInfo).catch(() => {})
  }, [pinnedPath])

  const name = pinnedPath.split('/').pop() || 'All Photos'
  const isActive = currentPath === pinnedPath

  const handleDragOver = (e) => {
    if (!token) return
    if (!e.dataTransfer.types.includes('application/photo-share')) return
    e.preventDefault(); e.dataTransfer.dropEffect = 'move'; setDragOver(true)
  }
  const handleDrop = async (e) => {
    e.preventDefault(); setDragOver(false)
    if (!token) return
    const files = parseDragFiles(e)
    if (!files.length) return
    await batchMoveDrop(files, pinnedPath, token, onFileMoved)
  }

  return (
    <div
      className={`pinned-item ${isActive ? 'pinned-active' : ''} ${dragOver ? 'sbi-drag-over' : ''}`}
      onClick={() => onNavigate(pinnedPath)}
      onDragOver={handleDragOver}
      onDragLeave={e => { if (!e.currentTarget.contains(e.relatedTarget)) setDragOver(false) }}
      onDrop={handleDrop}
    >
      <div className="pinned-thumb-wrap">
        {info?.firstImage
          ? <img className="pinned-thumb" src={`/api/thumb?path=${encodeURIComponent(info.firstImage)}`} alt="" loading="lazy" />
          : <div className="pinned-thumb pinned-thumb-icon"><FolderIcon size={18} /></div>
        }
      </div>
      <div className="pinned-name" title={pinnedPath}>{name}</div>
      <button className="pinned-unpin" title="Unpin" onClick={e => { e.stopPropagation(); onUnpin(pinnedPath) }}><CloseIcon size={13} /></button>
    </div>
  )
}

// ── UploadInboxItem ───────────────────────────────────────────────────────────

function UploadInboxItem({ folderName, currentPath, onNavigate, onFileMoved }) {
  const { token }               = useContext(AdminCtx)
  const [info, setInfo]         = useState(null)
  const [dragOver, setDragOver] = useState(false)

  useEffect(() => {
    fetch(`/api/folder-info?path=${encodeURIComponent(folderName)}`)
      .then(r => r.json()).then(setInfo).catch(() => {})
  }, [folderName])

  const handleDragOver = (e) => {
    if (!e.dataTransfer.types.includes('application/photo-share')) return
    e.preventDefault(); e.dataTransfer.dropEffect = 'move'; setDragOver(true)
  }
  const handleDrop = async (e) => {
    e.preventDefault(); setDragOver(false)
    if (!token) return
    const files = parseDragFiles(e)
    if (!files.length) return
    await batchMoveDrop(files, folderName, token, onFileMoved)
  }

  return (
    <button
      className={`inbox-item ${currentPath === folderName ? 'inbox-active' : ''} ${dragOver ? 'sbi-drag-over' : ''}`}
      onClick={() => onNavigate(folderName)}
      onDragOver={handleDragOver}
      onDragLeave={e => { if (!e.currentTarget.contains(e.relatedTarget)) setDragOver(false) }}
      onDrop={handleDrop}
    >
      <div className="sbi-thumb-wrap">
        {info?.firstImage
          ? <img className="sbi-thumb" src={`/api/thumb?path=${encodeURIComponent(info.firstImage)}`} alt="" loading="lazy" />
          : <div className="sbi-thumb sbi-thumb-icon" style={{color:'#f59e0b'}}><UploadIcon size={20} /></div>
        }
      </div>
      <div className="sbi-text">
        <div className="sbi-name" style={{color:'#fbbf24'}}>Upload Inbox</div>
        <div className="sbi-meta">{metaLine(info) ?? '…'}</div>
      </div>
    </button>
  )
}

function Sidebar({ currentPath, onNavigate, onFileMoved, onShowStats, onShowSettings, uploadFolderName }) {
  const { token } = useContext(AdminCtx)
  const [roots, setRoots]       = useState([])
  const [rootInfo, setRootInfo] = useState(null)
  const [refreshKey, setRefreshKey] = useState(0)
  const [query, setQuery]         = useState('')
  const [results, setResults]     = useState(null)
  const [searching, setSearching] = useState(false)
  const [searchType, setSearchType] = useState('')    // '' | 'image' | 'video'
  const [searchFrom, setSearchFrom] = useState('')
  const [searchTo, setSearchTo]     = useState('')
  const [showFilters, setShowFilters] = useState(false)
  const [creatingRoot, setCreatingRoot] = useState(false)
  const [rootFolderName, setRootFolderName] = useState('')
  const [rootError, setRootError] = useState(null)
  const rootInputRef  = useRef(null)
  const inputRef      = useRef(null)

  const [pinned, setPinned] = useState(() => {
    try { return JSON.parse(localStorage.getItem('photoshare-pinned') || '[]') }
    catch { return [] }
  })

  const pin = (path) => setPinned(prev => {
    if (prev.includes(path)) return prev
    const next = [...prev, path]
    localStorage.setItem('photoshare-pinned', JSON.stringify(next))
    return next
  })

  const unpin = (path) => setPinned(prev => {
    const next = prev.filter(p => p !== path)
    localStorage.setItem('photoshare-pinned', JSON.stringify(next))
    return next
  })
  const [rootDragOver, setRootDragOver] = useState(false)

  const handleRootDragOver = (e) => {
    if (!token) return
    if (!e.dataTransfer.types.includes('application/photo-share')) return
    e.preventDefault(); e.dataTransfer.dropEffect = 'move'; setRootDragOver(true)
  }
  const handleRootDrop = async (e) => {
    e.preventDefault(); setRootDragOver(false)
    if (!token) return
    const files = parseDragFiles(e)
    if (!files.length) return
    await batchMoveDrop(files, '', token, onFileMoved)
  }

  const refresh = () => setRefreshKey(k => k + 1)

  useEffect(() => {
    fetch('/api/browse?path=')
      .then(r => r.json())
      .then(data => setRoots((data || []).filter(e => e.isDir)))
      .catch(() => {})
    fetch('/api/folder-info?path=')
      .then(r => r.json())
      .then(setRootInfo)
      .catch(() => {})
  }, [refreshKey])

  useEffect(() => { if (creatingRoot) rootInputRef.current?.focus() }, [creatingRoot])

  const submitRootCreate = async () => {
    const name = rootFolderName.trim()
    if (!name) { setCreatingRoot(false); return }
    setRootError(null)
    const r = await adminFetch(
      `/api/admin/folder/create?path=&name=${encodeURIComponent(name)}`,
      token, { method: 'POST' }
    )
    if (!r.ok) { setRootError(await r.text()); return }
    setCreatingRoot(false)
    setRootFolderName('')
    refresh()
  }

  // Debounced search
  useEffect(() => {
    const hasFilters = searchType || searchFrom || searchTo
    if (!query.trim() && !hasFilters) { setResults(null); setSearching(false); return }
    setSearching(true)
    const t = setTimeout(() => {
      const params = new URLSearchParams()
      if (query.trim()) params.set('q', query.trim())
      if (searchType)   params.set('type', searchType)
      if (searchFrom)   params.set('from', searchFrom)
      if (searchTo)     params.set('to', searchTo)
      fetch(`/api/search?${params}`)
        .then(r => r.json())
        .then(data => { setResults(data); setSearching(false) })
        .catch(() => setSearching(false))
    }, 300)
    return () => clearTimeout(t)
  }, [query, searchType, searchFrom, searchTo])

  const clearSearch = () => { setQuery(''); setSearchType(''); setSearchFrom(''); setSearchTo(''); setResults(null); setShowFilters(false); inputRef.current?.focus() }

  // Navigate to parent folder of a file result, or into a folder result
  const handleResultClick = (item) => {
    onNavigate(item.isDir ? item.path : item.parent)
    clearSearch()
  }

  return (
    <aside className="sidebar">
      <div className="sidebar-header">Library</div>

      {/* Pinned folders */}
      {pinned.length > 0 && (
        <>
          <div className="sidebar-header" style={{paddingTop:6}}><PinIcon size={12} /> Pinned</div>
          <div className="pinned-list">
            {pinned.map(p => (
              <PinnedItem
                key={p}
                pinnedPath={p}
                currentPath={currentPath}
                onNavigate={onNavigate}
                onUnpin={unpin}
                onFileMoved={onFileMoved}
              />
            ))}
          </div>
          <div className="sidebar-divider" />
        </>
      )}

      {/* Search box */}
      <div className="search-wrap">
        <span className="search-icon">
          <svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" strokeWidth="2.2" strokeLinecap="round" strokeLinejoin="round">
            <circle cx="11" cy="11" r="7" />
            <line x1="21" y1="21" x2="16.65" y2="16.65" />
          </svg>
        </span>
        <input
          ref={inputRef}
          className="search-input"
          type="text"
          placeholder="Search files…"
          value={query}
          onChange={e => setQuery(e.target.value)}
        />
        <button
          className="search-clear"
          title="Filters"
          onClick={() => setShowFilters(v => !v)}
          style={{ color: showFilters ? '#818cf8' : undefined, display: 'flex', alignItems: 'center' }}
        >
          <svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" strokeWidth="2.2" strokeLinecap="round" strokeLinejoin="round">
            <polygon points="22 3 2 3 10 12.46 10 19 14 21 14 12.46 22 3" />
          </svg>
        </button>
        {(query || searchType || searchFrom || searchTo) && (
          <button className="search-clear" onClick={clearSearch}><CloseIcon size={14} /></button>
        )}
      </div>

      {/* Search filters */}
      {showFilters && (
        <div className="search-filters">
          <select className="sort-select" style={{flex:'1 1 100%', fontSize:'0.75rem'}} value={searchType} onChange={e => setSearchType(e.target.value)}>
            <option value="">All types</option>
            <option value="image">Photos only</option>
            <option value="video">Videos only</option>
          </select>
          <input type="date" className="search-date" value={searchFrom} onChange={e => setSearchFrom(e.target.value)} title="From date" />
          <input type="date" className="search-date" value={searchTo}   onChange={e => setSearchTo(e.target.value)}   title="To date" />
        </div>
      )}

      {/* Thumbnail regen button — admin only */}
      {token && (
        <button className="thumb-regen-btn" title="Clear and rebuild all thumbnails" onClick={async () => {
          await adminFetch('/api/thumbs/clear', token, { method: 'POST' })
        }}><RefreshIcon size={13} /> Rebuild Thumbnails</button>
      )}

      {/* Search results */}
      {query.trim() ? (
        <div className="search-results">
          {searching && <div className="search-status">Searching…</div>}
          {!searching && results?.length === 0 && (
            <div className="search-status">No results for "{query}"</div>
          )}
          {!searching && results?.map(item => (
            <button
              key={item.path}
              className="search-result-item"
              onClick={() => handleResultClick(item)}
              title={item.path}
            >
              <div className="sbi-thumb-wrap">
                {item.isDir ? (
                  <div className="sbi-thumb sbi-thumb-icon"><FolderIcon size={22} /></div>
                ) : (
                  <img
                    className="sbi-thumb"
                    src={`/api/thumb?path=${encodeURIComponent(item.path)}`}
                    alt={item.name}
                    loading="lazy"
                  />
                )}
              </div>
              <div className="sbi-text">
                <div className="sbi-name search-match">{item.name}</div>
                <div className="sbi-meta">{item.parent || '/'}</div>
              </div>
              {item.isVideo && <span className="search-video-tag">video</span>}
            </button>
          ))}
        </div>
      ) : (
        <>
          {/* Root / All Photos */}
          <button
            className={`sbi-all ${currentPath === '' ? 'sbi-active' : ''} ${rootDragOver ? 'sbi-drag-over' : ''}`}
            onClick={() => onNavigate('')}
            onDragOver={handleRootDragOver}
            onDragLeave={() => setRootDragOver(false)}
            onDrop={handleRootDrop}
          >
            <div className="sbi-thumb-wrap">
              <div className="sbi-thumb sbi-thumb-icon"><HomeIcon /></div>
            </div>
            <div className="sbi-text">
              <div className="sbi-name">All Photos</div>
              <div className="sbi-meta">{metaLine(rootInfo) ?? '…'}</div>
            </div>
          </button>

          {/* Upload Inbox — permanent pinned entry, no edit tools */}
          <UploadInboxItem
            folderName={uploadFolderName}
            currentPath={currentPath}
            onNavigate={onNavigate}
            onFileMoved={onFileMoved}
          />

          <div className="sidebar-divider" />

          {/* New root folder — admin only */}
          {token && (
          <div className="sbi-root-actions">
            {creatingRoot ? (
              <div className="sbi-new-folder-row sbi-new-folder-root">
                <div className="sbi-thumb sbi-thumb-icon" style={{width:44,height:44,flexShrink:0}}><FolderIcon size={22}/></div>
                <input
                  ref={rootInputRef}
                  className="sbi-rename-input"
                  placeholder="Folder name…"
                  value={rootFolderName}
                  onChange={e => setRootFolderName(e.target.value)}
                  onKeyDown={e => { if (e.key==='Enter') submitRootCreate(); if (e.key==='Escape') { setCreatingRoot(false); setRootFolderName('') }}}
                  onBlur={submitRootCreate}
                />
              </div>
            ) : (
              <button className="sbi-new-root-btn" onClick={() => setCreatingRoot(true)}><PlusIcon size={14} /> New Folder</button>
            )}
            {rootError && <div className="sbi-folder-error" onClick={() => setRootError(null)}>{rootError} ✕</div>}
          </div>
          )}

          <nav className="sidebar-nav">
            {roots.map(r => (
              <SidebarItem
                key={r.path + refreshKey}
                entry={r}
                currentPath={currentPath}
                onNavigate={onNavigate}
                depth={0}
                onRefresh={refresh}
                onFileMoved={onFileMoved}
                onPin={pin}
              />
            ))}
          </nav>
        </>
      )}

      {/* Bottom buttons */}
      <div className="sidebar-divider" style={{marginTop:'auto'}} />
      <div className="sidebar-utils">
        <button className={`util-btn ${currentPath === MEMORIES_PATH ? 'util-active' : ''}`} onClick={() => onNavigate(MEMORIES_PATH)}><SparkleIcon size={14} /> On This Day</button>
        <button className={`util-btn ${currentPath === MAP_PATH ? 'util-active' : ''}`} onClick={() => onNavigate(MAP_PATH)}><MapPinIcon size={14} /> Map</button>
        <button className={`util-btn ${currentPath === DUPES_PATH ? 'util-active' : ''}`} onClick={() => onNavigate(DUPES_PATH)}><CopyIcon size={14} /> Duplicates</button>
        {token && <button className="util-btn" onClick={() => onShowSettings()}><GearIcon size={14} /> Settings</button>}
        <button className="util-btn" onClick={() => onShowStats()}><StatsIcon size={14} /> Storage Stats</button>
        <button className={`util-btn ${currentPath === TRASH_PATH ? 'util-active' : ''}`} onClick={() => onNavigate(TRASH_PATH)}><TrashIcon size={14} /> Recycle Bin</button>
      </div>
    </aside>
  )
}

const DUPES_PATH = '__duplicates__'
const MEMORIES_PATH = '__memories__'
const MAP_PATH = '__map__'

// ── MapView (geotagged photos on a map) ────────────────────────────────────────
function MapView({ onOpen }) {
  const containerRef = useRef(null)
  const mapRef = useRef(null)
  const [status, setStatus] = useState('loading') // loading | building | empty | ok | error

  useEffect(() => {
    if (!containerRef.current || mapRef.current) return
    const map = L.map(containerRef.current).setView([20, 0], 2)
    L.tileLayer('https://{s}.tile.openstreetmap.org/{z}/{x}/{y}.png', {
      maxZoom: 19,
      attribution: '© OpenStreetMap contributors',
    }).addTo(map)
    mapRef.current = map
    setTimeout(() => map.invalidateSize(), 120)

    fetch('/api/geo').then(r => r.json()).then(d => {
      if (!d.built) { setStatus('building'); return }
      const pts = d.points || []
      if (!pts.length) { setStatus('empty'); return }
      setStatus('ok')
      setTimeout(() => map.invalidateSize(), 60)
      const markers = pts.map(p => {
        const icon = L.divIcon({ className: 'map-pin', iconSize: [14, 14] })
        const m = L.marker([p.lat, p.lng], { icon, title: p.name })
        m.on('click', () => onOpen({ name: p.name, path: p.path, isVideo: p.isVideo }))
        return m
      })
      const group = L.featureGroup(markers).addTo(map)
      try { map.fitBounds(group.getBounds().pad(0.2)) } catch {}
    }).catch(() => setStatus('error'))

    return () => { map.remove(); mapRef.current = null }
  }, [])

  return (
    <div className="map-view">
      {status === 'building' && <div className="map-banner">Building location index… reopen in a moment.</div>}
      {status === 'empty' && <div className="map-banner">No geotagged photos found in your library.</div>}
      {status === 'error' && <div className="map-banner">Couldn't load the map.</div>}
      <div ref={containerRef} className="map-canvas" />
    </div>
  )
}

// ── MemoriesView ("On This Day") ───────────────────────────────────────────────
function MemoriesView({ onOpen }) {
  const [data, setData] = useState(null)

  useEffect(() => {
    let alive = true
    const load = () => fetch('/api/on-this-day').then(r => r.json()).then(d => { if (alive) setData(d) }).catch(() => {})
    load()
    // If the index isn't built yet, poll until it is.
    const iv = setInterval(() => { if (!data?.built) load(); else clearInterval(iv) }, 3000)
    return () => { alive = false; clearInterval(iv) }
  }, [data?.built])

  const today = new Date().toLocaleDateString(undefined, { month: 'long', day: 'numeric' })

  if (!data) return <div className="status"><div className="spinner" /><span>Loading…</span></div>
  if (!data.built) return <div className="status"><div className="spinner" /><span>Building memories index…</span></div>
  if (!data.groups?.length) return <div className="status muted">No photos taken on {today} in past years — check back another day.</div>

  return (
    <div className="memories-view">
      <div className="memories-head">
        <h2 className="trash-title"><SparkleIcon size={18} /> On This Day</h2>
        <span className="memories-sub">{today}</span>
      </div>
      {data.groups.map(g => (
        <div key={g.year} className="memories-group">
          <div className="memories-year">
            {g.year} <span className="memories-ago">· {g.yearsAgo} year{g.yearsAgo !== 1 ? 's' : ''} ago</span>
          </div>
          <div className="memories-grid">
            {g.items.map(it => (
              <button key={it.path} className="memories-cell" title={it.name}
                onClick={() => onOpen({ name: it.name, path: it.path, isVideo: it.isVideo })}>
                <img src={`/api/thumb?path=${encodeURIComponent(it.path)}`} alt={it.name} loading="lazy" />
                {it.isVideo && <span className="memories-play"><PlayIcon size={14} /></span>}
              </button>
            ))}
          </div>
        </div>
      ))}
    </div>
  )
}

// ── DuplicatesView ────────────────────────────────────────────────────────────

function DuplicatesView() {
  const { token }               = useContext(AdminCtx)
  const [data, setData]         = useState(null)
  const [loading, setLoading]   = useState(true)
  const [error, setError]       = useState(null)
  const [status, setStatus]     = useState(null)

  const scan = () => {
    setLoading(true); setData(null); setError(null)
    fetch('/api/duplicates')
      .then(r => { if (!r.ok) throw new Error(`Server error ${r.status}`); return r.json() })
      .then(d => { setData(d); setLoading(false) })
      .catch(e => { setError(e.message); setLoading(false) })
  }

  useEffect(() => { scan() }, [])

  const doDelete = async (file, tok) => {
    await adminFetch(`/api/admin/delete?path=${encodeURIComponent(file.path)}`, tok, { method: 'DELETE' })
    setData(prev => ({
      ...prev,
      groups: prev.groups
        .map(g => ({ ...g, files: g.files.filter(f => f.path !== file.path) }))
        .filter(g => g.files.length > 1)
    }))
    setStatus('✓ Moved to trash')
    setTimeout(() => setStatus(null), 2000)
  }

  const trash = (file) => doDelete(file, token)

  return (
    <div className="trash-view">
      <div className="trash-header">
        <span className="trash-title"><CopyIcon size={16} /> Duplicate Finder</span>
        {data && <span className="trash-count">{data.groups?.length || 0} groups · {fmtBytes(data.totalWaste)} wasted</span>}
        <button className="trash-empty-btn" onClick={scan} style={{background:'none',borderColor:'#3730a3',color:'#a5b4fc'}}><RefreshIcon size={13} /> Re-scan</button>
        {status && <span className="batch-status">{status}</span>}
      </div>

      {loading && (
        <div className="status">
          <div className="spinner" />
          <span>Scanning library… this may take a few minutes on large folders.</span>
        </div>
      )}

      {error && <div className="status error">⚠ {error}</div>}
      {!loading && !error && data?.groups?.length === 0 && (
        <div className="status muted">✓ No exact duplicates found.</div>
      )}

      {!loading && !error && data?.groups?.map(group => (
        <div key={group.hash} className="dup-group">
          <div className="dup-group-header">
            <span className="dup-size">{fmtBytes(group.size)} each</span>
            <span className="dup-waste">· {fmtBytes(group.size * (group.files.length - 1))} wasted</span>
          </div>
          <div className="dup-files">
            {group.files.map((file, i) => (
              <div key={file.path} className={`dup-file ${i === 0 ? 'dup-keep' : ''}`}>
                <div className="dup-thumb-wrap">
                  {(isImageExt(file.name) || isVideoExt(file.name)) && (
                    <img className="dup-thumb" src={`/api/thumb?path=${encodeURIComponent(file.path)}`} alt={file.name} loading="lazy" />
                  )}
                  {i === 0 && <div className="dup-keep-badge">Keep</div>}
                </div>
                <div className="dup-info">
                  <div className="dup-name">{file.name}</div>
                  <div className="dup-path"><FolderIcon size={11} /> {file.path.split('/').slice(0, -1).join('/') || 'root'}</div>
                  <div className="dup-date"><CalendarIcon size={11} /> {file.mod}</div>
                </div>
                {i !== 0 && token && (
                  <button className="trash-btn-purge" onClick={() => trash(file)}><TrashIcon size={14} /> Delete</button>
                )}
              </div>
            ))}
          </div>
        </div>
      ))}
    </div>
  )
}

// Helper to check extension without Go backend
const isImageExt = n => /\.(jpg|jpeg|png|gif|webp|bmp|tiff|tif|heic|heif)$/i.test(n)
const isVideoExt = n => /\.(mp4|mkv|mov|avi|wmv|webm|m4v|flv|ts)$/i.test(n)

// ── TrashView ─────────────────────────────────────────────────────────────────

// ── EmptyTrashConfirm — double warning with 10s countdown ─────────────────────

function EmptyTrashConfirm({ onConfirm, onClose }) {
  const [secs, setSecs] = useState(10)
  const [busy, setBusy] = useState(false)

  useEffect(() => {
    if (secs <= 0) return
    const t = setTimeout(() => setSecs(s => s - 1), 1000)
    return () => clearTimeout(t)
  }, [secs])

  const confirm = async () => {
    setBusy(true)
    await onConfirm()
    setBusy(false)
  }

  return (
    <div className="adm-overlay" onClick={onClose}>
      <div className="adm-modal adm-confirm" onClick={e => e.stopPropagation()}>
        <div className="adm-modal-icon"><WarnIcon size={30} /></div>
        <h2 className="adm-modal-title">Empty Trash Forever?</h2>
        <p className="adm-confirm-name">Every file in the trash will be permanently deleted.</p>
        <p className="adm-warn">This action is irreversible. There is no way to recover these files.</p>

        {secs > 0 ? (
          <div className="empty-trash-countdown">
            Please wait <strong>{secs}s</strong> before confirming…
          </div>
        ) : (
          <p className="adm-warn" style={{color:'#4ade80'}}>✓ You can now confirm deletion.</p>
        )}

        <div className="adm-btns" style={{marginTop:14}}>
          <button className="adm-btn" onClick={onClose} disabled={busy}>Cancel</button>
          <button
            className="adm-btn adm-btn-danger"
            onClick={confirm}
            disabled={secs > 0 || busy}
            style={{opacity: secs > 0 ? 0.35 : 1}}
          >
            {busy ? 'Deleting…' : <><TrashIcon size={14} /> Delete Forever</>}
          </button>
        </div>
      </div>
    </div>
  )
}

function TrashView() {
  const { token } = useContext(AdminCtx)
  const [items, setItems]             = useState([])
  const [loading, setLoading]         = useState(true)
  const [status, setStatus]           = useState(null)
  const [showEmptyConfirm, setShowEmptyConfirm] = useState(false)

  const load = () => {
    setLoading(true)
    fetch('/api/trash').then(r => r.json()).then(data => { setItems(data || []); setLoading(false) }).catch(() => setLoading(false))
  }

  useEffect(() => { load() }, [])

  const toast = (msg) => { setStatus(msg); setTimeout(() => setStatus(null), 3000) }

  // Runs the actual action with a token (either existing admin token or temp from password prompt)
  const runAction = async (tok, action) => {
    if (action.type === 'restore') {
      const r = await adminFetch('/api/trash/restore', tok, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ name: action.item.name, originalPath: action.item.originalPath })
      })
      if (r.ok) { toast(`✓ Restored to ${action.item.originalPath || 'root'}`); load() }
      else toast('Restore failed: ' + await r.text())
    } else if (action.type === 'purge') {
      await adminFetch(`/api/trash/purge?file=${encodeURIComponent(action.item.name)}`, tok, { method: 'DELETE' })
      toast('✓ Permanently deleted'); load()
    } else if (action.type === 'purge-all') {
      await adminFetch('/api/trash/purge-all', tok, { method: 'DELETE' })
      toast('✓ Trash emptied'); load()
    }
  }

  const handle = (action) => {
    if (action.type === 'purge-all') {
      // Always show the countdown warning first
      setShowEmptyConfirm(true)
      return
    }
    runAction(token, action)
  }

  const handleEmptyConfirmed = () => {
    setShowEmptyConfirm(false)
    runAction(token, { type: 'purge-all' })
  }

  return (
    <div className="trash-view">
      <div className="trash-header">
        <span className="trash-title"><TrashIcon size={16} /> Recycle Bin</span>
        <span className="trash-count">{items.length} item{items.length !== 1 ? 's' : ''}</span>
        {token && items.length > 0 && (
          <button className="trash-empty-btn" onClick={() => handle({ type: 'purge-all' })}>
            Empty Trash
          </button>
        )}
        {status && <span className="batch-status">{status}</span>}
      </div>

      {loading && <div className="status"><div className="spinner" /><span>Loading…</span></div>}
      {!loading && items.length === 0 && <div className="status muted">Trash is empty.</div>}

      {!loading && items.length > 0 && (
        <div className="trash-list">
          {items.map(item => (
            <div key={item.name} className="trash-item">
              <div className="trash-thumb-wrap">
                {(item.isImage || item.isVideo) ? (
                  <img className="trash-thumb" src={`/api/trash/thumb?file=${encodeURIComponent(item.name)}`} alt={item.name} loading="lazy" />
                ) : (
                  <div className="trash-thumb trash-thumb-folder"><FolderIcon size={28} /></div>
                )}
                {item.isVideo && <div className="playlist-video-badge"><PlayIcon size={10} /></div>}
              </div>

              <div className="trash-info">
                <div className="trash-name">{item.name}</div>
                {item.originalPath && <div className="trash-orig"><FolderIcon size={11} /> {item.originalPath}</div>}
                {item.deletedAt   && <div className="trash-date"><CalendarIcon size={11} /> {item.deletedAt}</div>}
                {item.size > 0    && <div className="trash-date"><HardDriveIcon size={11} /> {fmtBytes(item.size)}</div>}
              </div>

              {token && (
                <div className="trash-actions">
                  <button className="trash-btn-restore" onClick={() => handle({ type: 'restore', item })}><RestoreIcon size={14} /> Restore</button>
                  <button className="trash-btn-purge"   onClick={() => handle({ type: 'purge',   item })}><TrashIcon size={14} /> Delete</button>
                </div>
              )}
            </div>
          ))}
        </div>
      )}

      {/* 10-second countdown warning for Empty Trash */}
      {showEmptyConfirm && (
        <EmptyTrashConfirm
          onConfirm={handleEmptyConfirmed}
          onClose={() => setShowEmptyConfirm(false)}
        />
      )}
    </div>
  )
}

// ── BatchRenameModal ──────────────────────────────────────────────────────────

function BatchRenameModal({ paths, onClose, onDone, adminToken }) {
  const [pattern, setPattern] = useState('{name}')
  const [start, setStart]     = useState(1)
  const [padding, setPadding] = useState(3)
  const [busy, setBusy]       = useState(false)

  const preview = paths.slice(0, 4).map((p, i) => {
    const base = p.split('/').pop()
    const ext  = base.includes('.') ? '.' + base.split('.').pop() : ''
    const name = base.slice(0, base.length - ext.length)
    const n    = String(start + i).padStart(padding, '0')
    const newName = pattern.replace('{n}', n).replace('{name}', name) + ext
    return { from: base, to: newName }
  })

  const execute = async (tok) => {
    setBusy(true)
    const r = await adminFetch('/api/admin/batch/rename', tok, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ paths, pattern, start, padding })
    })
    setBusy(false)
    const d = await r.json()
    onDone(d.errors?.length || 0)
  }

  const submit = () => {
    if (adminToken) execute(adminToken)
  }

  return (
    <div className="adm-overlay" onClick={onClose}>
        <div className="adm-modal" style={{width:440}} onClick={e => e.stopPropagation()}>
          <div className="adm-modal-icon"><PencilIcon size={30} /></div>
          <h2 className="adm-modal-title">Batch Rename</h2>
          <p className="adm-modal-sub">{paths.length} file{paths.length !== 1 ? 's' : ''} selected</p>

          <div className="rename-fields">
            <label className="rename-label">Pattern
              <input className="adm-input" value={pattern} onChange={e => setPattern(e.target.value)} placeholder="{name}_{n}" />
              <span className="rename-hint">Use <code>{'{n}'}</code> for number, <code>{'{name}'}</code> for original name</span>
            </label>
            <div className="rename-row">
              <label className="rename-label">Start at
                <input className="adm-input" type="number" value={start} min={0} onChange={e => setStart(+e.target.value)} style={{width:80}} />
              </label>
              <label className="rename-label">Padding
                <input className="adm-input" type="number" value={padding} min={1} max={6} onChange={e => setPadding(+e.target.value)} style={{width:80}} />
              </label>
            </div>
          </div>

          {preview.length > 0 && (
            <div className="rename-preview">
              <div className="rename-preview-header">Preview</div>
              {preview.map((p, i) => (
                <div key={i} className="rename-preview-row">
                  <span className="rename-from">{p.from}</span>
                  <span className="rename-arrow">→</span>
                  <span className="rename-to">{p.to}</span>
                </div>
              ))}
              {paths.length > 4 && <div className="rename-more">+{paths.length - 4} more…</div>}
            </div>
          )}

          <div className="adm-btns" style={{marginTop:14}}>
            <button className="adm-btn" onClick={onClose} disabled={busy}>Cancel</button>
            <button className="adm-btn adm-btn-primary" onClick={submit} disabled={busy || !adminToken}>
              {busy ? 'Renaming…' : 'Rename'}
            </button>
          </div>
        </div>
    </div>
  )
}

// ── StatsModal ────────────────────────────────────────────────────────────────

function StatsModal({ onClose }) {
  const [stats, setStats] = useState(null)
  const [loading, setLoading] = useState(true)

  useEffect(() => {
    fetch('/api/stats').then(r => r.json()).then(d => { setStats(d); setLoading(false) }).catch(() => setLoading(false))
  }, [])

  return (
    <div className="adm-overlay" onClick={onClose}>
      <div className="adm-modal" style={{width:420}} onClick={e => e.stopPropagation()}>
        <div className="adm-modal-icon"><StatsIcon size={30} /></div>
        <h2 className="adm-modal-title">Storage Stats</h2>
        {loading && <div className="status"><div className="spinner" /> Loading…</div>}
        {stats && (
          <>
            {/* Disk usage bar */}
            {stats.diskTotal > 0 && (
              <div className="disk-bar-wrap">
                <div className="disk-bar-track">
                  <div className="disk-bar-used" style={{width: `${Math.round((stats.diskTotal - stats.diskFree) / stats.diskTotal * 100)}%`}} />
                  <div className="disk-bar-library" style={{width: `${Math.round(stats.totalSize / stats.diskTotal * 100)}%`}} />
                </div>
                <div className="disk-bar-legend">
                  <span><span className="disk-dot disk-dot-used"/>Disk used: {fmtBytes(stats.diskTotal - stats.diskFree)}</span>
                  <span><span className="disk-dot disk-dot-lib"/>Library: {fmtBytes(stats.totalSize)}</span>
                  <span style={{marginLeft:'auto'}}>Free: {fmtBytes(stats.diskFree)} / {fmtBytes(stats.diskTotal)}</span>
                </div>
              </div>
            )}

            <div className="stats-grid">
              <div className="stats-card"><div className="stats-val">{fmtBytes(stats.totalSize)}</div><div className="stats-lbl">Library Size</div></div>
              <div className="stats-card"><div className="stats-val">{stats.totalPhotos.toLocaleString()}</div><div className="stats-lbl">Photos</div></div>
              <div className="stats-card"><div className="stats-val">{stats.totalVideos.toLocaleString()}</div><div className="stats-lbl">Videos</div></div>
              <div className="stats-card"><div className="stats-val">{stats.totalFolders.toLocaleString()}</div><div className="stats-lbl">Folders</div></div>
            </div>
            <div className="stats-top-header">Largest Folders</div>
            <div className="stats-top-list">
              {stats.topFolders?.map((f, i) => (
                <div key={f.path} className="stats-top-row">
                  <span className="stats-rank">#{i+1}</span>
                  <span className="stats-folder-name">{f.path}</span>
                  <span className="stats-folder-files">{f.files.toLocaleString()} files</span>
                  <span className="stats-folder-size">{fmtBytes(f.size)}</span>
                </div>
              ))}
            </div>
          </>
        )}
        <div className="adm-btns" style={{marginTop:12}}>
          <button className="adm-btn" onClick={onClose}>Close</button>
        </div>
      </div>
    </div>
  )
}

// ── PhotoCard ─────────────────────────────────────────────────────────────────

function PhotoCard({ entry, onOpen, adminToken, selectMode, isSelected, onToggle, onDeleteRequest, focused, onFocus, selItems }) {
  const { onMouseEnter, onMouseLeave, onMouseMove, tooltip } = useMetaTooltip(entry.path)

  return (
    <button
      className={`card card-photo ${isSelected ? 'card-selected' : ''} ${focused ? 'card-grid-focus' : ''}`}
      data-sel-path={entry.path}
      onClick={(e) => { onFocus?.(); if (selectMode) { onToggle(entry.path, e.shiftKey); return }; onOpen(entry) }}
      title={entry.name}
      style={{position:'relative'}}
      draggable={!!adminToken && !selectMode}
      onMouseEnter={onMouseEnter}
      onMouseLeave={onMouseLeave}
      onMouseMove={onMouseMove}
      onDragStart={e => {
        // If this file is part of a multi-selection, drag all selected files
        const files = isSelected && selItems?.size > 1
          ? [...selItems].map(p => ({ path: p, name: p.split('/').pop() }))
          : [{ path: entry.path, name: entry.name }]
        e.dataTransfer.setData('application/photo-share', JSON.stringify({ files }))
        e.dataTransfer.effectAllowed = 'move'
        e.currentTarget.classList.add('card-dragging')
      }}
      onDragEnd={e => e.currentTarget.classList.remove('card-dragging')}
    >
      {selectMode && <div className="card-checkbox">{isSelected ? '✓' : ''}</div>}
      {!selectMode && <TrashBtn entry={entry} />}
      <img className="card-thumb photo-thumb" src={`/api/thumb?path=${encodeURIComponent(entry.path)}`} alt={entry.name} loading="lazy" />
      <div className="card-label">{entry.name}</div>
      {tooltip}
    </button>
  )
}

// ── SettingsModal ─────────────────────────────────────────────────────────────

// ── QR connect modal ──────────────────────────────────────────────────────────
function QRModal({ onClose }) {
  const [url, setUrl] = useState('')
  useEffect(() => {
    fetch('/api/server-info').then(r => r.json()).then(d => setUrl(d.url || '')).catch(() => {})
  }, [])
  return ReactDOM.createPortal(
    <div className="adm-overlay" onClick={onClose}>
      <div className="adm-modal" onClick={e => e.stopPropagation()} style={{ textAlign: 'center' }}>
        <div className="adm-modal-icon"><QrIcon size={28} /></div>
        <h2 className="adm-modal-title">Connect a device</h2>
        <p className="adm-modal-sub">Scan with your phone's camera to open PhotoShare.</p>
        <div className="qr-frame">
          <img src="/api/qr" alt="QR code" width={240} height={240} />
        </div>
        {url && <p className="qr-url">{url}</p>}
        <div className="adm-btns" style={{ justifyContent: 'center', marginTop: 14 }}>
          <button className="adm-btn" onClick={onClose}>Close</button>
        </div>
      </div>
    </div>,
    document.body
  )
}

// ── Keyboard shortcuts overlay ──────────────────────────────────────────────────
function ShortcutsModal({ onClose }) {
  const rows = [
    ['?', 'Show / hide this help'],
    ['← / →', 'Previous / next photo (in viewer)'],
    ['Arrows', 'Move focus in the grid'],
    ['Enter / Space', 'Open the focused item'],
    ['Esc', 'Close viewer or dialog'],
    ['Alt + ← / →', 'Back / forward through folders'],
    ['Click', 'In select mode: toggle one item'],
    ['Shift + Click', 'Select a range of items'],
    ['Drag on empty space', 'Marquee-select (in select mode)'],
  ]
  return ReactDOM.createPortal(
    <div className="adm-overlay" onClick={onClose}>
      <div className="adm-modal" onClick={e => e.stopPropagation()} style={{ width: 420 }}>
        <h2 className="adm-modal-title">Keyboard shortcuts</h2>
        <div className="shortcuts-list">
          {rows.map(([k, d]) => (
            <div className="shortcut-row" key={k}>
              <kbd className="shortcut-key">{k}</kbd>
              <span className="shortcut-desc">{d}</span>
            </div>
          ))}
        </div>
        <div className="adm-btns" style={{ justifyContent: 'center', marginTop: 14 }}>
          <button className="adm-btn" onClick={onClose}>Close</button>
        </div>
      </div>
    </div>,
    document.body
  )
}

// ── User management (admin) — lives inside Settings ─────────────────────────────
function UsersManager() {
  const [data, setData] = useState(null) // { users:[{username,role}], guestAccess }
  const [u, setU]       = useState('')
  const [p, setP]       = useState('')
  const [role, setRole] = useState('viewer')
  const [err, setErr]   = useState(null)

  const load = () => fetch('/api/users', { credentials: 'same-origin' })
    .then(r => r.ok ? r.json() : Promise.reject()).then(setData).catch(() => {})
  useEffect(() => { load() }, [])

  const addUser = async () => {
    setErr(null)
    const r = await fetch('/api/users', {
      method: 'POST', credentials: 'same-origin',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ username: u, password: p, role }),
    })
    if (!r.ok) { setErr(await r.text()); return }
    setU(''); setP(''); setRole('viewer'); load()
  }
  const delUser = async (name) => {
    const r = await fetch(`/api/users?username=${encodeURIComponent(name)}`, { method: 'DELETE', credentials: 'same-origin' })
    if (!r.ok) { setErr(await r.text()); return }
    load()
  }
  const toggleGuest = async (enabled) => {
    await fetch('/api/guest-access', {
      method: 'POST', credentials: 'same-origin',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ enabled }),
    })
    load()
  }

  if (!data) return null
  return (
    <div className="users-manager">
      <div className="settings-label-head" style={{ marginBottom: 6 }}>Users &amp; access</div>
      <div className="users-list">
        {data.users.map(usr => (
          <div className="user-row" key={usr.username}>
            <span className="user-name">
              {usr.role === 'admin' ? <UnlockIcon size={12} /> : <LockIcon size={12} />} {usr.username}
            </span>
            <span className="user-role">{usr.role}</span>
            <button className="user-del" title="Remove user" onClick={() => delUser(usr.username)}><TrashIcon size={13} /></button>
          </div>
        ))}
      </div>
      <div className="user-add">
        <input className="adm-input" placeholder="username" value={u} onChange={e => setU(e.target.value)} autoComplete="off" />
        <input className="adm-input" type="password" placeholder="password" value={p} onChange={e => setP(e.target.value)} autoComplete="new-password" />
        <select className="sort-select" value={role} onChange={e => setRole(e.target.value)}>
          <option value="viewer">Viewer</option>
          <option value="admin">Admin</option>
        </select>
        <button className="adm-btn" type="button" onClick={addUser}><PlusIcon size={13} /> Add</button>
      </div>
      {err && <div className="adm-error">{err}</div>}
      <label className="settings-check" style={{ marginTop: 10 }}>
        <input type="checkbox" checked={!!data.guestAccess} onChange={e => toggleGuest(e.target.checked)} />
        <span className="settings-label-head">Allow guest access (view-only, no password)</span>
      </label>
    </div>
  )
}

function SettingsModal({ adminToken, onClose }) {
  const [cfg, setCfg]       = useState(null)
  const [busy, setBusy]     = useState(false)
  const [error, setError]   = useState(null)
  const [saved, setSaved]   = useState(false)
  const [showPicker, setShowPicker] = useState(false)
  const [isWindows, setIsWindows]   = useState(false)
  const [autostart, setAutostart]   = useState(false)
  const [updateInfo, setUpdateInfo] = useState(null)
  const [updateBusy, setUpdateBusy] = useState(false)

  useEffect(() => {
    fetch('/api/settings', { credentials: 'same-origin' })
      .then(r => r.ok ? r.json() : Promise.reject())
      .then(setCfg)
      .catch(() => setError('Failed to load settings'))
  }, [])

  useEffect(() => {
    fetch('/api/platform').then(r => r.json()).then(d => {
      setIsWindows(!!d.windows)
      if (d.windows) {
        fetch('/api/autostart').then(r => r.ok ? r.json() : null)
          .then(d => d && setAutostart(!!d.enabled)).catch(() => {})
      }
    }).catch(() => {})
  }, [])

  const toggleAutostart = async (enabled) => {
    setAutostart(enabled)
    await adminFetch('/api/autostart', null, {
      method: 'POST', headers: {'Content-Type':'application/json'},
      body: JSON.stringify({ enabled })
    })
  }

  const checkUpdate = async () => {
    setUpdateBusy(true)
    try {
      const r = await fetch('/api/update/check')
      setUpdateInfo(await r.json())
    } catch { setUpdateInfo({ error: 'Update check failed' }) }
    setUpdateBusy(false)
  }

  const runUpdate = async () => {
    if (!window.confirm('Download and install the update now? PhotoShare will close and the installer will open.')) return
    setUpdateBusy(true)
    await adminFetch('/api/update/run', null, { method: 'POST' })
  }

  const save = async () => {
    setBusy(true); setError(null)
    const r = await adminFetch('/api/settings', null, {
      method: 'POST', headers: {'Content-Type':'application/json'},
      body: JSON.stringify(cfg)
    })
    if (!r.ok) { setError('Save failed — ' + await r.text()); setBusy(false); return }
    setSaved(true)
  }

  const set = (k, v) => setCfg(c => ({...c, [k]: v}))

  if (saved) return (
    <div className="adm-overlay" onClick={onClose}>
      <div className="adm-modal" onClick={e => e.stopPropagation()}>
        <div className="adm-modal-icon"><CheckIcon size={30} /></div>
        <h2 className="adm-modal-title">Settings Saved</h2>
        <p className="adm-modal-sub">The app is restarting with the new settings.<br/>Reconnect in a few seconds.</p>
      </div>
    </div>
  )

  return (
    <div className="adm-overlay" onClick={onClose}>
      <div className="adm-modal settings-modal" onClick={e => e.stopPropagation()}>
        <div className="adm-modal-icon"><GearIcon size={30} /></div>
        <h2 className="adm-modal-title">Settings</h2>

        {cfg && (
          <>
            {cfg.usingDefaultPassword && (
              <p className="adm-warn" style={{marginBottom:10}}>
                ⚠ This admin account is still using a default password — change it below in Users.
              </p>
            )}
            <UsersManager />
            <div className="settings-grid">
              <label className="settings-label">
                <span className="settings-label-head"><FolderIcon size={13} /> Photos Folder</span>
                <div style={{display:'flex', gap:8}}>
                  <input className="adm-input" value={cfg.photoDir||''} onChange={e => set('photoDir', e.target.value)} placeholder="/photos" />
                  <button className="adm-btn" type="button" onClick={() => setShowPicker(true)}>Browse…</button>
                </div>
              </label>
              <label className="settings-label">
                <span className="settings-label-head"><PlugIcon size={13} /> Port</span>
                <input className="adm-input" value={cfg.port||''} onChange={e => set('port', e.target.value)} placeholder="8080" style={{width:90}} />
              </label>
              <label className="settings-label">
                <span className="settings-label-head"><GlobeIcon size={13} /> Server IP (optional)</span>
                <input className="adm-input" value={cfg.serverIP||''} onChange={e => set('serverIP', e.target.value)} placeholder="10.0.0.20" />
              </label>
              <label className="settings-label">
                <span className="settings-label-head"><OpenFolderIcon size={13} /> SMB Share Name (optional)</span>
                <input className="adm-input" value={cfg.shareName||''} onChange={e => set('shareName', e.target.value)} placeholder="memories" />
              </label>
              <label className="settings-label">
                <span className="settings-label-head"><UploadIcon size={13} /> Upload Inbox Folder</span>
                <input className="adm-input" value={cfg.uploadFolder||''} onChange={e => set('uploadFolder', e.target.value)} placeholder="_Uploads" />
              </label>
              <label className="settings-label">
                <span className="settings-label-head"><FilmIcon size={13} /> FFmpeg Path (optional)</span>
                <input className="adm-input" value={cfg.ffmpegPath||''} onChange={e => set('ffmpegPath', e.target.value)} placeholder="/usr/bin/ffmpeg (auto-detected)" />
              </label>
              <label className="settings-label settings-check">
                <input type="checkbox" checked={!cfg.httpOnly} onChange={e => set('httpOnly', !e.target.checked)} />
                <span className="settings-label-head"><LockIcon size={13} /> Use HTTPS (uncheck to drop the “Not Secure” warning on your LAN)</span>
              </label>
              <label className="settings-label settings-check">
                <input type="checkbox" checked={!!cfg.autoSortUploads} onChange={e => set('autoSortUploads', e.target.checked)} />
                <span className="settings-label-head"><CalendarIcon size={13} /> Auto-sort uploads into Year/Month folders by date</span>
              </label>
              {isWindows && (
                <label className="settings-label settings-check">
                  <input type="checkbox" checked={autostart} onChange={e => toggleAutostart(e.target.checked)} />
                  <span className="settings-label-head">Start PhotoShare when Windows starts</span>
                </label>
              )}
            </div>

            {isWindows && (
              <div className="settings-grid" style={{marginTop:10}}>
                <div className="settings-label">
                  <span className="settings-label-head">Updates</span>
                  {!updateInfo && (
                    <button className="adm-btn" type="button" onClick={checkUpdate} disabled={updateBusy}>
                      {updateBusy ? 'Checking…' : 'Check for updates'}
                    </button>
                  )}
                  {updateInfo && updateInfo.error && <p className="adm-error">{updateInfo.error}</p>}
                  {updateInfo && !updateInfo.error && !updateInfo.available && (
                    <p className="adm-modal-sub">You're up to date (v{updateInfo.current}).</p>
                  )}
                  {updateInfo && updateInfo.available && (
                    <div>
                      <p className="adm-modal-sub">v{updateInfo.latest} is available (you have v{updateInfo.current}).</p>
                      <button className="adm-btn adm-btn-primary" type="button" onClick={runUpdate} disabled={updateBusy}>
                        {updateBusy ? 'Updating…' : 'Download & install'}
                      </button>
                    </div>
                  )}
                </div>
              </div>
            )}

            {error && <div className="adm-error" style={{marginTop:8}}>{error}</div>}
            <p className="adm-warn" style={{marginTop:10}}>⚠ Saving will restart the app.</p>

            <div className="adm-btns" style={{marginTop:12}}>
              <button className="adm-btn" onClick={onClose}>Cancel</button>
              <button className="adm-btn adm-btn-primary" onClick={save} disabled={busy}>
                {busy ? 'Saving…' : <><SaveIcon size={14} /> Save &amp; Restart</>}
              </button>
            </div>

            <p className="settings-version">PhotoShare v{APP_VERSION}</p>
            <a
              className="settings-credit"
              href="https://github.com/jpinela24"
              target="_blank"
              rel="noopener noreferrer"
            >
              Made by jpinela24 on GitHub
            </a>
          </>
        )}
      </div>
      {showPicker && (
        <LibraryPathPicker
          onClose={() => setShowPicker(false)}
          onConfirm={p => { set('photoDir', p); setShowPicker(false) }}
        />
      )}
    </div>
  )
}

// ── FolderPicker — choose a destination folder ───────────────────────────────

function FolderPickerNode({ path, label, onPick, selected }) {
  const [open, setOpen]         = useState(path === null)
  const [children, setChildren] = useState(null)
  const isRoot = path === null

  useEffect(() => {
    if (!open) return
    fetch(`/api/browse?path=${encodeURIComponent(isRoot ? '' : path)}`)
      .then(r => r.json())
      .then(data => setChildren((data || []).filter(e => e.isDir)))
      .catch(() => setChildren([]))
  }, [open])

  return (
    <div className="fp-node">
      <div className={`fp-row ${selected === path && !isRoot ? 'fp-active' : ''}`}>
        <button className="fp-toggle" onClick={() => setOpen(v => !v)}>
          {open ? <ChevronDown /> : <ChevronRight />}
        </button>
        <span className="fp-label" onClick={() => !isRoot && onPick(path)}>{label}</span>
        {!isRoot && (
          <button className="fp-pick-btn" onClick={() => onPick(path)}>Select</button>
        )}
      </div>
      {open && children && (
        <div className="fp-children">
          {children.map(c => (
            <FolderPickerNode key={c.path} path={c.path} label={c.name} onPick={onPick} selected={selected} />
          ))}
        </div>
      )}
    </div>
  )
}

// ── LibraryPathPicker — choose the library root itself (any OS path) ────────

function FsPickerNode({ path, label, onPick, selected }) {
  const [open, setOpen]         = useState(false)
  const [children, setChildren] = useState(null)

  useEffect(() => {
    if (!open) return
    fetch(`/api/fs/browse?path=${encodeURIComponent(path)}`)
      .then(r => r.json())
      .then(data => setChildren((data || []).filter(e => e.isDir)))
      .catch(() => setChildren([]))
  }, [open])

  return (
    <div className="fp-node">
      <div className={`fp-row ${selected === path ? 'fp-active' : ''}`}>
        <button className="fp-toggle" onClick={() => setOpen(v => !v)}>
          {open ? <ChevronDown /> : <ChevronRight />}
        </button>
        <span className="fp-label" onClick={() => onPick(path)}>{label}</span>
        <button className="fp-pick-btn" onClick={() => onPick(path)}>Select</button>
      </div>
      {open && children && (
        <div className="fp-children">
          {children.map(c => (
            <FsPickerNode key={c.path} path={c.path} label={c.name} onPick={onPick} selected={selected} />
          ))}
        </div>
      )}
    </div>
  )
}

function LibraryPathPicker({ onConfirm, onClose }) {
  const [roots, setRoots] = useState(null)
  const [dest, setDest]   = useState(null)

  useEffect(() => {
    fetch('/api/fs/roots').then(r => r.json()).then(setRoots).catch(() => setRoots([]))
  }, [])

  return (
    <div className="adm-overlay" onClick={onClose}>
      <div className="adm-modal fp-modal" onClick={e => e.stopPropagation()}>
        <div className="adm-modal-icon"><OpenFolderIcon size={30} /></div>
        <h2 className="adm-modal-title">Choose Photo Library Folder</h2>
        <div className="fp-tree">
          {roots === null && <p className="adm-modal-sub">Loading drives…</p>}
          {roots && roots.map(r => (
            <FsPickerNode key={r.path} path={r.path} label={r.name} onPick={setDest} selected={dest} />
          ))}
        </div>
        {dest !== null && (
          <p className="adm-modal-sub" style={{marginTop:6}}>
            Selected: <strong style={{color:'#a5b4fc'}}>{dest}</strong>
          </p>
        )}
        <div className="adm-btns" style={{marginTop:12}}>
          <button className="adm-btn" onClick={onClose}>Cancel</button>
          <button className="adm-btn adm-btn-primary" disabled={dest === null} onClick={() => onConfirm(dest)}>
            Confirm
          </button>
        </div>
      </div>
    </div>
  )
}

function FolderPicker({ title, onConfirm, onClose }) {
  const [dest, setDest] = useState(null)
  return (
    <div className="adm-overlay" onClick={onClose}>
      <div className="adm-modal fp-modal" onClick={e => e.stopPropagation()}>
        <div className="adm-modal-icon"><OpenFolderIcon size={30} /></div>
        <h2 className="adm-modal-title">{title}</h2>
        <div className="fp-tree">
          <FolderPickerNode path={null} label={<><LibraryIcon size={13} /> Root (All Photos)</>} onPick={setDest} selected={dest} />
        </div>
        {dest !== null && (
          <p className="adm-modal-sub" style={{marginTop:6}}>
            Destination: <strong style={{color:'#a5b4fc'}}>{dest || '/ root'}</strong>
          </p>
        )}
        <div className="adm-btns" style={{marginTop:12}}>
          <button className="adm-btn" onClick={onClose}>Cancel</button>
          <button className="adm-btn adm-btn-primary" disabled={dest === null} onClick={() => onConfirm(dest)}>
            Confirm
          </button>
        </div>
      </div>
    </div>
  )
}

// ── AddressBar ────────────────────────────────────────────────────────────────

function AddressBar({ path, onNavigate }) {
  if (path === TRASH_PATH) {
    return <div className="address-bar"><span className="address-display"><TrashIcon size={14} /> Recycle Bin</span></div>
  }
  if (path === DUPES_PATH) {
    return <div className="address-bar"><span className="address-display"><CopyIcon size={14} /> Duplicate Finder</span></div>
  }
  if (path === MEMORIES_PATH) {
    return <div className="address-bar"><span className="address-display"><SparkleIcon size={14} /> On This Day</span></div>
  }
  if (path === MAP_PATH) {
    return <div className="address-bar"><span className="address-display"><MapPinIcon size={14} /> Map</span></div>
  }

  const parts = path ? path.split('/') : []
  const segs = [
    { label: 'All Photos', path: '' },
    ...parts.map((p, i) => ({ label: p, path: parts.slice(0, i + 1).join('/') }))
  ]

  return (
    <div className="address-bar">
      <div className="address-crumbs">
        {segs.map((seg, i) => {
          const isLast = i === segs.length - 1
          return (
            <span key={seg.path} className="address-crumb-item">
              {i > 0 && <span className="address-crumb-sep">/</span>}
              {isLast ? (
                <span className="address-crumb-current">{seg.label}</span>
              ) : (
                <button className="address-crumb-btn" onClick={() => onNavigate(seg.path)}>
                  {seg.label}
                </button>
              )}
            </span>
          )
        })}
      </div>
    </div>
  )
}

// VirtualGrid removed — using CSS content-visibility instead

const APP_VERSION = '2.3'

// ── Theme (client-only preference: 'dark' | 'light' | 'auto') ─────────────────
function prefersDark() {
  try { return window.matchMedia('(prefers-color-scheme: dark)').matches } catch { return true }
}
function resolveTheme(pref) {
  if (pref === 'auto') return prefersDark() ? 'dark' : 'light'
  return pref === 'light' ? 'light' : 'dark'
}
function applyTheme(pref) {
  try { localStorage.setItem('ps-theme', pref) } catch {}
  if (typeof document !== 'undefined') document.documentElement.dataset.theme = resolveTheme(pref)
}
function currentTheme() {
  try { return localStorage.getItem('ps-theme') || 'dark' } catch { return 'dark' }
}
// Apply saved theme immediately at module load; track OS changes while in auto.
if (typeof document !== 'undefined') {
  applyTheme(currentTheme())
  try {
    window.matchMedia('(prefers-color-scheme: dark)').addEventListener('change', () => {
      if (currentTheme() === 'auto') applyTheme('auto')
    })
  } catch {}
}

// ── Login page (full-screen gate) ──────────────────────────────────────────────
// ── OnboardingPage — first-run setup: pick a library folder + create admin ──

function OnboardingPage() {
  const [photoDir, setPhotoDir]     = useState('')
  const [username, setUsername]     = useState('admin')
  const [password, setPassword]     = useState('')
  const [showPicker, setShowPicker] = useState(false)
  const [busy, setBusy]             = useState(false)
  const [error, setError]           = useState(null)
  const [done, setDone]             = useState(false)

  const submit = async () => {
    setBusy(true); setError(null)
    try {
      const r = await fetch('/api/onboarding', {
        method: 'POST', headers: {'Content-Type':'application/json'},
        body: JSON.stringify({ photoDir, username, password })
      })
      if (!r.ok) { setError(await r.text()); setBusy(false); return }
      setDone(true)
    } catch {
      setError('Setup failed'); setBusy(false)
    }
  }

  if (done) {
    return (
      <div className="login-loading">
        <div className="spinner" />
        <p style={{marginTop:12, color:'#a5b4fc'}}>Setting up PhotoShare — reconnecting…</p>
      </div>
    )
  }

  return (
    <div className="adm-overlay" style={{position:'fixed'}}>
      <div className="adm-modal settings-modal" onClick={e => e.stopPropagation()}>
        <div className="adm-modal-icon"><LibraryIcon size={30} /></div>
        <h2 className="adm-modal-title">Welcome to PhotoShare</h2>
        <p className="adm-modal-sub">Pick your photo library folder and create the admin account.</p>
        <div className="settings-grid">
          <label className="settings-label">
            <span className="settings-label-head"><FolderIcon size={13} /> Photos Folder</span>
            <div style={{display:'flex', gap:8}}>
              <input className="adm-input" value={photoDir} onChange={e => setPhotoDir(e.target.value)} placeholder={'C:\\Photos'} />
              <button className="adm-btn" type="button" onClick={() => setShowPicker(true)}>Browse…</button>
            </div>
          </label>
          <label className="settings-label">
            <span className="settings-label-head">Admin Username</span>
            <input className="adm-input" value={username} onChange={e => setUsername(e.target.value)} />
          </label>
          <label className="settings-label">
            <span className="settings-label-head">Admin Password</span>
            <input className="adm-input" type="password" value={password} onChange={e => setPassword(e.target.value)} />
          </label>
        </div>
        {error && <div className="adm-error" style={{marginTop:8}}>{error}</div>}
        <div className="adm-btns" style={{marginTop:12}}>
          <button
            className="adm-btn adm-btn-primary"
            disabled={busy || !photoDir || !username || !password}
            onClick={submit}
          >
            {busy ? 'Setting up…' : 'Finish Setup'}
          </button>
        </div>
      </div>
      {showPicker && (
        <LibraryPathPicker
          onClose={() => setShowPicker(false)}
          onConfirm={p => { setPhotoDir(p); setShowPicker(false) }}
        />
      )}
    </div>
  )
}

function LoginPage({ guestAccess, onLogin }) {
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [error, setError]       = useState(null)
  const [busy, setBusy]         = useState(false)

  const doLogin = async (payload) => {
    setBusy(true); setError(null)
    try {
      const r = await fetch('/api/login', {
        method: 'POST',
        credentials: 'same-origin',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(payload),
      })
      if (!r.ok) {
        setError(r.status === 429 ? 'Too many attempts — wait a minute' : 'Wrong username or password')
        setBusy(false)
        return
      }
      await onLogin()
    } catch {
      setError('Could not reach the server')
      setBusy(false)
    }
  }

  return (
    <div className="login-page">
      <form className="login-card" onSubmit={e => { e.preventDefault(); doLogin({ username, password }) }}>
        <div className="login-logo"><CameraIcon size={40} /></div>
        <h1 className="login-title">PhotoShare</h1>
        <p className="login-sub">Sign in to continue</p>
        <input className="adm-input" placeholder="Username" autoFocus autoComplete="username"
          value={username} onChange={e => setUsername(e.target.value)} />
        <input className="adm-input" type="password" placeholder="Password" autoComplete="current-password"
          value={password} onChange={e => setPassword(e.target.value)} />
        {error && <div className="adm-error">{error}</div>}
        <button className="adm-btn adm-btn-primary login-submit" type="submit" disabled={busy}>
          {busy ? 'Signing in…' : 'Sign In'}
        </button>
        {guestAccess && (
          <button className="login-guest" type="button" disabled={busy} onClick={() => doLogin({ guest: true })}>
            Continue as guest
          </button>
        )}
      </form>
    </div>
  )
}

// ── Main App ──────────────────────────────────────────────────────────────────

export default function App() {
  const [path, setPath]               = useState('')
  const [history, setHistory]         = useState([''])
  const [histIdx, setHistIdx]         = useState(0)
  const [entries, setEntries]         = useState([])
  const [loading, setLoading]         = useState(true)
  const [error, setError]             = useState(null)
  const [selected, setSelected]       = useState(null)
  const [sidebarOpen, setSidebarOpen] = useState(false)
  const dragOpenedSidebar = useRef(false) // sidebar auto-opened during a drag
  const [theme, setTheme] = useState(currentTheme())
  const toggleTheme = () => {
    const next = theme === 'dark' ? 'light' : theme === 'light' ? 'auto' : 'dark'
    applyTheme(next); setTheme(next)
  }
  const [showQR, setShowQR] = useState(false)
  const [showShortcuts, setShowShortcuts] = useState(false)
  const [playingPath, setPlayingPath] = useState(null)
  // First-run check: true if no photo library path has been configured yet
  // (a fresh Windows install before onboarding completes). null = loading.
  const [needsSetup, setNeedsSetup] = useState(null)
  useEffect(() => {
    fetch('/api/onboarding-status')
      .then(r => r.json())
      .then(d => setNeedsSetup(!!d.needsSetup))
      .catch(() => setNeedsSetup(false))
  }, [])

  // Auth: me = null (loading) | { authenticated, role, username, guestAccess, isGuest }
  const [me, setMe] = useState(null)
  const loadMe = useCallback(async () => {
    try {
      const r = await fetch('/api/me', { credentials: 'same-origin' })
      const d = await r.json()
      setMe(d)
    } catch { setMe({ authenticated: false, guestAccess: false }) }
  }, [])
  useEffect(() => { if (needsSetup === false) loadMe() }, [needsSetup, loadMe])
  // adminToken is a truthy sentinel when the logged-in user is an admin, so all
  // existing `adminToken ? …` gates and adminFetch(url, adminToken) calls work.
  const adminToken = (me && me.authenticated && me.role === 'admin') ? 'admin' : null
  const [deleteTarget, setDeleteTarget]   = useState(null)
  const [selectMode, setSelectMode]       = useState(false)
  const [selItems, setSelItems]           = useState(new Set())
  const [sortBy, setSortBy]               = useState('name')   // name | date | size | type
  const [sortDir, setSortDir]             = useState('asc')
  const [gridSize, setGridSize]           = useState('medium') // small | medium | large
  const [pickerAction, setPickerAction]           = useState(null)
  const [dropZone, setDropZone]                   = useState(false)
  const [uploading, setUploading]                 = useState(false)
  const [uploadStatus, setUploadStatus]           = useState(null)
  const [uploadPendingFiles, setUploadPendingFiles] = useState(null)
  const [showRename, setShowRename]               = useState(false)
  const [showStats, setShowStats]                 = useState(false)
  const [showSettings, setShowSettings]           = useState(false)
  const [pregenStatus, setPregenStatus]           = useState(null)

  // Poll thumbnail pre-generation progress
  useEffect(() => {
    const poll = () => {
      fetch('/api/thumbs/status').then(r => r.json())
        .then(s => { setPregenStatus(s); if (!s.running) clearInterval(iv) })
        .catch(() => {})
    }
    poll()
    const iv = setInterval(poll, 3000)
    return () => clearInterval(iv)
  }, [])
  const [uploadFolderName, setUploadFolderName]   = useState('_Uploads')
  const mainScrollRef = useRef(null)
  const [gridFocus, setGridFocus]                 = useState(null) // index into sortedEntries

  useEffect(() => {
    fetch('/api/server-info').then(r => r.json())
      .then(d => { if (d.uploadFolder) setUploadFolderName(d.uploadFolder) })
      .catch(() => {})
  }, [])
  const [batchStatus, setBatchStatus]     = useState(null) // success/error message

  const orderedSelRef = useRef([])  // selectable paths in display order (for range select)
  const lastSelRef    = useRef(null) // last single-toggled path (range anchor)
  const toggleSelect = (path, shift) => {
    setSelItems(prev => {
      const next = new Set(prev)
      if (shift && lastSelRef.current) {
        const order = orderedSelRef.current
        const a = order.indexOf(lastSelRef.current)
        const b = order.indexOf(path)
        if (a !== -1 && b !== -1) {
          const [lo, hi] = a < b ? [a, b] : [b, a]
          for (let i = lo; i <= hi; i++) next.add(order[i])
          return next
        }
      }
      next.has(path) ? next.delete(path) : next.add(path)
      return next
    })
    lastSelRef.current = path
  }
  const clearSelect = () => { setSelItems(new Set()); setSelectMode(false); lastSelRef.current = null }
  const selectAll   = () => setSelItems(new Set(entries.filter(e => !e.isDir).map(e => e.path)))

  // ── Marquee (drag-rectangle) select ──
  const [marquee, setMarquee] = useState(null) // {x0,y0,x1,y1} in client coords
  const marqueeRef = useRef(null)
  useEffect(() => { marqueeRef.current = marquee }, [marquee])
  const onGridMouseDown = (e) => {
    if (!selectMode || e.button !== 0) return
    // Only in the normal photo grid — never over Map/Memories/Trash/Duplicates.
    if (path === MAP_PATH || path === MEMORIES_PATH || path === TRASH_PATH || path === DUPES_PATH) return
    if (e.target.closest('.card, button, a, input, select, label, .sel-bar')) return // empty space only
    setMarquee({ x0: e.clientX, y0: e.clientY, x1: e.clientX, y1: e.clientY })
  }
  useEffect(() => {
    if (!marquee) return
    const onMove = (e) => setMarquee(m => m && { ...m, x1: e.clientX, y1: e.clientY })
    const onUp = () => {
      const m = marqueeRef.current
      if (m) {
        const left = Math.min(m.x0, m.x1), right = Math.max(m.x0, m.x1)
        const top = Math.min(m.y0, m.y1), bottom = Math.max(m.y0, m.y1)
        if (right - left > 4 || bottom - top > 4) { // ignore tiny drags
          const add = []
          document.querySelectorAll('[data-sel-path]').forEach(el => {
            const r = el.getBoundingClientRect()
            if (r.left < right && r.right > left && r.top < bottom && r.bottom > top) {
              add.push(el.getAttribute('data-sel-path'))
            }
          })
          if (add.length) setSelItems(prev => { const n = new Set(prev); add.forEach(p => n.add(p)); return n })
        }
      }
      setMarquee(null)
    }
    window.addEventListener('mousemove', onMove)
    window.addEventListener('mouseup', onUp)
    return () => { window.removeEventListener('mousemove', onMove); window.removeEventListener('mouseup', onUp) }
  }, [marquee !== null])

  useEffect(() => {
    if (!me?.authenticated) return // wait until logged in (re-runs when auth flips)
    setLoading(true)
    setError(null)
    setSelected(null)
    setPlayingPath(null)
    setSelItems(new Set())
    setSelectMode(false)
    setGridFocus(null)
    if (path === TRASH_PATH || path === DUPES_PATH || path === MEMORIES_PATH || path === MAP_PATH) { setLoading(false); return }
    fetch(`/api/browse?path=${encodeURIComponent(path)}`)
      .then(r => { if (!r.ok) throw new Error(`Server error ${r.status}`); return r.json() })
      .then(data => { setEntries(data || []); setLoading(false) })
      .catch(err => { setError(err.message); setLoading(false) })
  }, [path, me?.authenticated])

  const navigate = useCallback((newPath) => {
    setPath(newPath)
    setHistory(prev => {
      const trimmed = prev.slice(0, histIdx + 1)
      return [...trimmed, newPath]
    })
    setHistIdx(prev => prev + 1)
  }, [histIdx])

  const goBack = () => {
    if (histIdx <= 0) return
    const newIdx = histIdx - 1
    setHistIdx(newIdx)
    setPath(history[newIdx])
  }

  const goForward = () => {
    if (histIdx >= history.length - 1) return
    const newIdx = histIdx + 1
    setHistIdx(newIdx)
    setPath(history[newIdx])
  }

  const canBack    = histIdx > 0
  const canForward = histIdx < history.length - 1

  // ── Batch actions (admin only; the buttons are hidden for non-admins) ──
  const executeBatch = async (action, destFolder) => {
    const paths = [...selItems]
    const url = `/api/admin/batch/${action}`
    const r = await adminFetch(url, null, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ paths, destFolder: destFolder ?? '' })
    })
    const data = await r.json()
    const errCount = data.errors?.length || 0
    if (action === 'delete') setEntries(prev => prev.filter(e => !selItems.has(e.path)))
    clearSelect()
    setBatchStatus(errCount === 0
      ? `✓ ${paths.length} item${paths.length !== 1 ? 's' : ''} ${action === 'delete' ? 'moved to trash' : action === 'copy' ? 'copied' : 'moved'}`
      : `Done with ${errCount} error${errCount !== 1 ? 's' : ''}`)
    setTimeout(() => setBatchStatus(null), 3000)
  }

  const batchAction = (action, destFolder) => {
    if (adminToken) executeBatch(action, destFolder)
  }

  const uploadFiles = async (files) => {
    if (!files.length) return
    setUploading(true)
    setUploadStatus(`Uploading ${files.length} file${files.length !== 1 ? 's' : ''}…`)
    const form = new FormData()
    Array.from(files).forEach(f => form.append('files', f))
    // Always upload to the inbox folder — no auth required
    const r = await fetch('/api/inbox-upload', { method: 'POST', body: form })
    const d = await r.json()
    setUploading(false)
    const skippedMsg = d.skipped?.length ? ` · ${d.skipped.length} skipped (not a photo/video)` : ''
    setUploadStatus(`✓ ${d.uploaded} file${d.uploaded !== 1 ? 's' : ''} uploaded${skippedMsg}`)
    setTimeout(() => setUploadStatus(null), 3000)
    if (path === uploadFolderName) setPath(p => p) // refresh if already in inbox
  }

  const handleUploadDrop = (files) => {
    setDropZone(false)
    uploadFiles(files)
  }

  const handleFileMoved = useCallback((filePath) => {
    setEntries(prev => prev.filter(e => e.path !== filePath))
    setSelItems(prev => { const n = new Set(prev); n.delete(filePath); return n })
  }, [])

  const handleLogout = async () => {
    await fetch('/api/logout', { method: 'POST', credentials: 'same-origin' }).catch(() => {})
    setMe({ authenticated: false, guestAccess: me?.guestAccess })
  }

  const handleDeleteRequest = (entry) => setDeleteTarget(entry)

  const handleDeleteConfirm = async () => {
    if (!deleteTarget) return
    await fetch(`/api/admin/delete?path=${encodeURIComponent(deleteTarget.path)}`, {
      method: 'DELETE',
      credentials: 'same-origin',
    })
    setDeleteTarget(null)
    // Refresh current folder
    setEntries(prev => prev.filter(e => e.path !== deleteTarget.path))
  }


  const sortedEntries = [...entries].sort((a, b) => {
    if (a.isDir !== b.isDir) return a.isDir ? -1 : 1
    const dir = sortDir === 'asc' ? 1 : -1
    switch (sortBy) {
      case 'date': return (a.mod - b.mod) * dir
      case 'size': return (a.size - b.size) * dir
      case 'type': return ((a.isVideo ? 1 : 0) - (b.isVideo ? 1 : 0)) * dir
      default:     return a.name.localeCompare(b.name) * dir
    }
  })
  // Keep the selectable paths (display order) current for shift-range selection.
  orderedSelRef.current = sortedEntries.filter(e => !e.isDir).map(e => e.path)

  const media = entries.filter(e => !e.isDir)
  const photoIndex = selected ? media.findIndex(e => e.path === selected.path) : -1

  const closeModal  = () => setSelected(null)
  const showPrev    = () => photoIndex > 0 && setSelected(media[photoIndex - 1])
  const showNext    = () => photoIndex < media.length - 1 && setSelected(media[photoIndex + 1])

  const handleKey = (e) => {
    const tag = document.activeElement?.tagName
    const inInput = tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT'

    // Keyboard-shortcuts overlay (? toggles, Esc closes)
    if (!inInput && (e.key === '?' || (e.shiftKey && e.key === '/'))) { e.preventDefault(); setShowShortcuts(v => !v); return }
    if (showShortcuts && e.key === 'Escape') { setShowShortcuts(false); return }

    // Modal navigation
    if (selected) {
      if (e.key === 'Escape')                    { closeModal(); return }
      if (e.key === 'ArrowLeft'  && !e.altKey)   { showPrev(); return }
      if (e.key === 'ArrowRight' && !e.altKey)   { showNext(); return }
    }
    // Folder history
    if (e.altKey && e.key === 'ArrowLeft')  { e.preventDefault(); goBack(); return }
    if (e.altKey && e.key === 'ArrowRight') { e.preventDefault(); goForward(); return }

    // Grid keyboard navigation (only when no modal, no input focused)
    if (!selected && !inInput && !e.altKey && sortedEntries.length > 0) {
      const cur = gridFocus ?? -1
      const total = sortedEntries.length
      if (e.key === 'ArrowRight' || e.key === 'ArrowDown') {
        e.preventDefault()
        setGridFocus(Math.min(cur + 1, total - 1))
      } else if (e.key === 'ArrowLeft' || e.key === 'ArrowUp') {
        e.preventDefault()
        setGridFocus(Math.max(cur - 1, 0))
      } else if ((e.key === 'Enter' || e.key === ' ') && gridFocus !== null) {
        e.preventDefault()
        const entry = sortedEntries[gridFocus]
        if (entry.isDir) { navigate(entry.path); setGridFocus(null) }
        else setSelected(entry)
      } else if (e.key === 'Escape') {
        setGridFocus(null)
      }
    }
  }


  // ── Setup/auth gate: nothing renders until the library is configured and
  // the user is logged in ──
  if (needsSetup === null || (needsSetup === false && me === null)) {
    return <div className="login-loading"><div className="spinner" /></div>
  }
  if (needsSetup) {
    return <OnboardingPage />
  }
  if (!me.authenticated) {
    return <LoginPage guestAccess={me.guestAccess} onLogin={loadMe} />
  }

  return (
    <AdminCtx.Provider value={{ token: adminToken, onDeleteRequest: handleDeleteRequest }}>
    <SelectCtx.Provider value={{ selectMode, selected: selItems, toggleSelect }}>
    <div
      className="app-shell"
      onKeyDown={handleKey}
      tabIndex={-1}
      onDragStart={e => {
        // Reveal the sidebar (folder tree) when an internal item drag begins,
        // so it can be used as a drop target.
        if (e.dataTransfer.types.includes('application/photo-share') && !sidebarOpen) {
          setSidebarOpen(true)
          dragOpenedSidebar.current = true
        }
      }}
      onDragEnd={() => {
        // Re-hide only if the drag is what opened it.
        if (dragOpenedSidebar.current) {
          setSidebarOpen(false)
          dragOpenedSidebar.current = false
        }
      }}
    >

      {/* ── Top bar ── */}
      <header className="topbar">
        <button className="menu-btn" onClick={() => setSidebarOpen(v => !v)} title="Toggle sidebar">
          <MenuIcon />
        </button>
        <span className="logo">PhotoShare</span>
        <div className="nav-btns">
          <button className="nav-arrow" onClick={goBack}    disabled={!canBack}    title="Back (Alt+Left)">‹</button>
          <button className="nav-arrow" onClick={goForward} disabled={!canForward} title="Forward (Alt+Right)">›</button>
        </div>

        <button className="theme-btn" onClick={toggleTheme}
          title={`Theme: ${theme} (click to change)`}>
          {theme === 'dark' ? <MoonIcon size={16} /> : theme === 'light' ? <SunIcon size={16} /> : <MonitorIcon size={16} />}
        </button>

        <button className="theme-btn" onClick={() => setShowQR(true)} title="Connect a phone (QR code)">
          <QrIcon size={16} />
        </button>

        {/* Address bar */}
        <AddressBar path={path} onNavigate={navigate} />

        {/* Pre-gen progress */}
        {pregenStatus?.running && (
          <div className="pregen-progress" title={`Generating thumbnails: ${pregenStatus.done}/${pregenStatus.total}`}>
            <div className="pregen-bar" style={{width: `${Math.round(pregenStatus.done/pregenStatus.total*100)}%`}} />
            <span className="pregen-label"><GearIcon size={11} /> {Math.round(pregenStatus.done/pregenStatus.total*100)}%</span>
          </div>
        )}

        {/* Toolbar — integrated into topbar */}
        {path !== TRASH_PATH && path !== DUPES_PATH && path !== MEMORIES_PATH && path !== MAP_PATH && (
          <div className="topbar-toolbar">
            {adminToken && (
              <button
                className={`select-toggle ${selectMode ? 'select-toggle-active' : ''}`}
                onClick={() => { setSelectMode(v => !v); setSelItems(new Set()) }}
              >
                {selectMode ? <CloseIcon size={15} /> : <SquareIcon size={15} />}
              </button>
            )}
            <div className="toolbar-group">
              <select className="sort-select" value={sortBy} onChange={e => setSortBy(e.target.value)}>
                <option value="name">Name</option>
                <option value="date">Date</option>
                <option value="size">Size</option>
                <option value="type">Type</option>
              </select>
              <button className="sort-dir-btn" onClick={() => setSortDir(d => d === 'asc' ? 'desc' : 'asc')}>
                {sortDir === 'asc' ? <ArrowUpIcon size={14} /> : <ArrowDownIcon size={14} />}
              </button>
            </div>
            <div className="toolbar-group">
              <button className={`grid-size-btn ${gridSize === 'small'  ? 'active' : ''}`} onClick={() => setGridSize('small')}  title="Small"><GridSmallIcon size={15} /></button>
              <button className={`grid-size-btn ${gridSize === 'medium' ? 'active' : ''}`} onClick={() => setGridSize('medium')} title="Medium"><GridMediumIcon size={15} /></button>
              <button className={`grid-size-btn ${gridSize === 'large'  ? 'active' : ''}`} onClick={() => setGridSize('large')}  title="Large"><SquareIcon size={15} /></button>
            </div>
            <label className="upload-btn" title="Upload files to inbox">
              <UploadIcon size={16} />
              <input type="file" multiple accept="image/*,video/*,.heic,.heif,.HEIC,.HEIF,.mov,.MOV,.mkv,.MKV,.avi,.wmv,.m4v,.flv,.ts,.webm,image/heic,image/heif,video/quicktime" style={{display:'none'}} onChange={e => { if (e.target.files.length) handleUploadDrop(e.target.files); e.target.value='' }} />
            </label>
          </div>
        )}

        <div className="topbar-right">
          <span className="user-chip" title={me.role === 'admin' ? 'Administrator' : 'View-only'}>
            {me.role === 'admin' ? <UnlockIcon size={13} /> : <LockIcon size={13} />}
            {me.username}
          </span>
          <button className="adm-topbtn" onClick={handleLogout} title="Log out"><LogoutIcon size={14} /></button>
        </div>

      </header>

      {/* ── Body ── */}
      <div className="app-body">

        {/* Dimmed scrim behind the slide-over sidebar */}
        {sidebarOpen && <div className="sidebar-scrim" onClick={() => setSidebarOpen(false)} />}

        {/* Sidebar */}
        <div className={`sidebar-wrap ${sidebarOpen ? 'sidebar-open' : 'sidebar-closed'}`}>
          <Sidebar currentPath={path} onNavigate={p => { navigate(p); setSidebarOpen(false) }} onFileMoved={handleFileMoved} onShowStats={() => { setShowStats(true); setSidebarOpen(false) }} onShowSettings={() => { setShowSettings(true); setSidebarOpen(false) }} uploadFolderName={uploadFolderName} />
        </div>

        {/* Main content */}
        <main
          className={`main ${marquee ? 'marquee-active' : ''}`}
          ref={mainScrollRef}
          onMouseDown={onGridMouseDown}
          onDragOver={e => { if (e.dataTransfer.types.includes('Files') && !e.dataTransfer.types.includes('application/photo-share')) { e.preventDefault(); setDropZone(true) } }}
          onDragLeave={e => { if (!e.currentTarget.contains(e.relatedTarget)) setDropZone(false) }}
          onDrop={e => { e.preventDefault(); if (e.dataTransfer.files.length) handleUploadDrop(e.dataTransfer.files) }}
        >
          {/* Special views */}
          {path === TRASH_PATH && <TrashView />}
          {path === DUPES_PATH && <DuplicatesView />}
          {path === MEMORIES_PATH && <MemoriesView onOpen={setSelected} />}
          {path === MAP_PATH && <MapView onOpen={setSelected} />}
          {path === TRASH_PATH || path === DUPES_PATH || path === MEMORIES_PATH || path === MAP_PATH ? null : (<>
          {/* Status messages */}
          {(batchStatus || uploadStatus) && (
            <div style={{padding:'6px 0'}}><span className="batch-status">{batchStatus || uploadStatus}</span></div>
          )}
          {loading && (
            <div className="status"><div className="spinner" /><span>Loading…</span></div>
          )}
          {error && <div className="status error">⚠ {error}</div>}
          {!loading && !error && entries.length === 0 && (
            <div className="status muted">No photos, videos or folders here.</div>
          )}
          {!loading && !error && entries.length > 0 && (
            <div className={`grid grid-${gridSize} grid-enter`} key={path}>
              {sortedEntries.map((entry, idx) => (
                entry.isVideo
                  ? <VideoCard key={entry.path} entry={entry} onOpenModal={setSelected} playingPath={playingPath} setPlayingPath={setPlayingPath} focused={gridFocus === idx} onFocus={() => setGridFocus(idx)} />
                  : entry.isDir
                  ? (
                    <div key={entry.path} role="button" tabIndex={0} className={`card card-folder ${gridFocus === idx ? 'card-grid-focus' : ''}`} onClick={() => { setGridFocus(idx); setPath(entry.path) }} onKeyDown={e => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); setGridFocus(idx); setPath(entry.path) } }} title={entry.name} style={{position:'relative'}}>
                      <TrashBtn entry={entry} />
                      <FolderThumb folderPath={entry.path} />
                      <div className="card-label">{entry.name}</div>
                    </div>
                  ) : (
                    <PhotoCard key={entry.path} entry={entry} onOpen={setSelected} adminToken={adminToken} selectMode={selectMode} isSelected={selItems.has(entry.path)} onToggle={toggleSelect} onDeleteRequest={handleDeleteRequest} focused={gridFocus === idx} onFocus={() => setGridFocus(idx)} selItems={selItems} />
                  )
              ))}
            </div>
          )}
          </>)}
        </main>
      </div>

      {/* ── Lightbox / video modal ── */}
      {selected && ReactDOM.createPortal((
        <div className="modal" onClick={closeModal}
          onTouchStart={e => { e._touchStart = { x: e.touches[0].clientX, y: e.touches[0].clientY } }}
          onTouchEnd={e => {
            const s = e._touchStart || e.currentTarget._touchStart
            if (!s) return
            const dx = e.changedTouches[0].clientX - s.x
            const dy = e.changedTouches[0].clientY - s.y
            if (Math.abs(dx) > Math.abs(dy) && Math.abs(dx) > 50) {
              e.stopPropagation()
              if (dx < 0) showNext(); else showPrev()
            } else if (dy > 80 && Math.abs(dy) > Math.abs(dx)) {
              closeModal()
            }
          }}
        >

          {/* Top bar */}
          <div className="modal-bar" onClick={e => e.stopPropagation()}>
            <span className="modal-name">{selected.name}</span>
            <div className="modal-actions">
              {/* Rotation — admin only, non-video, non-HEIC photos */}
              {adminToken && !selected.isVideo && !/\.(heic|heif)$/i.test(selected.name) && (
                <>
                  <button className="modal-btn" title="Rotate 90° left" onClick={async () => {
                    await adminFetch(`/api/admin/rotate?path=${encodeURIComponent(selected.path)}&angle=270`, null, { method:'POST' })
                    setSelected(s => ({...s, _v: Date.now()}))
                  }}><RotateCcwIcon size={14} /></button>
                  <button className="modal-btn" title="Rotate 90° right" onClick={async () => {
                    await adminFetch(`/api/admin/rotate?path=${encodeURIComponent(selected.path)}&angle=90`, null, { method:'POST' })
                    setSelected(s => ({...s, _v: Date.now()}))
                  }}><RotateCwIcon size={14} /></button>
                </>
              )}
              <a
                href={`/api/${selected.isVideo ? 'video' : 'photo'}?path=${encodeURIComponent(selected.path)}`}
                download={selected.name}
                className="modal-btn"
              ><DownloadIcon size={14} /> Download</a>
              {adminToken && (
                <button
                  className="modal-btn modal-btn-danger"
                  onClick={async () => {
                    const r = await adminFetch(`/api/admin/delete?path=${encodeURIComponent(selected.path)}`, null, { method: 'DELETE' })
                    if (r.ok) {
                      setEntries(prev => prev.filter(e => e.path !== selected.path))
                      closeModal()
                    }
                  }}
                ><TrashIcon size={14} /> Delete</button>
              )}
              <button className="modal-btn" onClick={closeModal}><CloseIcon size={14} /> Close</button>
            </div>
          </div>

          {/* Body: player + optional video playlist */}
          <div className="modal-body-wrap modal-has-playlist" onClick={e => e.stopPropagation()}>

            {/* Player area */}
            <div className="modal-player">
              {!selected.isVideo && photoIndex > 0 && (
                <button className="nav-btn nav-prev" onClick={showPrev}>‹</button>
              )}
              {selected.isVideo ? (
                <video
                  key={selected.path}
                  className="modal-video"
                  src={`/api/video?path=${encodeURIComponent(selected.path)}`}
                  controls autoPlay
                />
              ) : (
                <img
                  className="modal-img"
                  src={`/api/photo?path=${encodeURIComponent(selected.path)}${selected._v ? `&v=${selected._v}` : ''}`}
                  alt={selected.name}
                />
              )}
              {!selected.isVideo && photoIndex < media.length - 1 && (
                <button className="nav-btn nav-next" onClick={showNext}>›</button>
              )}
            </div>

            {/* Media strip — all photos & videos in this folder */}
            <div className="modal-playlist">
              <div className="playlist-header">
                In this folder
                <span className="playlist-count">{media.length}</span>
              </div>
              <div className="playlist-items">
                {media.map((item, i) => {
                  const active = item.path === selected.path
                  return (
                    <button
                      key={item.path}
                      className={`playlist-item ${active ? 'playlist-item-active' : ''}`}
                      onClick={() => setSelected(item)}
                    >
                      <div className="playlist-thumb-wrap">
                        <img
                          className="playlist-thumb"
                          src={`/api/thumb?path=${encodeURIComponent(item.path)}`}
                          alt={item.name}
                          loading="lazy"
                        />
                        {active && (
                          <div className="playlist-playing-badge">
                            {item.isVideo ? <PlayIcon size={11} /> : <ImageIcon size={11} />}
                          </div>
                        )}
                        {item.isVideo && !active && (
                          <div className="playlist-video-badge"><PlayIcon size={10} /></div>
                        )}
                      </div>
                      <div className="playlist-info">
                        <div className="playlist-name">{item.name}</div>
                        <div className="playlist-num">#{i + 1}</div>
                      </div>
                    </button>
                  )
                })}
              </div>
            </div>

          </div>
        </div>
      ), document.body)}

      {/* ── Admin modals ── */}
      {/* Drop zone overlay */}
      {dropZone && (
        <div className="upload-dropzone">
          <div className="upload-dropzone-inner">
            <div style={{display:'flex', justifyContent:'center'}}><UploadIcon size={44} /></div>
            <div>Drop files to upload to this folder</div>
          </div>
        </div>
      )}

      {/* Upload loading overlay */}
      {uploading && (
        <div className="upload-dropzone">
          <div className="upload-dropzone-inner">
            <div className="spinner" />
            <div>{uploadStatus}</div>
          </div>
        </div>
      )}


      {showRename && (
        <BatchRenameModal
          paths={[...selItems]}
          adminToken={adminToken}
          onClose={() => setShowRename(false)}
          onDone={(errs) => {
            setShowRename(false)
            clearSelect()
            setBatchStatus(errs === 0 ? '✓ Files renamed' : `Done with ${errs} error(s)`)
            setTimeout(() => setBatchStatus(null), 3000)
            setPath(p => p) // trigger refresh
          }}
        />
      )}
      {showStats && <StatsModal onClose={() => setShowStats(false)} />}
      {showSettings && <SettingsModal adminToken={adminToken} onClose={() => setShowSettings(false)} />}
      {showQR && <QRModal onClose={() => setShowQR(false)} />}
      {showShortcuts && <ShortcutsModal onClose={() => setShowShortcuts(false)} />}
      {marquee && (
        <div className="marquee" style={{
          left: Math.min(marquee.x0, marquee.x1),
          top: Math.min(marquee.y0, marquee.y1),
          width: Math.abs(marquee.x1 - marquee.x0),
          height: Math.abs(marquee.y1 - marquee.y0),
        }} />
      )}
      {deleteTarget && (
        <DeleteConfirm
          entry={deleteTarget}
          onConfirm={handleDeleteConfirm}
          onClose={() => setDeleteTarget(null)}
        />
      )}

      {/* ── Folder picker for copy/move ── */}
      {pickerAction && (
        <FolderPicker
          title={pickerAction === 'copy' ? 'Copy to…' : 'Move to…'}
          onConfirm={dest => { setPickerAction(null); batchAction(pickerAction, dest) }}
          onClose={() => setPickerAction(null)}
        />
      )}

      {/* ── Floating selection action bar ── */}
      {selectMode && (
        <div className="sel-bar">
          <button className="sel-bar-btn" onClick={clearSelect}><CloseIcon size={14} /></button>
          <span className="sel-count">{selItems.size} selected</span>
          <button className="sel-bar-btn" onClick={selectAll}>Select All</button>
          <div className="sel-bar-divider" />
          <button className="sel-bar-action" disabled={selItems.size === 0} onClick={() => setShowRename(true)}><PencilIcon size={14} /> Rename</button>
          <button className="sel-bar-action" disabled={selItems.size === 0} onClick={() => setPickerAction('copy')}><CopyIcon size={14} /> Copy</button>
          <button className="sel-bar-action" disabled={selItems.size === 0} onClick={() => setPickerAction('move')}><ScissorsIcon size={14} /> Move</button>
          <button className="sel-bar-action sel-bar-danger" disabled={selItems.size === 0} onClick={() => batchAction('delete')}><TrashIcon size={14} /> Delete</button>
        </div>
      )}

    </div>
    </SelectCtx.Provider>
    </AdminCtx.Provider>
  )
}
