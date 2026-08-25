interface LogoMarkProps {
  className?: string
}

/** u1s12api 品牌标 —— 圆角方块 + 闪电 U。 */
export function LogoMark({ className }: LogoMarkProps) {
  return (
    <svg viewBox="0 0 48 48" fill="none" className={className} aria-hidden>
      <rect x="2" y="2" width="44" height="44" rx="12" className="fill-primary" />
      <path
        d="M15 13v13.5c0 5.8 4 9.5 9.3 9.5s9.2-3.7 9.2-9.5V13h-6v13.3c0 2.7-1.3 4.4-3.2 4.4s-3.3-1.7-3.3-4.4V13h-6Z"
        className="fill-primary-foreground"
      />
      <path d="m31.5 8 5.5-4-1.8 6.5L40 9l-7 6 .8-4.5L31.5 8Z" className="fill-primary-foreground/80" />
    </svg>
  )
}
