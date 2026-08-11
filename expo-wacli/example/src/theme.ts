/** One small dark palette, kept in a single place so the screens stay about wacli, not about styling. */
export const theme = {
  bg: '#0b0f13',
  surface: '#141b22',
  surfaceAlt: '#1c262f',
  border: '#25313c',
  text: '#e8eef4',
  textMuted: '#8fa3b5',
  accent: '#25d366', // WhatsApp green
  accentText: '#04150a',
  danger: '#ff6b6b',
  warning: '#ffb454',
  bubbleMine: '#134d33',
  bubbleTheirs: '#1c262f',
} as const;

export const radius = { sm: 8, md: 12, lg: 18 } as const;
export const space = { xs: 4, sm: 8, md: 12, lg: 16, xl: 24 } as const;
