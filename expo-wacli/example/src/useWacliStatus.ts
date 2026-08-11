import { useCallback, useEffect, useState } from 'react';
import * as Wacli from 'expo-wacli';

export type ConnectionState = {
  connected: boolean;
  dndMode: boolean;
  chatCount: number;
  messageCount: number;
  userJID?: string;
};

const EMPTY: ConnectionState = {
  connected: false,
  dndMode: false,
  chatCount: 0,
  messageCount: 0,
};

/**
 * Keeps a snapshot of wacli's status in React state.
 *
 * Refreshed two ways, because neither alone is enough: `connection_state` events tell us the
 * instant the socket goes up or down, and a slow poll catches the counters, which change as
 * messages land without an event of their own.
 */
export function useWacliStatus() {
  const [status, setStatus] = useState<ConnectionState>(EMPTY);

  const refresh = useCallback(async () => {
    try {
      const snapshot = await Wacli.getStatus();
      setStatus({
        connected: snapshot.connected,
        dndMode: snapshot.dnd_mode,
        chatCount: snapshot.chat_count,
        messageCount: snapshot.message_count,
        userJID: snapshot.user_jid,
      });
    } catch {
      // The service may not be running yet; the next tick will pick it up.
    }
  }, []);

  useEffect(() => {
    refresh();
    const timer = setInterval(refresh, 10_000);
    const subscription = Wacli.addListener('onEvent', ({ event }) => {
      if (event === 'connection_state' || event === 'sync_complete') {
        refresh();
      }
    });
    return () => {
      clearInterval(timer);
      subscription.remove();
    };
  }, [refresh]);

  const setDND = useCallback(
    async (enabled: boolean) => {
      const applied = await Wacli.setDND(enabled);
      setStatus((current) => ({ ...current, dndMode: applied }));
      return applied;
    },
    []
  );

  return { status, refresh, setDND };
}
