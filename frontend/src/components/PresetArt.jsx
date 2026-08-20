import React from "react"
import { cn } from "@/lib/utils"

// Artwork for the three quality presets.
//
// The three drawings are the same idea at three scales — a screen you are
// watching something on — because that is the actual difference between the
// presets. A cinema, a television, a phone: the picture answers "which of these
// is my setup" faster than "2160p · 1440p · 1080p · 720p" does, and the tier
// list is right underneath for when the answer is not obvious.
//
// Everything is drawn in currentColor at varying opacity, so the set inherits
// the card's state — muted when the card is not chosen, accent when it is — and
// works in both themes without a second palette.

const line = {
  fill: "none",
  stroke: "currentColor",
  strokeWidth: 2,
  strokeLinecap: "round",
  strokeLinejoin: "round",
}

function Frame({ children, className }) {
  return (
    <svg
      viewBox="0 0 120 80"
      role="presentation"
      className={cn("h-20 w-full", className)}
    >
      {children}
    </svg>
  )
}

// Cinema: the widest screen, a beam of light, and the popcorn to go with it.
function CinemaArt() {
  return (
    <Frame>
      {/* projector beam */}
      <path d="M60 62 L26 18 h68 z" fill="currentColor" opacity="0.07" />
      {/* screen */}
      <rect x="18" y="10" width="84" height="42" rx="3" fill="currentColor" opacity="0.10" />
      <rect x="18" y="10" width="84" height="42" rx="3" {...line} />
      <path d="M55 23 l13 8 -13 8 z" fill="currentColor" opacity="0.55" />
      {/* seat backs */}
      <path d="M22 72 v-5 a4 4 0 0 1 4-4 h6 a4 4 0 0 1 4 4 v5" {...line} strokeWidth="1.8" />
      <path d="M42 72 v-5 a4 4 0 0 1 4-4 h6 a4 4 0 0 1 4 4 v5" {...line} strokeWidth="1.8" />
      {/* popcorn */}
      <path d="M74 72 l-3 -13 h20 l-3 13 z" fill="currentColor" opacity="0.14" />
      <path d="M74 72 l-3 -13 h20 l-3 13 z" {...line} strokeWidth="1.8" />
      <path d="M78 59 v13 M84 59 v13" stroke="currentColor" strokeWidth="1.2" opacity="0.4" />
      <circle cx="75" cy="56" r="3.2" fill="currentColor" opacity="0.55" />
      <circle cx="81" cy="53" r="3.6" fill="currentColor" opacity="0.75" />
      <circle cx="87" cy="56" r="3" fill="currentColor" opacity="0.55" />
    </Frame>
  )
}

// Television: the living-room middle ground.
function TelevisionArt() {
  return (
    <Frame>
      <rect x="20" y="12" width="80" height="46" rx="4" fill="currentColor" opacity="0.10" />
      <rect x="20" y="12" width="80" height="46" rx="4" {...line} />
      <path d="M55 27 l13 8 -13 8 z" fill="currentColor" opacity="0.55" />
      {/* stand */}
      <path d="M52 58 l-4 10 M68 58 l4 10" {...line} strokeWidth="1.8" />
      <path d="M42 70 h36" {...line} strokeWidth="1.8" />
      {/* a lamp, so the room reads as a room */}
      <path d="M108 70 v-9" {...line} strokeWidth="1.6" />
      <path d="M103 61 l2.5 -7 h5 l2.5 7 z" fill="currentColor" opacity="0.2" />
      <path d="M103 61 l2.5 -7 h5 l2.5 7 z" {...line} strokeWidth="1.6" />
    </Frame>
  )
}

// Handheld: the smallest screen, and the one on a mobile connection.
function HandheldArt() {
  return (
    <Frame>
      <rect x="45" y="8" width="30" height="56" rx="5" fill="currentColor" opacity="0.10" />
      <rect x="45" y="8" width="30" height="56" rx="5" {...line} />
      <path d="M56 13 h8" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" opacity="0.5" />
      <path d="M57 30 l10 6 -10 6 z" fill="currentColor" opacity="0.55" />
      <path d="M54 58 h12" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" opacity="0.35" />
      {/* signal, arcing away from the device */}
      <path d="M84 40 a10 10 0 0 1 0 -14" {...line} strokeWidth="1.6" opacity="0.55" />
      <path d="M90 46 a19 19 0 0 0 0 -26" {...line} strokeWidth="1.6" opacity="0.3" />
      <path d="M36 40 a10 10 0 0 0 0 -14" {...line} strokeWidth="1.6" opacity="0.55" />
      <path d="M30 46 a19 19 0 0 1 0 -26" {...line} strokeWidth="1.6" opacity="0.3" />
    </Frame>
  )
}

// PresetArt draws the scene for one preset. A preset with no drawing renders
// nothing, so adding a fourth tier does not have to wait for an illustration.
export function PresetArt({ preset }) {
  switch (preset) {
    case "4k":
      return <CinemaArt />
    case "1080p":
      return <TelevisionArt />
    case "720p":
      return <HandheldArt />
    default:
      return null
  }
}
