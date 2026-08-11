/** Presentation helpers. Nothing wacli-specific — just keeping the screens readable. */

export function formatTime(iso: string): string {
  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) {
    return '';
  }
  const now = new Date();
  const sameDay = date.toDateString() === now.toDateString();
  if (sameDay) {
    return date.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
  }
  const withinWeek = now.getTime() - date.getTime() < 7 * 24 * 60 * 60 * 1000;
  if (withinWeek) {
    return date.toLocaleDateString([], { weekday: 'short' });
  }
  return date.toLocaleDateString([], { month: 'short', day: 'numeric' });
}

/** wacli stores JIDs like `15551234567@s.whatsapp.net`; show the part a human recognises. */
export function displayName(name: string, jid: string): string {
  if (name && name.trim()) {
    return name;
  }
  const local = jid.split('@')[0] ?? jid;
  return local.startsWith('+') ? local : `+${local}`;
}

/** Surface wacli's own error text — "DND mode is off", "chat is locked" — rather than a generic one. */
export function errorMessage(error: unknown): string {
  if (error instanceof Error) {
    return error.message.replace(/^wacli:\s*/, '');
  }
  return String(error);
}
